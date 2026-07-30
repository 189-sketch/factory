use std::path::Path;

use anyhow::{Context, Result, bail};
use serde::Serialize;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio_util::sync::CancellationToken;

use crate::config::Config;

const DEFAULT_PORT: u16 = 7788;
const MAX_HEADER_BYTES: usize = 16 * 1024;

#[derive(Debug, Serialize)]
pub struct HealthReport {
    pub status: &'static str,
    pub version: &'static str,
    pub repository: String,
}

pub fn health_report(config: &Config) -> HealthReport {
    HealthReport {
        status: "ok",
        version: env!("CARGO_PKG_VERSION"),
        repository: crate::config::repository_remote_identity(&config.repositories[0])
            .unwrap_or_else(|_| "unknown".to_owned()),
    }
}

pub fn serve_port() -> Result<u16> {
    let Some(value) = std::env::var_os("FACTORY_PORT") else {
        return Ok(DEFAULT_PORT);
    };
    let value = value
        .into_string()
        .map_err(|_| anyhow::anyhow!("FACTORY_PORT is not valid UTF-8"))?;
    let port = value
        .trim()
        .parse::<u16>()
        .with_context(|| format!("FACTORY_PORT {value:?} is not a valid port"))?;
    if port == 0 {
        bail!("FACTORY_PORT must not be 0");
    }
    Ok(port)
}

/// Serve the minimal control plane: `GET /api/v1/health` only.
/// Everything else answers 404; authentication arrives with the full
/// HTTP/SSE surface in a later change (A2 allows anonymous health).
pub async fn serve(config: &Config, port: u16, cancellation: CancellationToken) -> Result<()> {
    let health = serde_json::to_vec(&health_report(config))
        .context("failed to encode the health response")?;
    let listener = TcpListener::bind(("0.0.0.0", port))
        .await
        .with_context(|| format!("failed to bind the Factory control plane on port {port}"))?;
    loop {
        let (mut stream, _) = tokio::select! {
            accepted = listener.accept() => accepted.context("failed to accept a control-plane connection")?,
            () = cancellation.cancelled() => return Ok(()),
        };
        let health = health.clone();
        let connection = cancellation.clone();
        tokio::spawn(async move {
            let _ = handle_connection(&mut stream, &health, connection).await;
        });
    }
}

async fn handle_connection(
    stream: &mut tokio::net::TcpStream,
    health: &[u8],
    cancellation: CancellationToken,
) -> Result<()> {
    let mut request = Vec::new();
    let mut buffer = [0u8; 4096];
    let header_end = loop {
        if let Some(end) = find_header_end(&request) {
            break end;
        }
        if request.len() > MAX_HEADER_BYTES {
            respond(
                stream,
                431,
                "Request Header Fields Too Large",
                "text/plain",
                b"",
            )
            .await?;
            return Ok(());
        }
        let read = tokio::select! {
            read = stream.read(&mut buffer) => read.context("failed to read a control-plane request")?,
            () = cancellation.cancelled() => return Ok(()),
        };
        if read == 0 {
            return Ok(());
        }
        request.extend_from_slice(&buffer[..read]);
    };
    let head = String::from_utf8_lossy(&request[..header_end]);
    let mut request_line = head.split("\r\n").next().unwrap_or("").split_whitespace();
    let (method, path) = (request_line.next(), request_line.next());
    match (method, path) {
        (Some("GET"), Some(path)) if path.split('?').next() == Some("/api/v1/health") => {
            respond(stream, 200, "OK", "application/json", health).await
        }
        (Some("GET"), Some(_)) => {
            respond(stream, 404, "Not Found", "text/plain", b"not found\n").await
        }
        _ => {
            respond(
                stream,
                405,
                "Method Not Allowed",
                "text/plain",
                b"method not allowed\n",
            )
            .await
        }
    }
}

fn find_header_end(request: &[u8]) -> Option<usize> {
    request
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .map(|position| position + 4)
}

async fn respond(
    stream: &mut tokio::net::TcpStream,
    status: u16,
    reason: &str,
    content_type: &str,
    body: &[u8],
) -> Result<()> {
    let response = format!(
        "HTTP/1.1 {status} {reason}\r\ncontent-type: {content_type}\r\ncontent-length: {}\r\nconnection: close\r\n\r\n",
        body.len()
    );
    stream
        .write_all(response.as_bytes())
        .await
        .context("failed to write a control-plane response")?;
    stream
        .write_all(body)
        .await
        .context("failed to write a control-plane response body")
}

pub fn control_plane_config(repository: &Path) -> Result<Config> {
    let path = crate::config::repository_config_path(repository);
    Config::load(&path).with_context(|| {
        format!(
            "failed to load the Factory configuration {}; run `factory init` first",
            path.display()
        )
    })
}

#[cfg(test)]
mod tests {
    use super::{find_header_end, serve_port};

    #[test]
    fn header_end_matches_only_the_crlf_crlf_boundary() {
        assert_eq!(find_header_end(b"GET / HTTP/1.1\r\n\r\n"), Some(18));
        assert_eq!(
            find_header_end(b"GET / HTTP/1.1\r\nA: b\r\n\r\nrest"),
            Some(24)
        );
        assert_eq!(find_header_end(b"GET / HTTP/1.1\r\n\n"), None);
        assert_eq!(find_header_end(b""), None);
    }

    #[test]
    fn serve_port_defaults_and_validates() {
        // SAFETY: process-unique variable mutated only by this test binary's
        // serve_port tests, which do not run in parallel with each other.
        unsafe { std::env::remove_var("FACTORY_PORT") };
        assert_eq!(serve_port().unwrap(), 7788);
        unsafe { std::env::set_var("FACTORY_PORT", "8080") };
        assert_eq!(serve_port().unwrap(), 8080);
        unsafe { std::env::set_var("FACTORY_PORT", "0") };
        assert!(serve_port().is_err());
        unsafe { std::env::set_var("FACTORY_PORT", "not-a-port") };
        assert!(serve_port().is_err());
        unsafe { std::env::set_var("FACTORY_PORT", "70000") };
        assert!(serve_port().is_err());
        unsafe { std::env::remove_var("FACTORY_PORT") };
    }
}
