#![cfg(unix)]

//! Integration tests for the SSE `/events` surface: live fanout, Last-Event-ID
//! backfill across a serve restart, the `?v=` handshake, and heartbeat frames.

use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command as ProcessCommand, Stdio};
use std::time::{Duration, Instant};

use factory::storage::{EventType, Ledger};

const TOKEN: &str = "w2-events-token";

struct Fixture {
    _temp: tempfile::TempDir,
    home: PathBuf,
    data_home: PathBuf,
    repository: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let home = temp.path().join("home");
        let repository = temp.path().join("repository");
        let data_home = temp.path().join("factory-data");
        fs::create_dir(&home).unwrap();
        init_git_repository(&repository, "git@github.com:example/repository.git");
        let mut command = assert_cmd::Command::cargo_bin("factory").unwrap();
        command
            .current_dir(&repository)
            .env("HOME", &home)
            .env("FACTORY_DATA_HOME", &data_home)
            .arg("init")
            .assert()
            .success();
        Self {
            _temp: temp,
            home,
            data_home,
            repository,
        }
    }

    fn spawn_serve(&self, port: u16) -> Child {
        let binary = assert_cmd::cargo::cargo_bin("factory");
        ProcessCommand::new(binary)
            .current_dir(&self.repository)
            .env("HOME", &self.home)
            .env("FACTORY_DATA_HOME", &self.data_home)
            .env("FACTORY_PORT", port.to_string())
            .env("FACTORY_API_TOKEN", TOKEN)
            // Fast repo.health cadence so tests observe the periodic emission.
            .env("FACTORY_REPO_HEALTH_INTERVAL_MS", "200")
            .arg("serve")
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap()
    }

    /// Open the ledger the spawned serve process serves, so tests can append
    /// committed events that the SSE stream must fan out.
    fn ledger(&self) -> Ledger {
        // SAFETY: each test drives a single serve child and opens the ledger
        // once; tests in this binary that touch the ledger do not run
        // concurrently on the same data home.
        unsafe { std::env::set_var("FACTORY_DATA_HOME", &self.data_home) };
        let data_directory =
            factory::config::repository_data_directory(&self.repository).unwrap();
        Ledger::open(&data_directory.join(factory::storage::DATABASE_NAME)).unwrap()
    }
}

fn init_git_repository(path: &Path, origin: &str) {
    fs::create_dir_all(path).unwrap();
    assert!(
        ProcessCommand::new("git")
            .args(["init", "--quiet"])
            .current_dir(path)
            .status()
            .unwrap()
            .success()
    );
    assert!(
        ProcessCommand::new("git")
            .args(["remote", "add", "origin", origin])
            .current_dir(path)
            .status()
            .unwrap()
            .success()
    );
}

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

