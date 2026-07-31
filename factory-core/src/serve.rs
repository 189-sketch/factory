use std::convert::Infallible;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use axum::extract::{Path as AxumPath, Query, Request, State};
use axum::http::StatusCode;
use axum::http::header;
use axum::middleware::{self, Next};
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::{Deserialize, Serialize};
use tokio::net::TcpListener;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use crate::config::Config;
use crate::inspection::{RunView, TaskView};
use crate::storage::{
    CancellationRequest, DATABASE_NAME, Ledger, LedgerEvent, RunContainer, RunSandbox, TaskState,
};

const DEFAULT_PORT: u16 = 7788;

/// The current event-envelope version (schema/events/envelope.json). The
/// handshake compares the ui's requested `?v=` against this.
const ENVELOPE_VERSION: u32 = 1;

/// SSE idle heartbeat interval (R2 §2: 15-30s keep-alive).
const HEARTBEAT_INTERVAL: Duration = Duration::from_secs(15);

/// Advisory EventSource reconnect delay, sent as `retry:` on each frame.
const SSE_RETRY: Duration = Duration::from_millis(3000);

/// How often the SSE stream polls the durable watermark for events committed
/// by other Ledger connections. Bounds cross-connection fanout latency.
const POLL_INTERVAL: Duration = Duration::from_millis(200);

/// How often the repo.health snapshot is aggregated and (on change) emitted.
/// Overridable via FACTORY_REPO_HEALTH_INTERVAL_MS for tests.
const REPO_HEALTH_INTERVAL: Duration = Duration::from_secs(30);

fn repo_health_interval() -> Duration {
    std::env::var("FACTORY_REPO_HEALTH_INTERVAL_MS")
        .ok()
        .and_then(|value| value.parse::<u64>().ok())
        .filter(|millis| *millis > 0)
        .map(Duration::from_millis)
        .unwrap_or(REPO_HEALTH_INTERVAL)
}

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
    database: Arc<PathBuf>,
}

/// Serve the control plane: anonymous `GET /api/v1/health` plus the
/// token-protected HTTP/SSE surface. Everything except health requires the
/// per-container bearer token.
pub async fn serve(config: &Config, port: u16, cancellation: CancellationToken) -> Result<()> {
    let token = api_token()?;
    let database = Arc::new(config.data_directory.join(DATABASE_NAME));
    let state = AppState {
        config: Arc::new(config.clone()),
        token: Arc::new(token),
        database: database.clone(),
    };
    let repository = crate::config::repository_remote_identity(&config.repositories[0])
        .unwrap_or_else(|_| "unknown".to_owned());
    // Emit repo.health periodically and whenever the aggregated snapshot
    // changes, so the overview card tracks live ledger state.
    let health_task = tokio::spawn(emit_repo_health(
        database,
        repository,
        cancellation.clone(),
    ));
    let app = build_router(state);
    let listener = TcpListener::bind(("0.0.0.0", port))
        .await
        .with_context(|| format!("failed to bind the Factory control plane on port {port}"))?;
    axum::serve(listener, app)
        .with_graceful_shutdown(async move { cancellation.cancelled().await })
        .await
        .context("the Factory control plane failed")?;
    health_task.abort();
    Ok(())
}

/// Periodically aggregate the repository-health snapshot and append a
/// repo.health event when it changes (or at least every interval), so the
/// overview card reflects live ledger state without spamming identical events.
async fn emit_repo_health(
    database: Arc<PathBuf>,
    repository: String,
    cancellation: CancellationToken,
) {
    let mut interval = tokio::time::interval(repo_health_interval());
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let mut last_fingerprint: Option<String> = None;
    loop {
        tokio::select! {
            () = cancellation.cancelled() => return,
            _ = interval.tick() => {}
        }
        let result = tokio::task::block_in_place(|| -> Result<(bool, String)> {
            let mut ledger = Ledger::open(&database)?;
            let view = ledger.repo_health_view(&repository)?;
            let fingerprint = serde_json::to_string(&view)?;
            let changed = last_fingerprint.as_deref() != Some(fingerprint.as_str());
            if changed {
                ledger.record_repo_health_event(&repository)?;
            }
            Ok((changed, fingerprint))
        });
        match result {
            Ok((_, fingerprint)) => last_fingerprint = Some(fingerprint),
            Err(error) => eprintln!("Factory failed to emit a repo.health event: {error:#}"),
        }
    }
}

