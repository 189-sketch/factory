use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use anyhow::{Context, Result, bail};
use tokio_util::sync::CancellationToken;

use crate::config::{Config, ExecutionMode, repository_config_path};
use crate::init::{InitOptions, initialize};
use crate::runtime::write_stderr_best_effort;
use crate::storage::Ledger;

const DEFAULT_WORK_DIR: &str = "/factory/work";

#[derive(Debug)]
struct EntrypointSettings {
    git_url: String,
    branch: Option<String>,
    work_root: PathBuf,
    run_command: Option<PathBuf>,
}

impl EntrypointSettings {
    fn from_env() -> Result<Self> {
        let git_url = std::env::var("FACTORY_GIT_URL")
            .context("FACTORY_GIT_URL is required for `factory agent-entrypoint`")?;
        if git_url.trim().is_empty() {
            bail!("FACTORY_GIT_URL must not be empty");
        }
        let branch = std::env::var("FACTORY_BRANCH")
            .ok()
            .map(|value| value.trim().to_owned())
            .filter(|value| !value.is_empty());
        let work_root = std::env::var_os("FACTORY_WORK_DIR")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from(DEFAULT_WORK_DIR));
        // Test seam: stand in for the long-running polling loop without
        // driving real workflows. Production containers never set this.
        let run_command = std::env::var_os("FACTORY_RUN_COMMAND").map(PathBuf::from);
        Ok(Self {
            git_url,
            branch,
            work_root,
            run_command,
        })
    }
}

/// Bootstrap a container from environment variables: clone, restore any
/// snapshot retention branch, scaffold `.factory/` idempotently, then run the
/// control plane and the polling loop concurrently over one shared Config and
/// Ledger until either stops.
pub async fn agent_entrypoint(cancellation: CancellationToken) -> Result<u8> {
    let settings = EntrypointSettings::from_env().map_err(sanitize_error)?;
    let repository = prepare_repository(&settings).map_err(sanitize_error)?;
    scaffold(&repository).map_err(sanitize_error)?;
    let config = crate::serve::control_plane_config(&repository).map_err(sanitize_error)?;
    let port = crate::serve::serve_port().map_err(sanitize_error)?;

    let serve_cancellation = cancellation.clone();
    let serve_config = config.clone();
    let serve =
        tokio::spawn(
            async move { crate::serve::serve(&serve_config, port, serve_cancellation).await },
        );
    let outcome = run_polling_loop(&settings, config, cancellation.clone()).await;
    cancellation.cancel();
    let serve_result = serve
        .await
        .context("factory serve task panicked")
        .map_err(sanitize_error)?;
    match (outcome, serve_result) {
        (Err(error), _) => Err(sanitize_error(error)),
        (Ok(()), Err(error)) => Err(sanitize_error(error)),
        (Ok(()), Ok(())) => Ok(0),
    }
}

fn prepare_repository(settings: &EntrypointSettings) -> Result<PathBuf> {
    let identity = crate::config::canonical_github_identity(&settings.git_url)
        .context("FACTORY_GIT_URL is not a supported GitHub URL")?;
    let clone_url = clone_source_override(&settings.git_url);
    let repository = settings.work_root.join(repository_directory(&identity)?);
    if repository.join(".git").is_dir() {
        git(&repository, &["fetch", "--no-tags", "origin"])?;
    } else {
        std::fs::create_dir_all(&settings.work_root).with_context(|| {
            format!(
                "failed to create the Factory work root {}",
                settings.work_root.display()
            )
        })?;
        // Clone credentials arrive as environment and are passed straight
        // through to git (GH_TOKEN/GIT_ASKPASS and friends); the URL itself
        // stays free of embedded tokens.
        let status = Command::new("git")
            .arg("clone")
            .arg("--no-tags")
            .arg(&clone_url)
            .arg(&repository)
            .status()
            .context("failed to start git clone")?;
        if !status.success() {
            if repository.exists() {
                let _ = std::fs::remove_dir_all(&repository);
            }
            bail!("clone failed with {status}");
        }
        // Keep the canonical remote identity visible to `factory init` and the
        // health report even when the clone came from a local mirror.
        git(
            &repository,
            &["remote", "set-url", "origin", &settings.git_url],
        )?;
    }
    restore_snapshot_or_branch(&repository, &identity, settings.branch.as_deref())?;
    Ok(repository)
}

