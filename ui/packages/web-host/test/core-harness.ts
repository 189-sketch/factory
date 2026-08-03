/**
 * Real-core test harness (W4.1, #39).
 *
 * Starts a genuine `factory serve` process (the W4.0 cross-platform binary) in
 * a throwaway git workspace, with a random port and injected FACTORY_API_TOKEN.
 * This replaces any hand-rolled fake core: aggregation tests drive the real
 * event/control-plane contract end to end.
 *
 * The binary is located by walking up from this package to the workspace-root
 * `target/<triple>/debug/factory[.exe]`. Set FACTORY_CORE_BIN to override.
 */

import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:net";
import { mkdtempSync, readdirSync, rmSync, statSync } from "node:fs";
import Database from "better-sqlite3";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));

/** One parsed SSE frame from the core `/events` stream. */
export interface SseFrame {
  /** Global cursor (`id:`). */
  id: number | null;
  /** Event type (`event:`). */
  event: string;
  /** Raw `data:` payload (JSON text). */
  data: string;
}

export interface CoreHandle {
  /** Bound host (always 127.0.0.1). */
  host: string;
  /** Published port the core is listening on. */
  port: number;
  /** Bearer token the core requires (also accepted as `?token=`). */
  token: string;
  /** Canonical "owner/repo" identity the core reports. */
  repository: string;
  /** Absolute path of the temp git workspace. */
  workdir: string;
  /**
   * Open an SSE connection to `/events` and resolve with the frames received
   * until `close()` (or until `maxFrames` arrive). Pass `lastEventId` to test
   * `Last-Event-ID` backfill, and `token`/`rawQuery` to exercise auth.
   */
  readEvents(options?: ReadEventsOptions): EventReader;
  /** Fetch a control-plane path, returning status + parsed body. */
  api(
    path: string,
    init?: { method?: string; token?: string | null; body?: unknown },
  ): Promise<{ status: number; body: unknown }>;
  /**
   * Append a committed event directly to the core's ledger (the same row a real
   * task.state / run.outcome would produce), so tests can drive deterministic
   * events. The core's cross-connection poll fans it out to `/events`.
   * Returns the assigned global `event_id`.
   */
  emitEvent(
    type: string,
    payload: Record<string, unknown>,
    ids?: { taskId?: number; runId?: number },
  ): number;
  /** Stop the core process and remove the temp workspace. */
  stop(): Promise<void>;
}

export interface ReadEventsOptions {
  lastEventId?: number;
  /** Override the token (null sends none — expect 401). */
  token?: string | null;
  /** Cap on frames to collect before auto-closing. */
  maxFrames?: number;
  /** Give up waiting for frames after this many ms (default 8000). */
  idleTimeoutMs?: number;
}

export interface EventReader {
  /** Resolves with the frames collected once the stream closes or idles out. */
  frames: Promise<SseFrame[]>;
  /** Resolves with the HTTP status (e.g. 401 when unauthenticated). */
  status: Promise<number>;
  /** Close the connection early. */
  close(): void;
}

function findCoreBinary(): string {
  if (process.env.FACTORY_CORE_BIN) {
    return resolve(process.env.FACTORY_CORE_BIN);
  }
  const exe = process.platform === "win32" ? "factory.exe" : "factory";
  // Walk up from ui/packages/web-host/test to the workspace root, then into
  // target/<triple>/debug. The MSVC and GNU triples are both accepted.
  const triples = ["x86_64-pc-windows-msvc", "x86_64-pc-windows-gnu"];
  let dir = HERE;
  for (let i = 0; i < 8; i += 1) {
    for (const triple of triples) {
      const candidate = join(dir, "target", triple, "debug", exe);
      if (existsFile(candidate)) return candidate;
    }
    // A plain `target/debug` (single-target Unix builds) is also valid.
    const plain = join(dir, "target", "debug", exe);
    if (existsFile(plain)) return plain;
    const parent = dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error(
    "factory core binary not found; build it (cargo build) or set FACTORY_CORE_BIN",
  );
}

function existsFile(path: string): boolean {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

async function freePort(): Promise<number> {
  return new Promise((resolvePort, rejectPort) => {
    const server = createServer();
    server.unref();
    server.on("error", rejectPort);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = typeof address === "object" && address ? address.port : 0;
      server.close(() => resolvePort(port));
    });
  });
}