fn build_router(state: AppState) -> Router {
    // Public surface: anonymous health only.
    let public = Router::new().route("/api/v1/health", get(health_handler));

    // Protected surface: every other route requires the bearer token. `/events`
    // additionally accepts `?token=` because EventSource cannot set headers.
    let protected = Router::new()
        .route("/events", get(events_handler))
        .route("/api/v1/tasks", get(tasks_handler))
        .route("/api/v1/tasks/{id}", get(task_handler))
        .route("/api/v1/runs", get(runs_handler))
        .route("/api/v1/runs/{id}", get(run_handler))
        .route("/api/v1/runs/{id}/cancel", post(cancel_run_handler))
        .route("/api/v1/onboard", post(onboard_handler))
        .route("/api/v1/status", get(status_handler))
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

/// The error half of a control-plane handler result. `Response` is large, so
/// clippy's large-Err lint fires; that is the idiomatic axum handler shape, so
/// the lint is allowed at the handler sites rather than distorting the API.
type HandlerResult<T> = Result<T, Response>;

/// Open the ledger for a control-plane query, mapping a failure to the shared
/// error envelope.
#[allow(clippy::result_large_err)]
fn open_ledger(state: &AppState) -> Result<Ledger, Response> {
    Ledger::open(&state.database).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "ledger_unavailable",
            &format!("failed to open the ledger: {error}"),
        )
    })
}

#[derive(Debug, Deserialize)]
struct TasksQuery {
    state: Option<String>,
}

#[allow(clippy::result_large_err)]
async fn tasks_handler(
    State(state): State<AppState>,
    Query(query): Query<TasksQuery>,
) -> HandlerResult<Json<Vec<TaskView>>> {
    let state_filter = match query.state.as_deref() {
        Some(value) => Some(value.parse::<TaskState>().map_err(|_| {
            error_response(
                StatusCode::BAD_REQUEST,
                "invalid_state",
                &format!("unknown task state {value:?}"),
            )
        })?),
        None => None,
    };
    let ledger = open_ledger(&state)?;
    let tasks = ledger.tasks().map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to list tasks: {error}"),
        )
    })?;
    let views = tasks
        .iter()
        .filter(|task| state_filter.is_none_or(|state| task.state == state))
        .map(TaskView::from)
        .collect();
    Ok(Json(views))
}

#[allow(clippy::result_large_err)]
async fn task_handler(
    State(state): State<AppState>,
    AxumPath(id): AxumPath<i64>,
) -> HandlerResult<Json<TaskView>> {
    let ledger = open_ledger(&state)?;
    let task = ledger.task(id).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to load task: {error}"),
        )
    })?;
    match task {
        Some(task) => Ok(Json(TaskView::from(&task))),
        None => Err(error_response(
            StatusCode::NOT_FOUND,
            "not_found",
            &format!("task {id} does not exist"),
        )),
    }
}

#[derive(Debug, Deserialize)]
struct RunsQuery {
    task_id: Option<i64>,
}

#[allow(clippy::result_large_err)]
async fn runs_handler(
    State(state): State<AppState>,
    Query(query): Query<RunsQuery>,
) -> HandlerResult<Json<Vec<RunView>>> {
    let ledger = open_ledger(&state)?;
    let runs = match query.task_id {
        Some(task_id) => ledger.runs_for_task(task_id),
        None => ledger.runs(None),
    }
    .map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to list runs: {error}"),
        )
    })?;
    Ok(Json(runs.iter().map(RunView::from).collect()))
}

#[derive(Debug, Serialize)]
struct RunDetail {
    #[serde(flatten)]
    run: RunView,
    container: Option<ContainerView>,
    sandbox: Option<SandboxView>,
}

/// Serializable container metadata for a run detail (RunContainer holds a
/// PathBuf and does not implement Serialize).
#[derive(Debug, Serialize)]
struct ContainerView {
    container_id: String,
    instance_id: String,
    image_ref: String,
    state: String,
    exit_code: Option<i32>,
    created_at: i64,
    removed_at: Option<i64>,
}

impl From<&RunContainer> for ContainerView {
    fn from(container: &RunContainer) -> Self {
        Self {
            container_id: container.container_id.clone(),
            instance_id: container.instance_id.clone(),
            image_ref: container.image_ref.clone(),
            state: container.state.clone(),
            exit_code: container.exit_code,
            created_at: container.created_at,
            removed_at: container.removed_at,
        }
    }
}

