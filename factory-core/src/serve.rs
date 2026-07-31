use std::path::Path;
use std::sync::Arc;

use anyhow::{Context, Result, bail};
use axum::extract::{Request, State};
use axum::http::StatusCode;
use axum::http::header;
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde::Serialize;
use tokio::net::TcpListener;
use tokio_util::sync::CancellationToken;

use crate::config::Config;

const DEFAULT_PORT: u16 = 7788;

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

/// The per-container access token the ui injects via env (C2 credential
/// chain). `serve` refuses to start without it (fail-fast) so a
/// network-reachable core is never usable without credentials.
fn api_token() -> Result<String> {
    let value = std::env::var_os("FACTORY_API_TOKEN")
        .context("FACTORY_API_TOKEN is not set; the control plane requires an access token")?;
    let token = value
        .into_string()
        .map_err(|_| anyhow::anyhow!("FACTORY_API_TOKEN is not valid UTF-8"))?;
    if token.trim().is_empty() {
        bail!("FACTORY_API_TOKEN must not be empty");
    }
    Ok(token)
}

#[derive(Debug, Serialize)]
struct ErrorBody {
    error: ErrorDetail,
}

#[derive(Debug, Serialize)]
struct ErrorDetail {
    code: &'static str,
    message: String,
}

/// Every error response shares the `{error:{code,message}}` envelope, and the
/// message is sanitized with the same redaction policy applied to ledger
/// persistence before storage so a failure can never leak credentials.
fn error_response(status: StatusCode, code: &'static str, message: &str) -> Response {
    let body = ErrorBody {
        error: ErrorDetail {
            code,
            message: crate::inspection::sanitize_for_storage(message),
        },
    };
    (status, Json(body)).into_response()
}

#[derive(Clone)]
struct AppState {
    config: Arc<Config>,
    token: Arc<String>,
}

/// Serve the control plane: anonymous `GET /api/v1/health` plus the
/// token-protected HTTP/SSE surface. Everything except health requires the
/// per-container bearer token.
pub async fn serve(config: &Config, port: u16, cancellation: CancellationToken) -> Result<()> {
    let token = api_token()?;
    let state = AppState {
        config: Arc::new(config.clone()),
        token: Arc::new(token),
    };
    let app = build_router(state);
    let listener = TcpListener::bind(("0.0.0.0", port))
        .await
        .with_context(|| format!("failed to bind the Factory control plane on port {port}"))?;
    axum::serve(listener, app)
        .with_graceful_shutdown(async move { cancellation.cancelled().await })
        .await
        .context("the Factory control plane failed")
}

fn build_router(state: AppState) -> Router {
    // Public surface: anonymous health only.
    let public = Router::new().route("/api/v1/health", get(health_handler));

    // Protected surface: every other route requires the bearer token. `/events`
    // additionally accepts `?token=` because EventSource cannot set headers.
    let protected = Router::new()
        .route("/events", get(events_placeholder))
        .route("/api/v1/tasks", get(unimplemented_handler))
        .route("/api/v1/runs", get(unimplemented_handler))
        .route("/api/v1/status", get(unimplemented_handler))
        .route_layer(middleware::from_fn_with_state(state.clone(), require_auth));

    public
        .merge(protected)
        .fallback(not_found_handler)
        .layer(middleware::from_fn(no_cache_headers))
        .with_state(state)
}

async fn health_handler(State(state): State<AppState>) -> Json<HealthReport> {
    Json(health_report(&state.config))
}

async fn events_placeholder() -> Response {
    error_response(
        StatusCode::NOT_IMPLEMENTED,
        "not_implemented",
        "the SSE event stream arrives in a later change",
    )
}

async fn unimplemented_handler() -> Response {
    error_response(
        StatusCode::NOT_IMPLEMENTED,
        "not_implemented",
        "this control-plane endpoint arrives in a later change",
    )
}

async fn not_found_handler() -> Response {
    error_response(StatusCode::NOT_FOUND, "not_found", "unknown path")
}

/// Bearer-token middleware. Accepts the `Authorization: Bearer <token>` header
/// or, for `/events` only, a `?token=` query parameter (EventSource cannot set
/// custom headers).
async fn require_auth(
    State(state): State<AppState>,
    request: Request,
    next: Next,
) -> Result<Response, Response> {
    let header_token = request
        .headers()
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .map(str::trim);
    let query_token = request
        .uri()
        .query()
        .and_then(|query| {
            query.split('&').find_map(|pair| {
                let (key, value) = pair.split_once('=')?;
                (key == "token").then_some(value)
            })
        });
    let presented = header_token.or(query_token);
    match presented {
        Some(presented) if constant_time_eq(presented.as_bytes(), state.token.as_bytes()) => {
            Ok(next.run(request).await)
        }
        _ => Err(error_response(
            StatusCode::UNAUTHORIZED,
            "unauthorized",
            "a valid bearer token is required",
        )),
    }
}

/// Compare without an early-exit timing side channel.
fn constant_time_eq(left: &[u8], right: &[u8]) -> bool {
    if left.len() != right.len() {
        return false;
    }
    let mut difference = 0u8;
    for (a, b) in left.iter().zip(right.iter()) {
        difference |= a ^ b;
    }
    difference == 0
}

/// Every response carries `Cache-Control: no-cache` (R2 §总则).
async fn no_cache_headers(request: Request, next: Next) -> Response {
    let mut response = next.run(request).await;
    response.headers_mut().insert(
        header::CACHE_CONTROL,
        header::HeaderValue::from_static("no-cache"),
    );
    response
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
    use super::{constant_time_eq, serve_port};

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

    #[test]
    fn constant_time_eq_compares_bytes() {
        assert!(constant_time_eq(b"secret", b"secret"));
        assert!(!constant_time_eq(b"secret", b"secreu"));
        assert!(!constant_time_eq(b"secret", b"shorter"));
    }
}