fn wait_for_serve(child: &mut Child, port: u16) {
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        if let Some(status) = child.try_wait().unwrap() {
            panic!("factory serve exited early with {status}");
        }
        if std::net::TcpStream::connect(("127.0.0.1", port)).is_ok() {
            return;
        }
        assert!(Instant::now() < deadline, "factory serve did not come up");
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// One parsed SSE frame.
#[derive(Debug, Default)]
struct SseFrame {
    id: Option<String>,
    event: Option<String>,
    data: String,
    retry: Option<String>,
    comment: Option<String>,
}

/// Read SSE frames from a blocking HTTP connection until `count` data frames
/// (or `include_comments` heartbeat comments) arrive or the deadline passes.
fn read_frames(
    stream: &mut impl std::io::Read,
    count: usize,
    deadline: Duration,
) -> Vec<SseFrame> {
    let start = Instant::now();
    let mut buffer = Vec::new();
    let mut byte = [0u8; 1];
    let mut frames = Vec::new();
    let mut current = SseFrame::default();
    let mut data_frames = 0;
    while data_frames < count && start.elapsed() < deadline {
        match stream.read(&mut byte) {
            Ok(0) => break,
            Ok(_) => buffer.push(byte[0]),
            Err(error)
                if error.kind() == std::io::ErrorKind::WouldBlock
                    || error.kind() == std::io::ErrorKind::TimedOut =>
            {
                std::thread::sleep(Duration::from_millis(10));
                continue;
            }
            Err(error) => panic!("SSE read failed: {error}"),
        }
        // Process complete lines.
        while let Some(position) = buffer.iter().position(|b| *b == b'\n') {
            let line: Vec<u8> = buffer.drain(..=position).collect();
            let line = String::from_utf8_lossy(&line);
            let line = line.trim_end_matches(['\r', '\n']);
            if line.is_empty() {
                // Blank line: frame boundary.
                if current.data.is_empty() && current.comment.is_none() {
                    continue;
                }
                if current.comment.is_some() {
                    frames.push(std::mem::take(&mut current));
                } else {
                    data_frames += 1;
                    frames.push(std::mem::take(&mut current));
                }
                if data_frames >= count {
                    return frames;
                }
                continue;
            }
            if let Some(comment) = line.strip_prefix(':') {
                current.comment = Some(comment.trim().to_owned());
            } else if let Some(value) = line.strip_prefix("id:") {
                current.id = Some(value.trim().to_owned());
            } else if let Some(value) = line.strip_prefix("event:") {
                current.event = Some(value.trim().to_owned());
            } else if let Some(value) = line.strip_prefix("retry:") {
                current.retry = Some(value.trim().to_owned());
            } else if let Some(value) = line.strip_prefix("data:") {
                if !current.data.is_empty() {
                    current.data.push('\n');
                }
                current.data.push_str(value.trim_start_matches(' '));
            }
        }
    }
    frames
}

/// Open a streaming SSE connection, skipping the HTTP headers.
fn open_sse(port: u16, path: &str) -> std::net::TcpStream {
    use std::io::Write;
    let mut stream = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_millis(200)))
        .unwrap();
    write!(
        stream,
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer {TOKEN}\r\nAccept: text/event-stream\r\n\r\n"
    )
    .unwrap();
    stream.flush().unwrap();
    // Consume headers up to the blank line.
    let mut head = Vec::new();
    let mut byte = [0u8; 1];
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        assert!(Instant::now() < deadline, "no SSE response headers");
        if stream.read(&mut byte).unwrap_or(0) == 0 {
            panic!("connection closed before headers");
        }
        head.push(byte[0]);
        if head.ends_with(b"\r\n\r\n") {
            break;
        }
    }
    let head = String::from_utf8_lossy(&head);
    assert!(
        head.contains("text/event-stream"),
        "expected an SSE content type, got: {head}"
    );
    assert!(
        head.to_lowercase().contains("x-accel-buffering: no"),
        "expected X-Accel-Buffering: no, got: {head}"
    );
    stream
}