#[derive(Debug, Serialize)]
struct SandboxView {
    sandbox_name: String,
    instance_id: String,
    template_ref: String,
    state: String,
    exit_code: Option<i32>,
    created_at: i64,
    removed_at: Option<i64>,
}

impl From<&RunSandbox> for SandboxView {
    fn from(sandbox: &RunSandbox) -> Self {
        Self {
            sandbox_name: sandbox.sandbox_name.clone(),
            instance_id: sandbox.instance_id.clone(),
            template_ref: sandbox.template_ref.clone(),
            state: sandbox.state.clone(),
            exit_code: sandbox.exit_code,
            created_at: sandbox.created_at,
            removed_at: sandbox.removed_at,
        }
    }
}

#[allow(clippy::result_large_err)]
async fn run_handler(
    State(state): State<AppState>,
    AxumPath(id): AxumPath<i64>,
) -> HandlerResult<Json<RunDetail>> {
    let ledger = open_ledger(&state)?;
    let run = ledger.run(id).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to load run: {error}"),
        )
    })?;
    let Some(run) = run else {
        return Err(error_response(
            StatusCode::NOT_FOUND,
            "not_found",
            &format!("run {id} does not exist"),
        ));
    };
    let container = ledger.run_container(id).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to load the run container: {error}"),
        )
    })?;
    let sandbox = ledger.run_sandbox(id).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to load the run sandbox: {error}"),
        )
    })?;
    Ok(Json(RunDetail {
        run: RunView::from(&run),
        container: container.as_ref().map(ContainerView::from),
        sandbox: sandbox.as_ref().map(SandboxView::from),
    }))
}

/// The repository overview snapshot (`GET /api/v1/status`): the same shape as
/// a `repo.health` event payload, so the ui can initialise a card then follow
/// the event stream for deltas.
#[allow(clippy::result_large_err)]
async fn status_handler(State(state): State<AppState>) -> HandlerResult<Json<serde_json::Value>> {
    let ledger = open_ledger(&state)?;
    let repository = crate::config::repository_remote_identity(&state.config.repositories[0])
        .unwrap_or_else(|_| "unknown".to_owned());
    let view = ledger.repo_health_view(&repository).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "query_failed",
            &format!("failed to aggregate repository status: {error}"),
        )
    })?;
    Ok(Json(serde_json::to_value(view).unwrap_or_else(|_| {
        serde_json::json!({"status": "error"})
    })))
}

/// The body every write endpoint accepts: an idempotency key the ui supplies
/// so a retried submission does not repeat the side effect.
#[derive(Debug, Deserialize)]
struct WriteRequest {
    client_request_id: String,
}

/// Resolve a write's idempotency: if this `client_request_id` was already
/// processed, return the stored response instead of re-running the operation.
#[allow(clippy::result_large_err)]
fn idempotent_replay(
    ledger: &Ledger,
    client_request_id: &str,
) -> Result<Option<Response>, Response> {
    if client_request_id.trim().is_empty() {
        return Err(error_response(
            StatusCode::BAD_REQUEST,
            "invalid_request",
            "client_request_id must not be empty",
        ));
    }
    ledger
        .control_request(client_request_id)
        .map_err(|error| {
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "query_failed",
                &format!("failed to read the idempotency record: {error}"),
            )
        })
        .map(|stored| {
            stored.map(|response| {
                (
                    StatusCode::OK,
                    [(header::CONTENT_TYPE, "application/json")],
                    response,
                )
                    .into_response()
            })
        })
}