function git(cwd: string, args: string[]): void {
  execFileSync("git", args, { cwd, stdio: "ignore" });
}

/** Start a real core serving a fresh "owner/repo" workspace. */
export async function startCore(options: { repository?: string; port?: number; token?: string } = {}): Promise<CoreHandle> {
  const repository = options.repository ?? "example/widget";
  const binary = findCoreBinary();
  const workdir = mkdtempSync(join(tmpdir(), "factory-core-harness-"));
  const home = join(workdir, "home");
  const dataHome = join(workdir, "data");
  const repoDir = join(workdir, "repo");
  const token = options.token ?? `harness-token-${Math.floor(Math.random() * 1e9)}`;
  const port = options.port ?? (await freePort());

  // Minimal git workspace the core can discover (needs an origin remote).
  git(workdir, ["init", "--quiet", repoDir]);
  git(repoDir, ["remote", "add", "origin", `https://github.com/${repository}.git`]);

  const env = {
    ...process.env,
    HOME: home,
    FACTORY_DATA_HOME: dataHome,
    FACTORY_API_TOKEN: token,
    FACTORY_PORT: String(port),
    // Fast health cadence so tests observe periodic repo.health quickly.
    FACTORY_REPO_HEALTH_INTERVAL_MS: "150",
  };
  // `factory init` materialises .factory/config.toml in the repo.
  execFileSync(binary, ["init"], { cwd: repoDir, env, stdio: "ignore" });

  const child: ChildProcess = spawn(binary, ["serve"], {
    cwd: repoDir,
    env,
    stdio: ["ignore", "ignore", "pipe"],
  });
  let stderrTail = "";
  child.stderr?.on("data", (chunk: Buffer) => {
    stderrTail = (stderrTail + chunk.toString()).slice(-2000);
  });

  await waitForHealth(port, token, () => stderrTail);

  async function waitForHealth(
    p: number,
    tk: string,
    tail: () => string,
  ): Promise<void> {
    const deadline = Date.now() + 15_000;
    let lastError: unknown = new Error("not ready");
    while (Date.now() < deadline) {
      try {
        const res = await fetch(`http://127.0.0.1:${p}/api/v1/health`, {
          headers: { Authorization: `Bearer ${tk}` },
          signal: AbortSignal.timeout(1_000),
        });
        if (res.ok) return;
        lastError = new Error(`health returned ${res.status}`);
      } catch (error) {
        lastError = error;
      }
      await new Promise((r) => setTimeout(r, 100));
    }
    throw new Error(
      `core did not become ready: ${String(lastError)}; stderr: ${tail()}`,
    );
  }

  function readEvents(readOptions: ReadEventsOptions = {}): EventReader {
    const useToken = readOptions.token === undefined ? token : readOptions.token;
    const maxFrames = readOptions.maxFrames ?? Number.POSITIVE_INFINITY;
    const idleTimeoutMs = readOptions.idleTimeoutMs ?? 8_000;
    const query = new URLSearchParams();
    if (useToken) query.set("token", useToken);
    if (readOptions.lastEventId !== undefined) {
      query.set("last_id", String(readOptions.lastEventId));
    }
    const controller = new AbortController();
    const frames: SseFrame[] = [];
    let resolveFrames!: (f: SseFrame[]) => void;
    let resolveStatus!: (s: number) => void;
    const framesPromise = new Promise<SseFrame[]>((r) => (resolveFrames = r));
    const statusPromise = new Promise<number>((r) => (resolveStatus = r));

    let idleTimer: ReturnType<typeof setTimeout> | undefined;
    const finish = () => {
      if (idleTimer) clearTimeout(idleTimer);
      resolveFrames(frames);
    };
    const bumpIdle = () => {
      if (idleTimer) clearTimeout(idleTimer);
      idleTimer = setTimeout(() => controller.abort(), idleTimeoutMs);
    };

    void (async () => {
      try {
        const res = await fetch(`http://127.0.0.1:${port}/events?${query}`, {
          signal: controller.signal,
        });
        resolveStatus(res.status);
        if (!res.ok || !res.body) {
          finish();
          return;
        }
        bumpIdle();
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let index: number;
          while ((index = buffer.indexOf("\n\n")) !== -1) {
            const raw = buffer.slice(0, index);
            buffer = buffer.slice(index + 2);
            const frame = parseFrame(raw);
            if (frame) {
              frames.push(frame);
              bumpIdle();
              if (frames.length >= maxFrames) {
                controller.abort();
                break;
              }
            }
          }
          if (frames.length >= maxFrames) break;
        }
        finish();
      } catch {
        // Aborted (idle timeout or close()): resolve with what we have.
        finish();
      }
    })();

    return {
      frames: framesPromise,
      status: statusPromise,
      close: () => controller.abort(),
    };
  }

  async function api(
    path: string,
    init: { method?: string; token?: string | null; body?: unknown } = {},
  ): Promise<{ status: number; body: unknown }> {
    const useToken = init.token === undefined ? token : init.token;
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      method: init.method ?? "GET",
      headers: {
        ...(useToken ? { Authorization: `Bearer ${useToken}` } : {}),
        ...(init.body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
    });
    const text = await res.text();
    let body: unknown = null;
    try {
      body = text ? JSON.parse(text) : null;
    } catch {
      body = text;
    }
    return { status: res.status, body };
  }

  /** Locate the core's `factory.sqlite3` under the data home (recursive). */
  function findDatabase(): string {
    const needle = "factory.sqlite3";
    const walk = (dir: string): string | null => {
      for (const entry of readdirSync(dir)) {
        const full = join(dir, entry);
        const stat = statSync(full);
        if (stat.isDirectory()) {
          const found = walk(full);
          if (found) return found;
        } else if (entry === needle) {
          return full;
        }
      }
      return null;
    };
    const found = walk(dataHome);
    if (!found) {
      throw new Error(`core database not found under ${dataHome}`);
    }
    return found;
  }

  function emitEvent(
    type: string,
    payload: Record<string, unknown>,
    ids: { taskId?: number; runId?: number } = {},
  ): number {
    const db = new Database(findDatabase());
    try {
      const ts = new Date().toISOString();
      const result = db
        .prepare(
          "INSERT INTO events (type, ts, repository, task_id, run_id, payload) VALUES (?, ?, ?, ?, ?, ?)",
        )
        .run(
          type,
          ts,
          repository,
          ids.taskId ?? null,
          ids.runId ?? null,
          JSON.stringify(payload),
        );
      return Number(result.lastInsertRowid);
    } finally {
      db.close();
    }
  }

  async function stop(): Promise<void> {
    if (child.exitCode === null) {
      child.kill("SIGTERM");
      await new Promise<void>((r) => {
        const t = setTimeout(r, 2_000);
        child.once("exit", () => {
          clearTimeout(t);
          r();
        });
      });
    }
    rmSync(workdir, { recursive: true, force: true });
  }

  return { host: "127.0.0.1", port, token, repository, workdir, readEvents, api, emitEvent, stop };
}

/** Parse one SSE frame block (`id:`/`event:`/`data:` lines) into a frame. */
function parseFrame(raw: string): SseFrame | null {
  if (!raw.trim() || raw.startsWith(":")) return null; // heartbeat/comment
  let id: number | null = null;
  let event = "message";
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith("id:")) id = Number.parseInt(line.slice(3).trim(), 10);
    else if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trimStart());
  }
  if (dataLines.length === 0) return null;
  return { id, event, data: dataLines.join("\n") };
}