#[test]
fn events_stream_fans_out_live_committed_events() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);

    let mut stream = open_sse(port, "/events");

    // Append a committed event from a separate connection; the stream must
    // fan it out (via the cross-connection poll).
    let mut ledger = fixture.ledger();
    // Use a task.state event so the test is unambiguous: the periodic
    // repo.health emitter also produces frames on the stream now.
    ledger
        .append_event(
            EventType::TaskState,
            "example/repository",
            Some(1),
            None,
            serde_json::json!({"from": null, "to": "queued", "workflow": "wf", "ticket": {"id": "1"}}),
        )
        .unwrap();

    // Read until our task.state frame arrives (a periodic repo.health may
    // arrive first); assert our event fanned out with the right envelope.
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut found: Option<SseFrame> = None;
    while Instant::now() < deadline && found.is_none() {
        for frame in read_frames(&mut stream, 1, Duration::from_secs(2)) {
            if frame.event.as_deref() == Some("task.state") {
                found = Some(frame);
                break;
            }
        }
    }
    let frame = found.expect("expected a task.state frame");
    assert_eq!(frame.retry.as_deref(), Some("3000"));
    let id: i64 = frame.id.as_ref().unwrap().parse().unwrap();
    assert!(id >= 1);
    let envelope: serde_json::Value = serde_json::from_str(&frame.data).unwrap();
    assert_eq!(envelope["v"], 1);
    assert_eq!(envelope["type"], "task.state");
    assert_eq!(envelope["seq"], id);
    assert_eq!(envelope["repository"], "example/repository");
    assert_eq!(envelope["payload"]["to"], "queued");
    assert_eq!(envelope["task_id"], 1);
    assert!(envelope["run_id"].is_null());

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn events_backfills_after_last_event_id_across_a_restart() {
    let fixture = Fixture::new();
    let mut ledger = fixture.ledger();
    let first = ledger
        .append_event(
            EventType::TaskState,
            "example/repository",
            Some(1),
            None,
            serde_json::json!({"from": null, "to": "queued", "workflow": "wf", "ticket": {"id": "1"}}),
        )
        .unwrap();
    let second = ledger
        .append_event(
            EventType::TaskState,
            "example/repository",
            Some(1),
            Some(1),
            serde_json::json!({"from": "queued", "to": "running", "workflow": "wf", "ticket": {"id": "1"}}),
        )
        .unwrap();
    drop(ledger);

    // First serve instance: connect with Last-Event-ID = first, get only the gap.
    // (The periodic repo.health emitter may interleave health frames, so
    // filter to the task.state events we appended.)
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);
    let mut stream = open_sse(port, &format!("/events?last_id={}", first.event_id));
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut task_states = Vec::new();
    while Instant::now() < deadline && task_states.is_empty() {
        for frame in read_frames(&mut stream, 1, Duration::from_secs(2)) {
            if frame.event.as_deref() == Some("task.state") {
                task_states.push(frame.id.as_ref().unwrap().parse::<i64>().unwrap());
            }
        }
    }
    assert_eq!(task_states, vec![second.event_id]);
    drop(stream);
    child.kill().unwrap();
    child.wait().unwrap();

    // Restart serve: the durable cursor still backfills the gap (restart persistence).
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);
    let mut stream = open_sse(port, "/events");
    // No cursor: both committed task.state events backfill in order, alongside
    // any periodic repo.health frames.
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut task_states = Vec::new();
    while Instant::now() < deadline && task_states.len() < 2 {
        for frame in read_frames(&mut stream, 1, Duration::from_secs(2)) {
            if frame.event.as_deref() == Some("task.state") {
                task_states.push(frame.id.as_ref().unwrap().parse::<i64>().unwrap());
            }
        }
    }
    assert_eq!(task_states, vec![first.event_id, second.event_id]);
    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn events_sends_an_unsupported_handshake_for_an_older_version() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);

    let mut stream = open_sse(port, "/events?v=0");
    let frames = read_frames(&mut stream, 1, Duration::from_secs(10));
    assert!(!frames.is_empty(), "expected a handshake event");
    assert_eq!(frames[0].event.as_deref(), Some("unsupported"));
    let payload: serde_json::Value = serde_json::from_str(&frames[0].data).unwrap();
    assert_eq!(payload["type"], "unsupported");
    assert_eq!(payload["payload"]["server_v"], 1);

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn events_heartbeat_keeps_an_idle_connection_alive() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);

    // With no events, the stream should emit a `:keep-alive` comment within
    // the heartbeat interval (15s). Allow generous slack.
    let mut stream = open_sse(port, "/events");
    let start = Instant::now();
    let mut saw_heartbeat = false;
    let mut buffer = Vec::new();
    let mut byte = [0u8; 1];
    while start.elapsed() < Duration::from_secs(25) {
        match stream.read(&mut byte) {
            Ok(0) => break,
            Ok(_) => {
                buffer.push(byte[0]);
                if buffer.ends_with(b"\n") {
                    let line = String::from_utf8_lossy(&buffer);
                    if line.trim().starts_with(':') {
                        saw_heartbeat = true;
                        break;
                    }
                    buffer.clear();
                }
            }
            Err(_) => std::thread::sleep(Duration::from_millis(20)),
        }
    }
    assert!(saw_heartbeat, "expected a :keep-alive comment");

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn status_endpoint_returns_the_current_repo_health_snapshot() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);

    let response = http_get_json(port, "/api/v1/status");
    assert_eq!(response["status"], "idle");
    assert_eq!(response["active_runs"], 0);
    assert_eq!(response["queued_tasks"], 0);
    assert!(response.get("backoff_until").is_some());

    // Anonymous access is rejected.
    let status = http_status_anonymous(port, "/api/v1/status");
    assert_eq!(status, 401);

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn repo_health_event_is_emitted_on_the_sse_stream() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);

    // The periodic emitter fires the first repo.health snapshot shortly after
    // startup; the SSE stream fans it out.
    let mut stream = open_sse(port, "/events");
    let frames = read_frames(&mut stream, 1, Duration::from_secs(10));
    assert!(!frames.is_empty(), "expected a repo.health event");
    let health = frames
        .iter()
        .find(|frame| frame.event.as_deref() == Some("repo.health"))
        .expect("expected a repo.health frame");
    let envelope: serde_json::Value = serde_json::from_str(&health.data).unwrap();
    assert_eq!(envelope["type"], "repo.health");
    assert_eq!(envelope["payload"]["status"], "idle");
    assert!(envelope["task_id"].is_null());
    assert!(envelope["run_id"].is_null());

    child.kill().unwrap();
    child.wait().unwrap();
}