#[allow(clippy::result_large_err)]
async fn cancel_run_handler(
    State(state): State<AppState>,
    AxumPath(id): AxumPath<i64>,
    Json(body): Json<WriteRequest>,
) -> HandlerResult<Response> {
    let mut ledger = open_ledger(&state)?;
    if let Some(replay) = idempotent_replay(&ledger, &body.client_request_id)? {
        return Ok(replay);
    }

    let request = ledger.request_run_cancellation(id).map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "cancel_failed",
            &format!("failed to request cancellation: {error}"),
        )
    })?;
    let (status, outcome, run) = match &request {
        CancellationRequest::Requested(run) => (StatusCode::OK, "cancellation_requested", Some(run)),
        CancellationRequest::AlreadyRequested(run) => {
            (StatusCode::OK, "cancellation_requested", Some(run))
        }
        CancellationRequest::Terminal(run) => (StatusCode::OK, "terminal", Some(run)),
        CancellationRequest::OwnedElsewhere(run) => (StatusCode::OK, "terminal", Some(run)),
        CancellationRequest::NotFound => (StatusCode::NOT_FOUND, "not_found", None),
    };
    let Some(run) = run else {
        return Err(error_response(
            StatusCode::NOT_FOUND,
            "not_found",
            &format!("run {id} does not exist"),
        ));
    };
    let body_json = serde_json::json!({
        "client_request_id": body.client_request_id,
        "status": if status == StatusCode::OK { "ok" } else { "error" },
        "outcome": outcome,
        "run": RunView::from(run),
    });
    let body_text = body_json.to_string();
    ledger
        .record_control_request(&body.client_request_id, "cancel", "completed", &body_text)
        .map_err(|error| {
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "cancel_failed",
                &format!("failed to record the idempotency record: {error}"),
            )
        })?;
    Ok((
        StatusCode::OK,
        [(header::CONTENT_TYPE, "application/json")],
        body_text,
    )
        .into_response())
}

#[allow(clippy::result_large_err)]
async fn onboard_handler(
    State(state): State<AppState>,
    Json(body): Json<WriteRequest>,
) -> HandlerResult<Response> {
    let mut ledger = open_ledger(&state)?;
    if let Some(replay) = idempotent_replay(&ledger, &body.client_request_id)? {
        return Ok(replay);
    }

    // Trigger one source scheduling evaluation (the source half of
    // `factory run --once`), not waiting for the next poll cycle.
    let report = run_source_evaluation(&state, &mut ledger).await.map_err(|error| {
        error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "onboard_failed",
            &format!("failed to evaluate the source: {error}"),
        )
    })?;
    let body_json = serde_json::json!({
        "client_request_id": body.client_request_id,
        "status": "ok",
        "tasks_created": report.tasks_created,
        "issues_seen": report.issues_seen,
    });
    let body_text = body_json.to_string();
    ledger
        .record_control_request(&body.client_request_id, "onboard", "completed", &body_text)
        .map_err(|error| {
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "onboard_failed",
                &format!("failed to record the idempotency record: {error}"),
            )
        })?;
    Ok((
        StatusCode::OK,
        [(header::CONTENT_TYPE, "application/json")],
        body_text,
    )
        .into_response())
}

/// A minimal source-evaluation outcome for the onboard endpoint.
#[derive(Debug)]
struct SourceEvaluation {
    tasks_created: usize,
    issues_seen: usize,
}

/// Run one source scheduling evaluation, equivalent to the source half of
/// `factory run --once`. Without a configured source this is a no-op.
async fn run_source_evaluation(
    state: &AppState,
    ledger: &mut Ledger,
) -> Result<SourceEvaluation> {
    if state.config.source.is_none() {
        return Ok(SourceEvaluation {
            tasks_created: 0,
            issues_seen: 0,
        });
    }
    let catalog = crate::workflow::WorkflowCatalog::load(&state.config)?;
    let client = crate::source::SourceClient;
    let cancellation = CancellationToken::new();
    let report = client
        .poll_once(&state.config, &catalog, ledger, cancellation)
        .await?;
    let (tasks_created, issues_seen) = report
        .repositories
        .iter()
        .fold((0usize, 0usize), |(tasks, issues), repo| {
            (tasks + repo.tasks_created, issues + repo.issues_seen)
        });
    Ok(SourceEvaluation {
        tasks_created,
        issues_seen,
    })
}

#[derive(Debug, Deserialize)]
struct EventsQuery {
    /// Backfill cursor (alternative to the Last-Event-ID header).
    last_id: Option<i64>,
    /// Envelope version the ui speaks, for the capability handshake.
    v: Option<u32>,
}

