/**
 * W4.4 (#42): per-frontend cursors on `/ui/events` — reconnect backfill and
 * the resync fallback when a cursor falls outside the ring buffer.
 *
 * Asserts on the wire frames a frontend actually receives (id/event/data
 * sequence), not on the hub's internal buffer. The app is served on a real
 * loopback port; the hub is driven directly (no container needed).
 */

import type { Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createApp } from "../src/app.js";
import { EventHub } from "../src/hub.js";
import { RepositoryRegistry } from "../src/registry.js";

let registry: RepositoryRegistry;
let hub: EventHub;
let server: Server;
let base: string;

beforeEach(async () => {
  registry = RepositoryRegistry.inMemory();
  hub = new EventHub({ bufferSize: 3 }); // tiny buffer to exercise resync
  const app = createApp(registry, { hub });
  server = app.listen(0, "127.0.0.1");
  await new Promise<void>((r) => server.once("listening", r));
  const { port } = server.address() as AddressInfo;
  base = `http://127.0.0.1:${port}`;
});

afterEach(async () => {
  hub.flush();
  await new Promise<void>((r) => server.close(() => r()));
  registry.close();
});

function pushTaskState(repository: string, n: number): void {
  hub.ingest(repository, {
    id: n,
    event: "task.state",
    data: JSON.stringify({
      v: 1, type: "task.state", seq: n, ts: "t", repository, task_id: 1, run_id: null,
      payload: { to: "queued" },
    }),
  });
}

/** Read SSE frames until `count` arrive or the idle timeout fires. */
async function readSse(
  path: string,
  count: number,
  headers: Record<string, string> = {},
  idleMs = 3000,
): Promise<{ id: number | null; event: string; data: string }[]> {
  const controller = new AbortController();
  const res = await fetch(`${base}${path}`, { headers, signal: controller.signal });
  const frames: { id: number | null; event: string; data: string }[] = [];
  const timer = setTimeout(() => controller.abort(), idleMs);
  try {
    const reader = res.body!.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let i: number;
      while ((i = buffer.indexOf("\n\n")) !== -1) {
        const raw = buffer.slice(0, i);
        buffer = buffer.slice(i + 2);
        if (!raw.trim() || raw.startsWith(":")) continue;
        let id: number | null = null;
        let event = "message";
        const data: string[] = [];
        for (const line of raw.split("\n")) {
          if (line.startsWith("id:")) id = Number.parseInt(line.slice(3).trim(), 10);
          else if (line.startsWith("event:")) event = line.slice(6).trim();
          else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
        }
        if (data.length) frames.push({ id, event, data: data.join("\n") });
        if (frames.length >= count) {
          controller.abort();
          break;
        }
      }
      if (frames.length >= count) break;
    }
  } catch {
    // aborted after idle
  } finally {
    clearTimeout(timer);
  }
  return frames;
}

async function waitFor(condition: () => boolean, timeoutMs = 5000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error("waitFor timed out");
    await new Promise((r) => setTimeout(r, 10));
  }
}

describe("/ui/events per-frontend cursors", () => {
  it("streams live frames with a ui-side id cursor", async () => {
    const reading = readSse("/ui/events", 2);
    // Wait until the subscription is actually registered before publishing,
    // so the first frame can't be missed due to a connection-setup race.
    await waitFor(() => hub.subscriberCount() === 1);
    pushTaskState("acme/one", 1);
    pushTaskState("acme/one", 2);
    const frames = await reading;
    expect(frames.map((f) => f.event)).toEqual(["task.state", "task.state"]);
    expect(frames[0]!.id).toBe(1);
    expect(frames[1]!.id).toBe(2);
  });

  it("backfills missed frames on reconnect via Last-Event-ID", async () => {
    // Publish 3 frames with no client connected (they sit in the buffer).
    pushTaskState("acme/one", 1);
    pushTaskState("acme/one", 2);
    pushTaskState("acme/one", 3);
    // A frontend reconnects having last seen seq 1: it should get 2 and 3.
    const frames = await readSse("/ui/events", 2, { "Last-Event-ID": "1" });
    expect(frames.map((f) => f.id)).toEqual([2, 3]);
  });

  it("emits a synthetic resync when the cursor is beyond the ring buffer", async () => {
    // Buffer holds only the last 3; publish 10 so seq 1..7 are evicted.
    for (let n = 1; n <= 10; n += 1) pushTaskState("acme/one", n);
    // A frontend that last saw seq 2 has lost frames it can never recover.
    const frames = await readSse("/ui/events", 1, { "Last-Event-ID": "2" });
    expect(frames[0]!.event).toBe("resync");
    expect(JSON.parse(frames[0]!.data)).toMatchObject({ reason: "cursor_out_of_buffer" });
  });

  it("supports ?last_id= as a cursor fallback", async () => {
    pushTaskState("acme/one", 1);
    pushTaskState("acme/one", 2);
    const frames = await readSse("/ui/events?last_id=1", 1);
    expect(frames[0]!.id).toBe(2);
  });
});
