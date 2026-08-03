/**
 * W4.7 (#45): the assembled aggregation layer, end to end without docker.
 *
 * `createAggregation` is exercised the way index.ts wires it: a registry row
 * marked running (here pointing at a real core process standing in for the
 * container) is auto-tracked, its events reach the hub, a control-plane call
 * routes back to it, and a destroy closes the loop. This runs anywhere (no
 * docker) because the "container" is a real core process.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createAggregation, type Aggregation } from "../src/aggregation.js";
import { CursorStore } from "../src/cursors.js";
import { RepositoryRegistry } from "../src/registry.js";
import { startCore, type CoreHandle } from "./core-harness.js";

const VALID = (n: string) => ({ from: null, to: "queued", workflow: "wf", ticket: { id: n } });

let dir: string;
let registry: RepositoryRegistry;
let cores: CoreHandle[];
let aggs: Aggregation[];

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-asm-"));
  registry = RepositoryRegistry.open(join(dir, "registry.db"));
  cores = [];
  aggs = [];
});

afterEach(async () => {
  for (const a of aggs) await a.stop();
  for (const c of cores) await c.stop();
  registry.close();
  rmSync(dir, { recursive: true, force: true });
});

async function waitFor(condition: () => boolean, timeoutMs = 9000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error("waitFor timed out");
    await new Promise((r) => setTimeout(r, 25));
  }
}

function registerRunning(core: CoreHandle): void {
  registry.create({ repository: core.repository, provider: "github", status: "running" });
  registry.update(core.repository, {
    containerId: "c1", host: core.host, port: core.port, apiToken: core.token,
  });
}

describe("createAggregation assembly (real core, no docker)", () => {
  it("auto-tracks running rows, fans events to the hub, and routes control plane", async () => {
    const core = await startCore({ repository: "acme/asm" });
    cores.push(core);
    registerRunning(core);

    const agg = createAggregation(registry, { cursors: CursorStore.inMemory() });
    aggs.push(agg);
    const received: { repository: string; type: string }[] = [];
    agg.hub.subscribe((f) => received.push({ repository: f.repository, type: f.type }));

    // Event from the container reaches the hub via the tracked connection.
    core.emitEvent("task.state", VALID("1"));
    await waitFor(() => received.some((f) => f.type === "task.state" && f.repository === "acme/asm"));

    // Control plane routes back to the same container.
    const status = await agg.controlPlane.forward("acme/asm", "/status");
    expect(status.status).toBe(200);
  });

  it("notifyRunning starts tracking a repo that became ready after assembly", async () => {
    const agg = createAggregation(registry, { cursors: CursorStore.inMemory() });
    aggs.push(agg);
    const received: { repository: string; type: string }[] = [];
    agg.hub.subscribe((f) => received.push({ repository: f.repository, type: f.type }));

    // Repo comes up after the aggregation layer already started.
    const core = await startCore({ repository: "acme/late" });
    cores.push(core);
    registerRunning(core);
    agg.notifyRunning("acme/late");

    core.emitEvent("task.state", VALID("x"));
    await waitFor(() => received.some((f) => f.type === "task.state" && f.repository === "acme/late"));
  });

  it("notifyDestroyed stops tracking and emits repo.health{destroyed}", async () => {
    const core = await startCore({ repository: "acme/gone" });
    cores.push(core);
    registerRunning(core);
    const agg = createAggregation(registry, { cursors: CursorStore.inMemory() });
    aggs.push(agg);
    const received: { repository: string; status?: string }[] = [];
    agg.hub.subscribe((f) =>
      received.push({
        repository: f.repository,
        status: (f.envelope.payload as Record<string, unknown>)?.status as string | undefined,
      }),
    );

    agg.notifyDestroyed("acme/gone");
    expect(registry.get("acme/gone")!.status).toBe("destroyed");
    expect(agg.connectionManager.isTracking("acme/gone")).toBe(false);
    expect(received.some((f) => f.status === "destroyed")).toBe(true);
  });
});