/// The SSE stream. Order of operations converges the replay/subscribe race:
/// subscribe to committed events first, snapshot the backfill cursor, then
/// drain forward — the watermark only increases, and `> last_sent` dedupes.
async fn events_handler(
    State(state): State<AppState>,
    headers: header::HeaderMap,
    Query(query): Query<EventsQuery>,
) -> Response {
    let ledger = match Ledger::open(&state.database) {
        Ok(ledger) => ledger,
        Err(error) => {
            return error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "ledger_unavailable",
                &format!("failed to open the ledger: {error}"),
            );
        }
    };
    let notifier = ledger.subscribe_events();

    // Resolve the backfill cursor: explicit query param wins, then the header.
    let header_cursor = headers
        .get("last-event-id")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.trim().parse::<i64>().ok());
    let cursor = query.last_id.or(header_cursor).unwrap_or(0);

    // Capability handshake: a ui speaking an older envelope than the core has
    // already raised is told to upgrade with an explicit event up front, not
    // silently fed an unparseable structure.
    let unsupported = match query.v {
        Some(v) if v < ENVELOPE_VERSION => Some((v, ENVELOPE_VERSION)),
        _ => None,
    };

    let stream = build_event_stream(ledger, notifier, cursor, unsupported);
    let mut response = Sse::new(stream)
        .keep_alive(KeepAlive::new().interval(HEARTBEAT_INTERVAL).text("keep-alive"))
        .into_response();
    response.headers_mut().insert(
        header::HeaderName::from_static("x-accel-buffering"),
        header::HeaderValue::from_static("no"),
    );
    response
}

/// The per-connection stream state: the ledger handle (its own connection),
/// the committed-event notifier, and the next batch of rendered events to
/// emit before blocking on the watermark again.
struct EventStreamState {
    ledger: Ledger,
    notifier: watch::Receiver<i64>,
    last_sent: i64,
    pending: std::collections::VecDeque<Event>,
}

/// Build the live + backfill stream. The single unfolding state owns the
/// mutable cursor, so `last_sent` advances monotonically and the
/// `> last_sent` filter dedupes the replay/subscribe overlap.
fn build_event_stream(
    ledger: Ledger,
    notifier: watch::Receiver<i64>,
    cursor: i64,
    unsupported: Option<(u32, u32)>,
) -> impl tokio_stream::Stream<Item = Result<Event, Infallible>> {
    let mut state = EventStreamState {
        ledger,
        notifier,
        last_sent: cursor,
        pending: unsupported
            .map(|(client, server)| handshake_event(client, server))
            .into_iter()
            .collect(),
    };
    // Poll the durable watermark as a fallback: events committed by *other*
    // Ledger connections (the daemon loop holds its own) don't fire this
    // connection's watch, so a short poll bounds the cross-connection latency.
    let mut poll = tokio::time::interval(POLL_INTERVAL);
    poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    async_stream::stream! {
        loop {
            if let Some(event) = state.pending.pop_front() {
                yield Ok(event);
                continue;
            }
            // Drain the durable gap after the cursor (initial backfill, then
            // every committed advance), then block until the watermark moves.
            for event in state.ledger.events_after(state.last_sent).unwrap_or_default() {
                if event.event_id > state.last_sent {
                    state.last_sent = event.event_id;
                    state.pending.push_back(sse_event(&event));
                }
            }
            if let Some(event) = state.pending.pop_front() {
                yield Ok(event);
                continue;
            }
            // Wake on a same-connection commit (immediate) or the poll tick
            // (cross-connection), whichever comes first.
            tokio::select! {
                changed = state.notifier.changed() => {
                    if changed.is_err() {
                        // The sender side is gone (ledger dropped); end the stream.
                        break;
                    }
                }
                _ = poll.tick() => {}
            }
        }
    }
}

/// Wrap a ledger event in the schema/events/envelope.json envelope and render
/// it as one SSE frame (`id:` = global cursor, `event:` = type, `retry:`).
fn sse_event(event: &LedgerEvent) -> Event {
    let envelope = serde_json::json!({
        "v": ENVELOPE_VERSION,
        "type": event.event_type,
        "seq": event.event_id,
        "ts": event.ts,
        "repository": event.repository,
        "task_id": event.task_id,
        "run_id": event.run_id,
        "payload": event.payload,
    });
    let data = serde_json::to_string(&envelope).unwrap_or_else(|_| "{}".to_owned());
    Event::default()
        .id(event.event_id.to_string())
        .event(&event.event_type)
        .retry(SSE_RETRY)
        .data(data)
}

fn handshake_event(client: u32, server: u32) -> Event {
    let payload = serde_json::json!({
        "v": server,
        "type": "unsupported",
        "seq": 0,
        "ts": chrono::Utc::now().to_rfc3339(),
        "repository": "",
        "task_id": null,
        "run_id": null,
        "payload": {
            "client_v": client,
            "server_v": server,
            "message": "envelope version unsupported; upgrade the ui",
        },
    });
    Event::default()
        .event("unsupported")
        .retry(SSE_RETRY)
        .data(serde_json::to_string(&payload).unwrap_or_else(|_| "{}".to_owned()))
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
