/**
 * W4.3 (#41): the fan-out hub — one `/ui/events` stream mixing all containers.
 *
 * Behavioural assertions on the renderer-facing stream: frames carry a ui-side
 * monotonic seq, are bucketable by repository, and a single subscriber sees
 * every container's events. Batching is covered in aggregator.test.ts; here we
 * use non-activity events to keep the stream synchronous.
 */

import { describe, expect, it } from "vitest";
import { EventHub, type HubFrame } from "../src/hub.js";

function frame(id: number, type: string, repository: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    event: type,
    data: JSON.stringify({
      v: 1, type, seq: id, ts: "t", repository, task_id: 1, run_id: null,
      payload: { to: "queued" }, ...extra,
    }),
  };
}

describe("EventHub fan-out", () => {
  it("fans a single mixed stream out to one subscriber, bucketable by repository", () => {
    const hub = new EventHub();
    const received: HubFrame[] = [];
    hub.subscribe((f) => received.push(f));

    hub.ingest("acme/one", frame(1, "task.state", "acme/one"));
    hub.ingest("acme/two", frame(1, "task.state", "acme/two"));
    hub.ingest("acme/one", frame(2, "task.state", "acme/one"));

    expect(received).toHaveLength(3);
    // Bucketing by repository groups each repo's events.
    const buckets = new Map<string, number>();
    for (const f of received) buckets.set(f.repository, (buckets.get(f.repository) ?? 0) + 1);
    expect(buckets.get("acme/one")).toBe(2);
    expect(buckets.get("acme/two")).toBe(1);
  });

  it("assigns a ui-side monotonic seq independent of per-repo cursors", () => {
    const hub = new EventHub();
    const received: HubFrame[] = [];
    hub.subscribe((f) => received.push(f));

    // Two repos whose cores share overlapping event_id spaces (both start at 1).
    hub.ingest("acme/one", frame(1, "task.state", "acme/one"));
    hub.ingest("acme/two", frame(1, "task.state", "acme/two"));
    hub.ingest("acme/one", frame(2, "task.state", "acme/one"));

    expect(received.map((f) => f.seq)).toEqual([1, 2, 3]);
  });

  it("delivers to multiple subscribers independently", () => {
    const hub = new EventHub();
    const a: HubFrame[] = [];
    const b: HubFrame[] = [];
    hub.subscribe((f) => a.push(f));
    hub.subscribe((f) => b.push(f));
    hub.ingest("acme/one", frame(1, "task.state", "acme/one"));
    expect(a).toHaveLength(1);
    expect(b).toHaveLength(1);
  });

  it("stops delivering after unsubscribe", () => {
    const hub = new EventHub();
    const received: HubFrame[] = [];
    const sub = hub.subscribe((f) => received.push(f));
    hub.ingest("acme/one", frame(1, "task.state", "acme/one"));
    sub.close();
    hub.ingest("acme/one", frame(2, "task.state", "acme/one"));
    expect(received).toHaveLength(1);
  });

  it("batches a run.activity burst into one frame on the stream", () => {
    const hub = new EventHub({ batchWindowMs: 0 }); // immediate flush on next tick
    const received: HubFrame[] = [];
    hub.subscribe((f) => received.push(f));
    for (let i = 1; i <= 4; i += 1) {
      hub.ingest("acme/one", {
        id: i, event: "run.activity",
        data: JSON.stringify({
          v: 1, type: "run.activity", seq: i, ts: "t", repository: "acme/one",
          run_id: 9, payload: { sequence: i, activity: `l${i}` },
        }),
      });
    }
    hub.flush(); // force the batch window closed
    expect(received).toHaveLength(1);
    expect(received[0]!.envelope.batch).toHaveLength(4);
  });

  it("keeps backfill ordered before live frames on reconnect (#1: no interleave)", () => {
    // Deterministic unit reproduction of the race: a live frame arriving
    // between subscribe() and activate() must not overtake the backfill.
    const hub = new EventHub();
    hub.ingest("acme/one", frame(1, "task.state", "acme/one"));
    hub.ingest("acme/one", frame(2, "task.state", "acme/one"));

    const received: number[] = [];
    const sub = hub.subscribe((f) => received.push(f.seq), /* lastSeenSeq */ 1);
    // A live frame lands AFTER subscribe computed the missed backfill but
    // BEFORE the caller finished writing it and called activate().
    hub.ingest("acme/one", frame(3, "task.state", "acme/one"));
    // Caller writes the backfill, then goes live.
    for (const f of sub.missed) received.push(f.seq);
    sub.activate();

    // Order must be backfill (2) then the live sliver (3) — never 3,2.
    expect(received).toEqual([2, 3]);
  });

  it("resync subscribers get no sliver backfill, only frames after activate", () => {
    const hub = new EventHub({ bufferSize: 2 });
    for (let n = 1; n <= 5; n += 1) hub.ingest("acme/one", frame(n, "task.state", "acme/one"));
    const received: HubFrame[] = [];
    const sub = hub.subscribe((f) => received.push(f), 1); // cursor beyond buffer
    expect(sub.resyncRequired).toBe(true);
    // A frame landing before activate() must NOT be sliver-backfilled (the
    // frontend is resyncing from scratch, not trusting any gap).
    hub.ingest("acme/one", frame(6, "task.state", "acme/one"));
    sub.activate();
    expect(received).toEqual([]);
    // Frames after activate stream live as normal.
    hub.ingest("acme/one", frame(7, "task.state", "acme/one"));
    expect(received.map((f) => f.seq)).toEqual([7]);
  });
});
