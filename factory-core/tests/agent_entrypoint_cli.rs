#![cfg(unix)]

use std::fs;
use std::io::Read;
use std::net::TcpStream;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command as ProcessCommand, Stdio};
use std::time::{Duration, Instant};

use predicates::prelude::PredicateBooleanExt;

struct Fixture {
    _temp: tempfile::TempDir,
    home: PathBuf,
    data_home: PathBuf,
    work_root: PathBuf,
    bare_remote: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let home = temp.path().join("home");
        let work_root = temp.path().join("work");
        let data_home = temp.path().join("factory-data");
        fs::create_dir_all(&home).unwrap();
        fs::create_dir_all(&work_root).unwrap();

        // Build a local bare repository that impersonates the GitHub remote.
        let seed = temp.path().join("seed");
        fs::create_dir_all(&seed).unwrap();
        git(&seed, &["init", "--quiet"]);
        git(&seed, &["symbolic-ref", "HEAD", "refs/heads/main"]);
        fs::write(seed.join("README.md"), "fixture repository\n").unwrap();
        git(&seed, &["add", "README.md"]);
        git(
            &seed,
            &[
                "-c",
                "user.name=Fixture",
                "-c",
                "user.email=fixture@example.com",
                "commit",
                "--quiet",
                "-m",
                "initial",
            ],
        );
        let bare_remote = temp.path().join("remote.git");
        git(
            temp.path(),
            &[
                "clone",
                "--quiet",
                "--bare",
                seed.to_str().unwrap(),
                bare_remote.to_str().unwrap(),
            ],
        );
        git(&bare_remote, &["symbolic-ref", "HEAD", "refs/heads/main"]);
        git(
            &seed,
            &["remote", "add", "origin", bare_remote.to_str().unwrap()],
        );

        Self {
            _temp: temp,
            home,
            data_home,
            work_root,
            bare_remote,
        }
    }

    fn github_url(&self) -> String {
        // An HTTPS GitHub URL whose authority redirects to the local bare
        // repository via FACTORY_GIT_URL_BASE instead of network access.
        "https://github.com/example/repository.git".to_owned()
    }

    fn spawn_entrypoint(&self, port: u16) -> Child {
        let run_stub = self.work_root.join("run-stub");
        run_stub_binary(&run_stub);
        let binary = assert_cmd::cargo::cargo_bin("factory");
        ProcessCommand::new(binary)
            .arg("agent-entrypoint")
            .env("HOME", &self.home)
            .env("FACTORY_DATA_HOME", &self.data_home)
            .env("FACTORY_WORK_DIR", &self.work_root)
            .env("FACTORY_GIT_URL", self.github_url())
            .env("FACTORY_GIT_URL_BASE", &self.bare_remote)
            .env("FACTORY_RUN_COMMAND", &run_stub)
            .env("FACTORY_PORT", port.to_string())
            .env_remove("FACTORY_BRANCH")
            .current_dir(&self.work_root)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap()
    }
}

fn git(path: &Path, arguments: &[&str]) {
    let status = ProcessCommand::new("git")
        .args(arguments)
        .current_dir(path)
        .status()
        .unwrap();
    assert!(status.success(), "git {arguments:?} failed in {path:?}");
}

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

fn run_stub_binary(path: &Path) {
    fs::write(
        path,
        "#!/bin/sh\n# Stub standing in for the long-running `factory run` polling loop.\nsleep 3600\n",
    )
    .unwrap();
    let mut permissions = fs::metadata(path).unwrap().permissions();
    permissions.set_mode(0o700);
    fs::set_permissions(path, permissions).unwrap();
}

fn wait_for_health(child: &mut Child, port: u16) -> String {
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        if let Some(status) = child.try_wait().unwrap() {
            let mut stderr = String::new();
            child
                .stderr
                .take()
                .unwrap()
                .read_to_string(&mut stderr)
                .unwrap();
            panic!("agent-entrypoint exited early with {status}: {stderr}");
        }
        if let Ok(mut stream) = TcpStream::connect(("127.0.0.1", port)) {
            stream
                .set_read_timeout(Some(Duration::from_secs(2)))
                .unwrap();
            use std::io::Write;
            if write!(
                stream,
                "GET /api/v1/health HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
            )
            .is_ok()
            {
                let mut raw = Vec::new();
                if stream.read_to_end(&mut raw).is_ok() && !raw.is_empty() {
                    return String::from_utf8(raw).unwrap();
                }
            }
        }
        assert!(
            Instant::now() < deadline,
            "agent-entrypoint serve did not come up"
        );
        std::thread::sleep(Duration::from_millis(150));
    }
}

#[test]
fn agent_entrypoint_requires_git_url_and_sanitizes_output() {
    let temp = tempfile::tempdir().unwrap();
    let home = temp.path().join("home");
    fs::create_dir(&home).unwrap();
    let mut command = assert_cmd::Command::cargo_bin("factory").unwrap();
    command
        .env("HOME", &home)
        .env("FACTORY_DATA_HOME", temp.path().join("data"))
        .env("FACTORY_WORK_DIR", temp.path().join("work"))
        .env_remove("FACTORY_GIT_URL")
        .env("FACTORY_API_TOKEN", "ghp_secret0123456789abcdef0123456789")
        .arg("agent-entrypoint")
        .assert()
        .failure()
        .stderr(predicates::str::contains("FACTORY_GIT_URL"))
        .stderr(predicates::str::contains("ghp_secret").not());
}

