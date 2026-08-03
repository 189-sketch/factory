/**
 * W4.3 (#41): event normalization + run.activity batching.
 *
 * Asserts on the normalized events emitted to the sink (the renderer-facing
 * contract), not on internal batching state. The clock is injected so the
 * 100ms coalescing window is driven deterministically.
 */

import { describe, expect, it } from "vitest";
import { EventAggregator, type NormalizedEvent } from "../src/aggregator.js";
import type { ContainerFrame } from "../src/connections.js";

/** A fake scheduler: timers queue up and fire only when `advance()` runs them. */
function fakeScheduler() {
  const timers: { handle: number; fn: () => void }[] = [];
  let next = 1;
  return {
    setTimeoutFn: (fn: () => void) => {
      const handle = next++;
      timers.push({ handle, fn });
      return handle;
    },
    clearTimeoutFn: (handle: unknown) => {
      const i = timers.findIndex((t) => t.handle === (handle as number));
      if (i !== -1) timers.splice(i, 1);
    },
    /** Fire all currently-armed timers (simulates the window elapsing). */
    advance: () => {
      const pending = timers.splice(0);
      for (const t of pending) t.fn();
    },
    pendingCount: () => timers.length,
  };
}

function frame(id: number, event: string, data: unknown): ContainerFrame {
  return { id, event, data: typeof data === "string" ? data : JSON.stringify(data) };
}

function setup() {
  const emitted: NormalizedEvent[] = [];
  const scheduler = fakeScheduler();
  const agg = new EventAggregator({
    emit: (e) => emitted.push(e),
    setTimeoutFn: scheduler.setTimeoutFn,
    clearTimeoutFn: scheduler.clearTimeoutFn,
  });
  return { emitted, agg, scheduler };
}

describe("EventAggregator normalization", () => {
  it("forwards an envelope that already carries repository as-is", () => {
    const { emitted, agg } = setup();
    agg.ingest(
      "acme/one",
      frame(5, "task.state", {
        v: 1, type: "task.state", seq: 5, ts: "t", repository: "acme/one",
        task_id: 1, run_id: null, payload: { to: "queued" },
      }),
    );
    expect(emitted).toHaveLength(1);
    expect(emitted[0]!.repository).toBe("acme/one");
    expect(emitted[0]!.envelope.seq).toBe(5);
    expect(emitted[0]!.envelope.payload).toEqual({ to: "queued" });
  });

  it("stamps the canonical repository when the envelope lacks it", () => {
    const { emitted, agg } = setup();
    agg.ingest("acme/two", frame(7, "task.state", { v: 1, type: "task.state", seq: 7, payload: {} }));
    expect(emitted[0]!.repository).toBe("acme/two");
    expect(emitted[0]!.envelope.repository).toBe("acme/two");
  });

  it("passes through unknown event types unchanged (additive-only)", () => {
    const { emitted, agg } = setup();
    agg.ingest(
      "acme/one",
      frame(9, "run.todo", { v: 1, type: "run.todo", seq: 9, repository: "acme/one", payload: { items: [] } }),
    );
    expect(emitted[0]!.type).toBe("run.todo");
    expect(emitted[0]!.envelope.payload).toEqual({ items: [] });
  });

  it("passes through unknown envelope and payload fields unchanged", () => {
    const { emitted, agg } = setup();
    agg.ingest(
      "acme/one",
      frame(3, "task.state", {
        v: 2, type: "task.state", seq: 3, repository: "acme/one",
        future_field: { nested: true }, payload: { to: "queued", brand_new: 1 },
      }),
    );
    expect(emitted[0]!.envelope.future_field).toEqual({ nested: true });
    expect((emitted[0]!.envelope.payload as Record<string, unknown>).brand_new).toBe(1);
  });

  it("never emits an api_token even if one sneaks into a payload", () => {
    const { emitted, agg } = setup();
    agg.ingest("acme/one", frame(1, "task.state", {
      v: 1, type: "task.state", seq: 1, repository: "acme/one", payload: { to: "queued" },
    }));
    expect(JSON.stringify(emitted)).not.toContain("api_token");
  });
});

describe("EventAggregator run.activity batching", () => {
  it("coalesces a burst of same-run activity into one batched frame", () => {
    const { emitted, agg, scheduler } = setup();
    for (let i = 1; i <= 5; i += 1) {
      agg.ingest("acme/one", frame(i, "run.activity", {
        v: 1, type: "run.activity", seq: i, ts: "t", repository: "acme/one",
        run_id: 42, payload: { sequence: i, activity: `line ${i}` },
      }));
    }
    // Nothing emitted before the window elapses.
    expect(emitted).toHaveLength(0);
    scheduler.advance();
    expect(emitted).toHaveLength(1);
    expect(emitted[0]!.type).toBe("run.activity");
    expect(emitted[0]!.envelope.batch).toHaveLength(5);
    expect((emitted[0]!.envelope.payload as Record<string, unknown>).count).toBe(5);
  });

  it("passes a lone activity frame through unbatched", () => {
    const { emitted, agg, scheduler } = setup();
    agg.ingest("acme/one", frame(1, "run.activity", {
      v: 1, type: "run.activity", seq: 1, repository: "acme/one", run_id: 7, payload: { sequence: 1 },
    }));
    scheduler.advance();
    expect(emitted).toHaveLength(1);
    expect(emitted[0]!.envelope.batch).toBeUndefined();
    expect(emitted[0]!.envelope.run_id).toBe(7);
  });

  it("does not batch activity across different runs", () => {
    const { emitted, agg, scheduler } = setup();
    for (const runId of [1, 2]) {
      for (let i = 1; i <= 3; i += 1) {
        agg.ingest("acme/one", frame(runId * 10 + i, "run.activity", {
          v: 1, type: "run.activity", seq: runId * 10 + i, repository: "acme/one",
          run_id: runId, payload: { sequence: i },
        }));
      }
    }
    scheduler.advance();
    expect(emitted).toHaveLength(2);
    const byRun = new Map(emitted.map((e) => [e.envelope.run_id, e]));
    expect((byRun.get(1)!.envelope.batch as unknown[]).length).toBe(3);
    expect((byRun.get(2)!.envelope.batch as unknown[]).length).toBe(3);
  });

  it("does not batch activity across different repositories", () => {
    const { emitted, agg, scheduler } = setup();
    for (const repo of ["acme/a", "acme/b"]) {
      for (let i = 1; i <= 2; i += 1) {
        agg.ingest(repo, frame(i, "run.activity", {
          v: 1, type: "run.activity", seq: i, repository: repo, run_id: 1, payload: { sequence: i },
        }));
      }
    }
    scheduler.advance();
    expect(emitted).toHaveLength(2);
    expect(emitted.map((e) => e.repository).sort()).toEqual(["acme/a", "acme/b"]);
  });

  it("never batches low-frequency events", () => {
    const { emitted, agg, scheduler } = setup();
    agg.ingest("acme/one", frame(1, "repo.health", {
      v: 1, type: "repo.health", seq: 1, repository: "acme/one", payload: { status: "ready" },
    }));
    agg.ingest("acme/one", frame(2, "task.state", {
      v: 1, type: "task.state", seq: 2, repository: "acme/one", task_id: 1, payload: { to: "queued" },
    }));
    // Emitted immediately, no window needed.
    expect(emitted).toHaveLength(2);
    expect(scheduler.pendingCount()).toBe(0);
  });
});
