#![cfg(unix)]

use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::process::{Child, Command as ProcessCommand, Stdio};
use std::time::{Duration, Instant};

const TOKEN: &str = "w2-test-token";

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

    /// Open the ledger the spawned serve process serves, to seed tasks/runs.
    fn ledger(&self) -> factory::storage::Ledger {
        // SAFETY: each test drives a single serve child; ledger access here is
        // not concurrent with other tests on the same data home.
        unsafe { std::env::set_var("FACTORY_DATA_HOME", &self.data_home) };
        let data_directory =
            factory::config::repository_data_directory(&self.repository).unwrap();
        factory::storage::Ledger::open(
            &data_directory.join(factory::storage::DATABASE_NAME),
        )
        .unwrap()
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

#[derive(Debug)]
struct HttpResponse {
    status: u16,
    headers: Vec<(String, String)>,
    body: String,
}

impl HttpResponse {
    fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }

    fn json(&self) -> serde_json::Value {
        serde_json::from_str(&self.body)
            .unwrap_or_else(|error| panic!("response body is not JSON ({error}): {}", self.body))
    }
}

/// Send one raw HTTP request and read the full response (connection-close).
fn http_request(port: u16, method: &str, path: &str, headers: &[(&str, &str)]) -> HttpResponse {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let mut request = format!("{method} {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n");
    for (name, value) in headers {
        request.push_str(&format!("{name}: {value}\r\n"));
    }
    request.push_str("\r\n");
    stream.write_all(request.as_bytes()).unwrap();
    stream.flush().unwrap();
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).unwrap();
    parse_response(&raw)
}

fn http_get(port: u16, path: &str) -> HttpResponse {
    http_request(port, "GET", path, &[])
}

fn http_get_token(port: u16, path: &str, token: &str) -> HttpResponse {
    http_request(port, "GET", path, &[("Authorization", &format!("Bearer {token}"))])
}

fn parse_response(raw: &[u8]) -> HttpResponse {
    let raw = String::from_utf8(raw.to_vec()).unwrap();
    let (head, body) = raw.split_once("\r\n\r\n").unwrap();
    let mut lines = head.lines();
    let status = lines
        .next()
        .unwrap()
        .split_whitespace()
        .nth(1)
        .unwrap()
        .parse::<u16>()
        .unwrap();
    let headers = lines
        .filter_map(|line| {
            let (name, value) = line.split_once(':')?;
            Some((name.trim().to_owned(), value.trim().to_owned()))
        })
        .collect();
    HttpResponse {
        status,
        headers,
        body: body.to_owned(),
    }
}

fn wait_for_health(child: &mut Child, port: u16) -> HttpResponse {
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        if let Some(status) = child.try_wait().unwrap() {
            let mut stderr = String::new();
            child
                .stderr
                .take()
                .unwrap()
                .read_to_string(&mut stderr)
                .unwrap();
            panic!("factory serve exited early with {status}: {stderr}");
        }
        if TcpStream::connect(("127.0.0.1", port)).is_ok() {
            let response = http_get(port, "/api/v1/health");
            if response.status == 200 {
                return response;
            }
        }
        assert!(Instant::now() < deadline, "factory serve did not come up");
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[test]
fn serve_reports_health_anonymously() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);

    let response = wait_for_health(&mut child, port);
    assert_eq!(response.status, 200);
    assert_eq!(response.header("content-type"), Some("application/json"));
    assert_eq!(response.header("cache-control"), Some("no-cache"));
    let body = response.json();
    assert_eq!(body["status"], "ok");
    assert_eq!(body["repository"], "example/repository");
    assert_eq!(body["version"], env!("CARGO_PKG_VERSION"));

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn serve_refuses_to_start_without_an_api_token() {
    let fixture = Fixture::new();
    let binary = assert_cmd::cargo::cargo_bin("factory");
    let output = ProcessCommand::new(binary)
        .current_dir(&fixture.repository)
        .env("HOME", &fixture.home)
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("FACTORY_PORT", free_port().to_string())
        .env_remove("FACTORY_API_TOKEN")
        .arg("serve")
        .output()
        .unwrap();
    assert!(
        !output.status.success(),
        "serve must fail without FACTORY_API_TOKEN: {output:?}"
    );
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(
        stderr.contains("FACTORY_API_TOKEN"),
        "error should name the missing token, got: {stderr}"
    );
}

