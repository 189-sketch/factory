/**
 * ContainerConnectionManager (W4.2, #40): one SSE connection per running
 * container to its core `/events`, with per-repo `Last-Event-ID` backfill and
 * exponential-backoff reconnect.
 *
 * For each repository registered `running` (with host/port/token), the manager
 * holds a connection to `http://{host}:{port}/events?token=…&last_id=<cursor>`.
 * On drop it reconnects from that repository's persisted cursor (CursorStore),
 * backing off exponentially per repository, so one repo's flapping never delays
 * another's stream. Parsed frames are handed to `onEvent` for normalization
 * and fan-out (W4.3); the cursor advances only after a frame is delivered.
 *
 * Time and the connection factory are injectable so tests drive reconnect and
 * backfill deterministically against the real-core harness (W4.1).
 */

import type { CursorStore } from "./cursors.js";

/** A single parsed frame received from a container's `/events` stream. */
export interface ContainerFrame {
  /** Global per-repo cursor (`id:`). */
  id: number;
  /** Event type (`event:`), e.g. "task.state". */
  event: string;
  /** Raw JSON `data:` payload. */
  data: string;
}

/** How the manager reaches one container's event stream (a test seam). */
export type EventStreamFactory = (
  endpoint: { host: string; port: number; token: string },
  lastEventId: number,
  signal: AbortSignal,
) => AsyncIterable<ContainerFrame>;

export interface ContainerEndpoint {
  repository: string;
  host: string;
  port: number;
  token: string;
}

export interface ConnectionManagerOptions {
  /** Injectable event-source; defaults to a real HTTP SSE client. */
  streamFactory?: EventStreamFactory;
  /** Backoff schedule; defaults to exponential 250ms→30s (per repo). */
  backoffMs?: (consecutiveFailures: number) => number;
  /** Sleep (injectable for deterministic tests). */
  sleep?: (ms: number) => Promise<void>;
  /** Called for each delivered frame, after the cursor advances. */
  onEvent?: (repository: string, frame: ContainerFrame) => void;
  /** Called when a repo's connection drops and a reconnect is scheduled. */
  onDisconnect?: (repository: string, consecutiveFailures: number) => void;
  /** Called when a repo's connection (re)establishes successfully. */
  onConnect?: (repository: string) => void;
}

const MIN_BACKOFF_MS = 250;
const MAX_BACKOFF_MS = 30_000;

/** Default exponential backoff: 250ms, doubling, capped at 30s. */
export function defaultBackoffMs(consecutiveFailures: number): number {
  const exponent = Math.min(Math.max(consecutiveFailures - 1, 0), 31);
  return Math.min(MIN_BACKOFF_MS * 2 ** exponent, MAX_BACKOFF_MS);
}

interface RepoConnection {
  controller: AbortController;
  failures: number;
}

export class ContainerConnectionManager {
  private readonly cursors: CursorStore;
  private readonly streamFactory: EventStreamFactory;
  private readonly backoffMs: (n: number) => number;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly onEvent?: (repository: string, frame: ContainerFrame) => void;
  private readonly onDisconnect?: (repository: string, failures: number) => void;
  private readonly onConnect?: (repository: string) => void;
  private readonly connections = new Map<string, RepoConnection>();
  private stopped = false;

  constructor(cursors: CursorStore, options: ConnectionManagerOptions = {}) {
    this.cursors = cursors;
    this.streamFactory = options.streamFactory ?? httpEventStream;
    this.backoffMs = options.backoffMs ?? defaultBackoffMs;
    this.sleep = options.sleep ?? ((ms) => new Promise((r) => setTimeout(r, ms)));
    this.onEvent = options.onEvent;
    this.onDisconnect = options.onDisconnect;
    this.onConnect = options.onConnect;
  }

  /**
   * Track a container's stream. Idempotent: re-tracking an already-tracked
   * repository is a no-op. Runs the read loop in the background.
   */
  track(endpoint: ContainerEndpoint): void {
    if (this.stopped) return;
    if (this.connections.has(endpoint.repository)) return;
    const controller = new AbortController();
    const conn: RepoConnection = { controller, failures: 0 };
    this.connections.set(endpoint.repository, conn);
    void this.runLoop(endpoint, conn);
  }

  /** Stop tracking a repository (e.g. it was destroyed); closes its stream. */
  untrack(repository: string): void {
    const conn = this.connections.get(repository);
    if (!conn) return;
    conn.controller.abort();
    this.connections.delete(repository);
  }

  /** Stop all connections. */
  async stop(): Promise<void> {
    this.stopped = true;
    for (const conn of this.connections.values()) {
      conn.controller.abort();
    }
    this.connections.clear();
  }

  /** Whether a repository currently has a managed connection. */
  isTracking(repository: string): boolean {
    return this.connections.has(repository);
  }

  private async runLoop(endpoint: ContainerEndpoint, conn: RepoConnection): Promise<void> {
    const { repository } = endpoint;
    while (!this.stopped && !conn.controller.signal.aborted) {
      const cursor = this.cursors.get(repository);
      try {
        const stream = this.streamFactory(
          { host: endpoint.host, port: endpoint.port, token: endpoint.token },
          cursor,
          conn.controller.signal,
        );
        let connected = false;
        for await (const frame of stream) {
          if (conn.controller.signal.aborted) break;
          if (!connected) {
            connected = true;
            conn.failures = 0;
            this.onConnect?.(repository);
          }
          // Advance the cursor before delivering so a crash never re-sends.
          this.cursors.advance(repository, frame.id);
          this.onEvent?.(repository, frame);
        }
        // Stream ended cleanly (server closed): treat as a drop and reconnect.
        throw new Error("event stream ended");
      } catch (error) {
        if (conn.controller.signal.aborted || this.stopped) break;
        conn.failures += 1;
        this.onDisconnect?.(repository, conn.failures);
        await this.sleep(this.backoffMs(conn.failures));
      }
    }
  }
}

/**
 * Default HTTP SSE client: connects to `/events?token=…&last_id=…` and yields
 * parsed frames. Honours abort by cancelling the underlying fetch.
 */
async function* httpEventStream(
  endpoint: { host: string; port: number; token: string },
  lastEventId: number,
  signal: AbortSignal,
): AsyncIterable<ContainerFrame> {
  const query = new URLSearchParams({ token: endpoint.token });
  if (lastEventId > 0) query.set("last_id", String(lastEventId));
  const res = await fetch(`http://${endpoint.host}:${endpoint.port}/events?${query}`, {
    signal,
  });
  if (!res.ok) {
    throw new Error(`events stream returned ${res.status}`);
  }
  if (!res.body) {
    throw new Error("events stream has no body");
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let index: number;
      while ((index = buffer.indexOf("\n\n")) !== -1) {
        const raw = buffer.slice(0, index);
        buffer = buffer.slice(index + 2);
        const frame = parseFrame(raw);
        if (frame) yield frame;
      }
    }
  } finally {
    reader.cancel().catch(() => undefined);
  }
}

/** Parse one SSE frame block into a ContainerFrame (skips heartbeats/comments). */
function parseFrame(raw: string): ContainerFrame | null {
  if (!raw.trim() || raw.startsWith(":")) return null;
  let id: number | null = null;
  let event = "message";
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith("id:")) id = Number.parseInt(line.slice(3).trim(), 10);
    else if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trimStart());
  }
  if (id === null || dataLines.length === 0) return null;
  return { id, event, data: dataLines.join("\n") };
}