#[test]
fn agent_entrypoint_clone_failure_exits_nonzero_and_sanitizes() {
    let temp = tempfile::tempdir().unwrap();
    let home = temp.path().join("home");
    let work = temp.path().join("work");
    fs::create_dir_all(&home).unwrap();
    fs::create_dir_all(&work).unwrap();
    let mut command = assert_cmd::Command::cargo_bin("factory").unwrap();
    command
        .env("HOME", &home)
        .env("FACTORY_DATA_HOME", temp.path().join("data"))
        .env("FACTORY_WORK_DIR", &work)
        .env(
            "FACTORY_GIT_URL",
            "https://x-access-token:ghp_secret0123456789abcdef0123456789@github.com/example/missing.git",
        )
        .env("FACTORY_GIT_URL_BASE", "/nonexistent/remote.git")
        .arg("agent-entrypoint")
        .assert()
        .failure()
        .stderr(predicates::str::contains("clone"))
        .stderr(predicates::str::contains("ghp_secret").not())
        .stderr(predicates::str::contains("x-access-token:ghp").not());
    assert!(!work.join("repository").exists());
}

#[test]
fn agent_entrypoint_bootstraps_repository_and_serves_health() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_entrypoint(port);

    let raw = wait_for_health(&mut child, port);
    let (_, body) = raw.split_once("\r\n\r\n").unwrap();
    let body: serde_json::Value = serde_json::from_str(body).unwrap();
    assert_eq!(body["status"], "ok");
    assert_eq!(body["repository"], "example/repository");

    // clone landed at <work>/<repo>, detached on the default branch tip.
    let clone = fixture.work_root.join("repository");
    assert!(clone.join(".git").is_dir());
    assert_eq!(
        fs::read_to_string(clone.join("README.md")).unwrap(),
        "fixture repository\n"
    );
    // init scaffolded the repository configuration.
    assert!(clone.join(".factory/config.toml").is_file());
    assert!(clone.join(".factory/workflows/triage.md").is_file());

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn agent_entrypoint_init_is_idempotent_and_restores_snapshot_branch() {
    let fixture = Fixture::new();

    // Pre-create committed configuration in the remote so init must skip
    // scaffolding, plus a snapshot retention branch to restore from.
    let seed = fixture._temp.path().join("seed");
    fs::create_dir_all(seed.join(".factory")).unwrap();
    let committed_config = r#"version = 1
poll_every = "30s"

[worker]
runtime = "codex"
sandbox = "worktree"
timeout = "2h"
maximum_timeout = "8h"
max_concurrent = 1

[source]
command = [".factory/sources/github"]

[trigger]

[trigger.triage]
type = "source"
state = "open"
labels = ["factory:ready-for-spec"]
workflow = ".factory/workflows/triage.md"
"#;
    fs::write(seed.join(".factory/config.toml"), committed_config).unwrap();
    git(&seed, &["add", ".factory/config.toml"]);
    git(
        &seed,
        &[
            "-c",
            "user.name=Fixture",
            "-c",
            "user.email=fixture@example.com",
            "commit",
            "--quiet",
            "-m",
            "add factory config",
        ],
    );
    git(&seed, &["push", "--quiet", "origin", "main"]);
    git(
        &seed,
        &["checkout", "--quiet", "-b", "factory/snapshot/repository"],
    );
    fs::write(seed.join("snapshot-state.txt"), "resumed work\n").unwrap();
    git(&seed, &["add", "snapshot-state.txt"]);
    git(
        &seed,
        &[
            "-c",
            "user.name=Fixture",
            "-c",
            "user.email=fixture@example.com",
            "commit",
            "--quiet",
            "-m",
            "snapshot state",
        ],
    );
    git(
        &seed,
        &["push", "--quiet", "origin", "factory/snapshot/repository"],
    );

    let port = free_port();
    let mut child = fixture.spawn_entrypoint(port);
    let raw = wait_for_health(&mut child, port);
    let (_, body) = raw.split_once("\r\n\r\n").unwrap();
    let body: serde_json::Value = serde_json::from_str(body).unwrap();
    assert_eq!(body["status"], "ok");

    let clone = fixture.work_root.join("repository");
    // The snapshot branch was restored: its file is present and HEAD is on it.
    assert_eq!(
        fs::read_to_string(clone.join("snapshot-state.txt")).unwrap(),
        "resumed work\n"
    );
    let branch = ProcessCommand::new("git")
        .args(["branch", "--show-current"])
        .current_dir(&clone)
        .output()
        .unwrap();
    assert_eq!(
        String::from_utf8(branch.stdout).unwrap().trim(),
        "factory/snapshot/repository"
    );
    // The committed config survived: init did not overwrite it with the template.
    assert_eq!(
        fs::read_to_string(clone.join(".factory/config.toml")).unwrap(),
        committed_config
    );

    child.kill().unwrap();
    child.wait().unwrap();
}
