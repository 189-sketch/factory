//! Cross-platform process helpers (W4.0, #38).
//!
//! Factory was Unix-only; these shims give the few call sites that need a
//! synthetic `ExitStatus` a portable definition. On Unix an exit code is encoded
//! as `code << 8` in the raw wait status; on Windows the raw status *is* the
//! process exit code. Process-group management (process-group anchors, killpg)
//! has no direct Windows analogue and stays `cfg(unix)`; callers already degrade
//! to a no-op on Windows via `RunProcessGroup`'s `cfg(not(unix))` branch.

use std::path::{Path, PathBuf};
use std::process::ExitStatus;

/// Canonicalize a path into a form safe to hand to external tools.
///
/// On Windows, `std::fs::canonicalize` returns a verbatim (`\\?\C:\...`) path
/// that git and many other tools reject (`Invalid argument`). `dunce` converts
/// it back to a familiar `C:\...` path whenever that is safe. On Unix this is
/// just `std::fs::canonicalize`.
pub(crate) fn canonicalize(path: &Path) -> std::io::Result<PathBuf> {
    #[cfg(windows)]
    {
        dunce::canonicalize(path)
    }
    #[cfg(not(windows))]
    {
        std::fs::canonicalize(path)
    }
}

/// A synthetic `ExitStatus` representing a generic non-zero (failure) exit.
///
/// Used when a run is cancelled/times out before the child produces a real
/// status, so callers still receive a well-formed `ExecutionResult`.
pub(crate) fn failure_exit_status() -> ExitStatus {
    failure_exit_status_with_code(1)
}

#[cfg(unix)]
fn failure_exit_status_with_code(code: i32) -> ExitStatus {
    use std::os::unix::process::ExitStatusExt;
    ExitStatus::from_raw(code << 8)
}

#[cfg(windows)]
fn failure_exit_status_with_code(code: i32) -> ExitStatus {
    use std::os::windows::process::ExitStatusExt;
    ExitStatus::from_raw(code as u32)
}