fn restore_snapshot_or_branch(
    repository: &Path,
    identity: &str,
    requested_branch: Option<&str>,
) -> Result<()> {
    let snapshot = format!("factory/snapshot/{}", repository_directory(identity)?);
    let remote_snapshot = format!("refs/remotes/origin/{snapshot}");
    if git_ok(
        repository,
        &["show-ref", "--verify", "--quiet", &remote_snapshot],
    )? {
        git(repository, &["checkout", "-B", &snapshot, &remote_snapshot])?;
        write_stderr_best_effort(
            format!("Factory restored snapshot branch {snapshot}\n").as_bytes(),
        );
        return Ok(());
    }
    if let Some(branch) = requested_branch {
        let remote_branch = format!("refs/remotes/origin/{branch}");
        if git_ok(
            repository,
            &["show-ref", "--verify", "--quiet", &remote_branch],
        )? {
            git(repository, &["checkout", "--detach", &remote_branch])?;
        } else {
            git(repository, &["checkout", "--detach", branch])?;
        }
        return Ok(());
    }
    // Default: stay detached on the default branch tip so workflows choose
    // their own baseline, matching the onboarding contract.
    git(repository, &["checkout", "--detach", "HEAD"])?;
    Ok(())
}

fn scaffold(repository: &Path) -> Result<()> {
    let report = initialize(InitOptions {
        config_path: repository_config_path(repository),
        repository: repository.to_path_buf(),
        check: false,
        execution_mode: ExecutionMode::Worktree,
    })?;
    if report.exit_code() != 0 {
        bail!("factory init did not complete:\n{report}");
    }
    Ok(())
}

async fn run_polling_loop(
    settings: &EntrypointSettings,
    config: Config,
    cancellation: CancellationToken,
) -> Result<()> {
    // Test seam: stand in for the long-running polling loop.
    if let Some(stub) = &settings.run_command {
        return run_stub(stub, cancellation).await;
    }
    let catalog = crate::workflow::WorkflowCatalog::load(&config)?;
    let ledger = Ledger::open_in(&config.data_directory)?;
    crate::daemon::run_repository_daemon(config, catalog, ledger.path(), cancellation).await
}

async fn run_stub(stub: &Path, cancellation: CancellationToken) -> Result<()> {
    let mut child = Command::new(stub)
        .stdin(Stdio::null())
        .spawn()
        .with_context(|| format!("failed to start the run stub {}", stub.display()))?;
    tokio::select! {
        status = tokio::task::spawn_blocking(move || child.wait()) => {
            let status = status
                .context("run stub task panicked")?
                .context("failed to wait for the run stub")?;
            if !status.success() {
                bail!("run stub exited with {status}");
            }
            Ok(())
        }
        () = cancellation.cancelled() => {
            bail!("factory agent-entrypoint was cancelled")
        }
    }
}

fn clone_source_override(git_url: &str) -> String {
    // Test seam: point GitHub URLs at a local mirror instead of the network.
    // Production containers never set FACTORY_GIT_URL_BASE.
    let Some(base) = std::env::var_os("FACTORY_GIT_URL_BASE").map(OsString::into_string) else {
        return git_url.to_owned();
    };
    let Ok(base) = base else {
        return git_url.to_owned();
    };
    if crate::config::canonical_github_identity(git_url).is_ok() {
        return base;
    }
    git_url.to_owned()
}

fn repository_directory(identity: &str) -> Result<&str> {
    identity
        .rsplit('/')
        .next()
        .filter(|value| !value.is_empty())
        .context("repository identity has no name")
}

fn git(repository: &Path, arguments: &[&str]) -> Result<()> {
    let status = Command::new("git")
        .args(arguments)
        .current_dir(repository)
        .status()
        .context("failed to start git")?;
    if !status.success() {
        bail!("git {} failed with {status}", arguments.join(" "));
    }
    Ok(())
}

fn git_ok(repository: &Path, arguments: &[&str]) -> Result<bool> {
    let status = Command::new("git")
        .args(arguments)
        .current_dir(repository)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .context("failed to start git")?;
    Ok(status.success())
}

fn sanitize_error(error: anyhow::Error) -> anyhow::Error {
    anyhow::anyhow!(
        "{}",
        crate::inspection::sanitize_for_storage(&format!("{error:#}"))
    )
}

#[cfg(test)]
mod tests {
    use super::sanitize_error;

    #[test]
    fn sanitized_errors_redact_embedded_credentials() {
        let error = anyhow::anyhow!(
            "clone failed for https://x-access-token:ghp_secret0123456789@github.com/example/repository.git"
        );
        let sanitized = sanitize_error(error).to_string();
        assert!(!sanitized.contains("ghp_secret"));
        assert!(sanitized.contains("[REDACTED]"));
    }
}
