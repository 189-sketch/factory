// Cross-platform: historically Unix-only. W4.0 (#38) ports the HTTP/SSE serve
// path to Windows; remaining Unix-specific behaviours are gated `cfg(unix)` and
// degrade gracefully (or are unsupported) on Windows.
pub mod approval;
pub mod clone;
pub mod config;
pub mod daemon;
pub mod entrypoint;
pub mod events;
pub mod execution;
pub mod fleet;
pub mod github;
mod hash;
pub mod init;
pub mod inspection;
mod platform;
pub mod runtime;
pub mod sandbox;
pub mod serve;
pub mod source;
pub mod storage;
mod table;
pub mod workflow;
pub mod workspace;
