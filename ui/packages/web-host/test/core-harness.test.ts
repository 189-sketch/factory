/**
 * W4.1 (#39): the real-core test harness, verified against the real binary.
 *
 * These tests prove the seam the rest of W4 builds on: a genuine `factory
 * serve` process can be started on demand, streams well-formed SSE events with
 * a global cursor, honours `Last-Event-ID` backfill, enforces bearer auth, and
 * answers the control plane. No docker, no fake core.
 */

import { afterEach, describe, expect, it } from "vitest";
import { startCore, type CoreHandle } from "./core-harness.js";

let core: CoreHandle | undefined;

afterEach(async () => {
  await core?.stop();
  core = undefined;
});

describe("core-harness (real factory serve)", () => {
  it("starts a real core that becomes healthy and reports its identity", async () => {
    core = await startCore({ repository: "acme/gadget" });
    const health = await core.api("/api/v1/health");
    expect(health.status).toBe(200);
    expect(health.body).toMatchObject({ status: "ok", repository: "acme/gadget" });
  });

  it("streams well-formed SSE events with a monotonically increasing cursor", async () => {
    core = await startCore({ repository: "acme/stream" });
    const reader = core.readEvents({ maxFrames: 2, idleTimeoutMs: 6000 });
    expect(await reader.status).toBe(200);
    const frames = await reader.frames;
    expect(frames.length).toBeGreaterThanOrEqual(1);
    for (const frame of frames) {
      expect(frame.id).not.toBeNull();
      expect(frame.event).toBe("repo.health");
      const envelope = JSON.parse(frame.data) as Record<string, unknown>;
      expect(envelope.repository).toBe("acme/stream");
      expect(envelope.type).toBe("repo.health");
    }
    // Global cursor is monotonic across the frames we saw.
    const ids = frames.map((f) => f.id!);
    expect([...ids].sort((a, b) => a - b)).toEqual(ids);
  });

  it("rejects /events without a token (401)", async () => {
    core = await startCore();
    const reader = core.readEvents({ token: null });
    expect(await reader.status).toBe(401);
    expect(await reader.frames).toEqual([]);
  });

  it("rejects control-plane reads without a token (401)", async () => {
    core = await startCore();
    expect((await core.api("/api/v1/tasks", { token: null })).status).toBe(401);
    expect((await core.api("/api/v1/status", { token: null })).status).toBe(401);
  });

  it("answers control-plane endpoints with the token", async () => {
    core = await startCore({ repository: "acme/api" });
    expect((await core.api("/api/v1/tasks")).status).toBe(200);
    const status = await core.api("/api/v1/status");
    expect(status.status).toBe(200);
    expect(status.body).toHaveProperty("status");
  });

  it("drives deterministic events via emitEvent and streams them", async () => {
    core = await startCore({ repository: "acme/drive" });
    // task.state payloads must satisfy the contract schema (from/to/workflow/
    // ticket) or the core validates-and-drops them on the way out.
    const eventId = core.emitEvent("task.state", {
      from: null,
      to: "queued",
      workflow: "wf",
      ticket: { id: "1" },
    });
    expect(eventId).toBeGreaterThan(0);
    const frames = await core
      .readEvents({ lastEventId: eventId - 1, maxFrames: 1, idleTimeoutMs: 6000 })
      .frames;
    const ours = frames.filter((f) => f.event === "task.state");
    expect(ours.length).toBeGreaterThanOrEqual(1);
    expect(JSON.parse(ours[0]!.data)).toMatchObject({ repository: "acme/drive" });
  });

  it("backfills only events after Last-Event-ID, never re-sending earlier ones", async () => {
    core = await startCore();
    // Learn the current high-water mark from the first committed event.
    const first = await core.readEvents({ maxFrames: 1, idleTimeoutMs: 6000 }).frames;
    const cursor = first[0]!.id!;
    // Commit two new valid events AFTER the cursor, then reconnect from it.
    const payload = (n: string) => ({ from: null, to: "queued", workflow: "wf", ticket: { id: n } });
    const a = core.emitEvent("task.state", payload("a"));
    const b = core.emitEvent("task.state", payload("b"));
    const reader = core.readEvents({ lastEventId: cursor, maxFrames: 2, idleTimeoutMs: 6000 });
    const taskStates = (await reader.frames).filter((f) => f.event === "task.state");
    const ids = taskStates.map((f) => f.id!);
    // Exactly the two events after the cursor, in order — nothing re-sent.
    expect(ids).toEqual([a, b]);
    expect(ids.every((id) => id > cursor)).toBe(true);
  });
});
