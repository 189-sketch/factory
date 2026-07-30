#![cfg(unix)]

use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::process::{Child, Command as ProcessCommand, Stdio};
use std::time::{Duration, Instant};

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
            .arg("serve")
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .spawn()
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

struct HttpResponse {
    status: u16,
    content_type: String,
    body: String,
}

fn http_get(port: u16, path: &str) -> HttpResponse {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
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
    let (head, body) = raw.split_once("\r\n\r\n").unwrap();
    let status = head
        .split_whitespace()
        .nth(1)
        .unwrap()
        .parse::<u16>()
        .unwrap();
    let content_type = head
        .lines()
        .find_map(|line| line.strip_prefix("content-type: "))
        .unwrap_or("")
        .to_owned();
    HttpResponse {
        status,
        content_type,
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
        if let Ok(mut stream) = TcpStream::connect(("127.0.0.1", port)) {
            stream
                .set_read_timeout(Some(Duration::from_secs(2)))
                .unwrap();
            if write!(
                stream,
                "GET /api/v1/health HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
            )
            .is_ok()
            {
                let mut raw = Vec::new();
                if stream.read_to_end(&mut raw).is_ok() && !raw.is_empty() {
                    let raw = String::from_utf8(raw).unwrap();
                    let (head, body) = raw.split_once("\r\n\r\n").unwrap();
                    let status = head
                        .split_whitespace()
                        .nth(1)
                        .unwrap()
                        .parse::<u16>()
                        .unwrap();
                    let content_type = head
                        .lines()
                        .find_map(|line| line.strip_prefix("content-type: "))
                        .unwrap_or("")
                        .to_owned();
                    return HttpResponse {
                        status,
                        content_type,
                        body: body.to_owned(),
                    };
                }
            }
        }
        assert!(Instant::now() < deadline, "factory serve did not come up");
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[test]
fn serve_reports_health_with_repository_identity() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);

    let response = wait_for_health(&mut child, port);
    assert_eq!(response.status, 200);
    assert_eq!(response.content_type, "application/json");
    let body: serde_json::Value = serde_json::from_str(&response.body).unwrap();
    assert_eq!(body["status"], "ok");
    assert_eq!(body["repository"], "example/repository");
    assert_eq!(body["version"], env!("CARGO_PKG_VERSION"));

    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn serve_returns_404_for_unknown_paths() {
    let fixture = Fixture::new();
    let port = free_port();
    let mut child = fixture.spawn_serve(port);
    wait_for_health(&mut child, port);

    let response = http_get(port, "/api/v1/tasks");
    assert_eq!(response.status, 404);

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
        .arg("serve")
        .assert()
        .failure();
}