#[test]
fn serve_rejects_protected_routes_without_a_token() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_health(&mut child, port);

    // No token at all.
    let response = http_get(port, "/api/v1/tasks");
    assert_eq!(response.status, 401);
    assert_eq!(response.header("cache-control"), Some("no-cache"));
    let body = response.json();
    assert_eq!(body["error"]["code"], "unauthorized");
    assert!(body["error"]["message"].is_string());

    // Wrong token.
    let response = http_get_token(port, "/api/v1/tasks", "not-the-token");
    assert_eq!(response.status, 401);
    assert_eq!(response.json()["error"]["code"], "unauthorized");

    // /events also rejects without a token (header or query).
    let response = http_get(port, "/events");
    assert_eq!(response.status, 401);

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn serve_accepts_bearer_header_and_query_token_on_events() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_health(&mut child, port);

    // A correct bearer token upgrades to an SSE stream (never a 401).
    assert_eq!(events_auth_status(port, "/events", Some(TOKEN)), 200);
    // The ?token= query also authenticates (EventSource cannot set headers).
    assert_eq!(events_auth_status(port, &format!("/events?token={TOKEN}"), None), 200);
    // No token and a wrong token are rejected.
    assert_eq!(events_auth_status(port, "/events", None), 401);
    assert_eq!(events_auth_status(port, "/events", Some("wrong")), 401);

    child.kill().unwrap();
    child.wait().unwrap();
}

/// Read only the status line of an /events response, without consuming the
/// (potentially never-ending) SSE body.
fn events_auth_status(port: u16, path: &str, token: Option<&str>) -> u16 {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let auth = token
        .map(|token| format!("Authorization: Bearer {token}\r\n"))
        .unwrap_or_default();
    write!(
        stream,
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\n{auth}Connection: close\r\n\r\n"
    )
    .unwrap();
    stream.flush().unwrap();
    let mut status_line = Vec::new();
    let mut byte = [0u8; 1];
    loop {
        if stream.read(&mut byte).unwrap_or(0) == 0 {
            break;
        }
        status_line.push(byte[0]);
        if byte[0] == b'\n' {
            break;
        }
    }
    String::from_utf8_lossy(&status_line)
        .split_whitespace()
        .nth(1)
        .unwrap()
        .parse::<u16>()
        .unwrap()
}

#[test]
fn serve_returns_404_for_unknown_paths() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_health(&mut child, port);

    let response = http_get_token(port, "/api/v1/does-not-exist", TOKEN);
    assert_eq!(response.status, 404);
    assert_eq!(response.json()["error"]["code"], "not_found");

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn serve_requires_initialized_repository() {
    let temp = tempfile::tempdir().unwrap();
    let home = temp.path().join("home");
    fs::create_dir(&home).unwrap();
    let mut command = assert_cmd::Command::cargo_bin("factory").unwrap();
    command
        .current_dir(temp.path())
        .env("HOME", &home)
        .env("FACTORY_DATA_HOME", temp.path().join("data"))
        .env("FACTORY_PORT", free_port().to_string())
        .env("FACTORY_API_TOKEN", TOKEN)
        .arg("serve")
        .assert()
        .failure();
}