/// GET a JSON control-plane endpoint with the bearer token.
fn http_get_json(port: u16, path: &str) -> serde_json::Value {
    let mut stream = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    write!(
        stream,
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer {TOKEN}\r\nConnection: close\r\n\r\n"
    )
    .unwrap();
    stream.flush().unwrap();
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).unwrap();
    let raw = String::from_utf8(raw).unwrap();
    let body = raw.split_once("\r\n\r\n").unwrap().1;
    serde_json::from_str(body).unwrap_or_else(|error| panic!("not JSON ({error}): {body}"))
}

/// GET an endpoint without a token and return only the status code.
fn http_status_anonymous(port: u16, path: &str) -> u16 {
    let mut stream = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    write!(
        stream,
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
    )
    .unwrap();
    stream.flush().unwrap();
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).unwrap();
    let raw = String::from_utf8(raw).unwrap();
    raw.split_whitespace().nth(1).unwrap().parse().unwrap()
}

#[test]
fn invalid_event_is_dropped_but_the_stream_continues() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);

    // Insert a malformed task.state payload directly (bypassing the sanitized
    // append path), then a valid one. The malformed one must be dropped; the
    // valid one must still arrive.
    // SAFETY: same rationale as Fixture::ledger; this test owns its data home.
    unsafe { std::env::set_var("FACTORY_DATA_HOME", &fixture.data_home) };
    let data_directory = factory::config::repository_data_directory(&fixture.repository).unwrap();
    let connection = rusqlite::Connection::open(
        data_directory.join(factory::storage::DATABASE_NAME),
    )
    .unwrap();
    connection
        .execute(
            "INSERT INTO events (type, ts, repository, task_id, run_id, payload)
             VALUES ('task.state', '2026-07-31T00:00:00Z', 'example/repository', 1, NULL,
                     '{\"from\": null}')",
            [],
        )
        .unwrap();
    drop(connection);

    let mut ledger = fixture.ledger();
    ledger
        .append_event(
            EventType::TaskState,
            "example/repository",
            Some(1),
            None,
            serde_json::json!({"from": null, "to": "queued", "workflow": "wf", "ticket": {"id": "1"}}),
        )
        .unwrap();

    // Read task.state frames: only the valid one should appear (the malformed
    // one is dropped). The periodic repo.health frames are ignored.
    let mut stream = open_sse(port, "/events");
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut task_state_seqs = Vec::new();
    while Instant::now() < deadline && task_state_seqs.is_empty() {
        for frame in read_frames(&mut stream, 1, Duration::from_secs(2)) {
            if frame.event.as_deref() == Some("task.state") {
                let envelope: serde_json::Value = serde_json::from_str(&frame.data).unwrap();
                task_state_seqs.push(envelope["seq"].as_i64().unwrap());
            }
        }
    }
    // Exactly one task.state (the valid one) survived; the malformed one was
    // dropped without breaking the stream.
    assert_eq!(task_state_seqs.len(), 1, "got {task_state_seqs:?}");

    child.kill().unwrap();
    child.wait().unwrap();
}
