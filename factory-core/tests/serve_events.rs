#![cfg(unix)]

//! Integration tests for the SSE `/events` surface: live fanout, Last-Event-ID
//! backfill across a serve restart, the `?v=` handshake, and heartbeat frames.

use std::fs;
use std::io::Read;
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
    ledger
        .append_event(
            EventType::RepoHealth,
            "example/repository",
            None,
            None,
            serde_json::json!({"status": "ready"}),
        )
        .unwrap();

    let frames = read_frames(&mut stream, 1, Duration::from_secs(10));
    assert_eq!(frames.len(), 1, "expected one live event, got {frames:?}");
    let frame = &frames[0];
    assert_eq!(frame.event.as_deref(), Some("repo.health"));
    assert_eq!(frame.retry.as_deref(), Some("3000"));
    let id: i64 = frame.id.as_ref().unwrap().parse().unwrap();
    assert!(id >= 1);
    let envelope: serde_json::Value = serde_json::from_str(&frame.data).unwrap();
    assert_eq!(envelope["v"], 1);
    assert_eq!(envelope["type"], "repo.health");
    assert_eq!(envelope["seq"], id);
    assert_eq!(envelope["repository"], "example/repository");
    assert_eq!(envelope["payload"]["status"], "ready");
    assert!(envelope["task_id"].is_null());
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
            serde_json::json!({"from": null, "to": "queued"}),
        )
        .unwrap();
    let second = ledger
        .append_event(
            EventType::TaskState,
            "example/repository",
            Some(1),
            Some(1),
            serde_json::json!({"from": "queued", "to": "running"}),
        )
        .unwrap();
    drop(ledger);

    // First serve instance: connect with Last-Event-ID = first, get only the gap.
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);
    let mut stream = open_sse(port, &format!("/events?last_id={}", first.event_id));
    let frames = read_frames(&mut stream, 1, Duration::from_secs(10));
    assert_eq!(frames.len(), 1);
    assert_eq!(frames[0].id.as_ref().unwrap().parse::<i64>().unwrap(), second.event_id);
    drop(stream);
    child.kill().unwrap();
    child.wait().unwrap();

    // Restart serve: the durable cursor still backfills the gap (restart persistence).
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_serve(&mut child, port);
    let mut stream = open_sse(port, "/events");
    // No cursor: both committed events backfill in order.
    let frames = read_frames(&mut stream, 2, Duration::from_secs(10));
    assert_eq!(frames.len(), 2);
    assert_eq!(frames[0].id.as_ref().unwrap().parse::<i64>().unwrap(), first.event_id);
    assert_eq!(frames[1].id.as_ref().unwrap().parse::<i64>().unwrap(), second.event_id);
    assert_eq!(frames[0].event.as_deref(), Some("task.state"));
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