/// Seed one queued task and one running run directly in the ledger.
fn seed_task_and_run(fixture: &Fixture) -> (i64, i64) {
    use factory::storage::TaskIdentity;
    let mut ledger = fixture.ledger();
    ledger
        .register_daemon_owner("owner", std::process::id())
        .unwrap();
    let identity = TaskIdentity::ticket(
        "example/repository",
        "implement-ready-ticket",
        "7",
        "rev-1",
    )
    .unwrap();
    ledger.enqueue(&identity).unwrap();
    let runtimes = std::collections::HashMap::from([(
        (
            "example/repository".to_owned(),
            "implement-ready-ticket".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let claimed = ledger
        .claim_and_start_run(
            &["example/repository".to_owned()],
            &runtimes,
            "owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    (claimed.task.id, claimed.run.id)
}

#[test]
fn tasks_query_filters_by_state_and_serves_detail() {
    let fixture = Fixture::new();
    let (task_id, _run_id) = seed_task_and_run(&fixture);
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_health(&mut child, port);

    // All tasks (one running).
    let response = http_get_token(port, "/api/v1/tasks", TOKEN);
    assert_eq!(response.status, 200);
    let tasks = response.json();
    assert_eq!(tasks.as_array().unwrap().len(), 1);
    assert_eq!(tasks[0]["id"], task_id);
    assert_eq!(tasks[0]["state"], "running");
    assert_eq!(tasks[0]["workflow"], "implement-ready-ticket");

    // ?state=running matches; ?state=queued is empty.
    let response = http_get_token(port, "/api/v1/tasks?state=running", TOKEN);
    assert_eq!(response.json().as_array().unwrap().len(), 1);
    let response = http_get_token(port, "/api/v1/tasks?state=queued", TOKEN);
    assert_eq!(response.json().as_array().unwrap().len(), 0);

    // Unknown state -> 400 with the error envelope.
    let response = http_get_token(port, "/api/v1/tasks?state=bogus", TOKEN);
    assert_eq!(response.status, 400);
    assert_eq!(response.json()["error"]["code"], "invalid_state");

    // Detail by id; missing id -> 404.
    let response = http_get_token(port, &format!("/api/v1/tasks/{task_id}"), TOKEN);
    assert_eq!(response.status, 200);
    assert_eq!(response.json()["id"], task_id);
    let response = http_get_token(port, "/api/v1/tasks/9999", TOKEN);
    assert_eq!(response.status, 404);
    assert_eq!(response.json()["error"]["code"], "not_found");

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn runs_query_filters_by_task_and_serves_detail() {
    let fixture = Fixture::new();
    let (task_id, run_id) = seed_task_and_run(&fixture);
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_health(&mut child, port);

    // All runs; filter by task_id.
    let response = http_get_token(port, "/api/v1/runs", TOKEN);
    assert_eq!(response.status, 200);
    assert_eq!(response.json().as_array().unwrap().len(), 1);
    let response = http_get_token(port, &format!("/api/v1/runs?task_id={task_id}"), TOKEN);
    assert_eq!(response.json().as_array().unwrap().len(), 1);
    let response = http_get_token(port, "/api/v1/runs?task_id=9999", TOKEN);
    assert_eq!(response.json().as_array().unwrap().len(), 0);

    // Detail includes outcome and container/sandbox keys.
    let response = http_get_token(port, &format!("/api/v1/runs/{run_id}"), TOKEN);
    assert_eq!(response.status, 200);
    let detail = response.json();
    assert_eq!(detail["id"], run_id);
    assert_eq!(detail["task_id"], task_id);
    assert_eq!(detail["outcome"], "running");
    assert!(detail.get("container").is_some());
    assert!(detail.get("sandbox").is_some());
    assert!(detail["container"].is_null());
    assert!(detail["sandbox"].is_null());

    let response = http_get_token(port, "/api/v1/runs/9999", TOKEN);
    assert_eq!(response.status, 404);

    // All query endpoints reject anonymous access.
    assert_eq!(http_get(port, "/api/v1/tasks").status, 401);
    assert_eq!(http_get(port, "/api/v1/runs").status, 401);
    assert_eq!(http_get(port, &format!("/api/v1/runs/{run_id}")).status, 401);

    child.kill().unwrap();
    child.wait().unwrap();
}
