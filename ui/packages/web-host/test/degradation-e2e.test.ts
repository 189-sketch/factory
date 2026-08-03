/**
 * W4.6 (#44) integration: a container's connection drop surfaces as a
 * ui-synthesized repo.health{offline} on the renderer stream, and recovery
 * surfaces repo.health{ready} — driven by the real connection manager against
 * a real core.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ContainerConnectionManager } from "../src/connections.js";
import { CursorStore } from "../src/cursors.js";
import { EventHub, type HubFrame } from "../src/hub.js";
import { DegradationPolicy } from "../src/lifecycle.js";
import { RepositoryRegistry } from "../src/registry.js";
import { startCore, type CoreHandle } from "./core-harness.js";

let dir: string;
let registry: RepositoryRegistry;
let store: CursorStore;
let cores: CoreHandle[];
let managers: ContainerConnectionManager[];

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-degint-"));
  registry = RepositoryRegistry.open(join(dir, "registry.db"));
  store = CursorStore.open(join(dir, "cursors.db"));
  cores = [];
  managers = [];
});

afterEach(async () => {
  for (const m of managers) await m.stop();
  for (const c of cores) await c.stop();
  store.close();
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

describe("W4.6 degradation end-to-end (real core)", () => {
  it("emits offline then ready on the renderer stream as a container drops and recovers", async () => {
    const core = await startCore({ repository: "acme/deg" });
    cores.push(core);
    registry.create({ repository: "acme/deg", provider: "github", status: "running" });
    registry.update("acme/deg", {
      containerId: "c1", host: core.host, port: core.port, apiToken: core.token,
    });

    const hub = new EventHub();
    const received: HubFrame[] = [];
    hub.subscribe((f) => received.push(f));

    const policy = new DegradationPolicy(registry, {
      offlineAfterFailures: 2,
      emit: (repository, status, extra) =>
        hub.ingest(repository, {
          id: 0,
          event: "repo.health",
          data: JSON.stringify({
            v: 1, type: "repo.health", ts: new Date().toISOString(), repository,
            payload: { status, ...(extra ?? {}) },
          }),
        }),
    });

    const manager = new ContainerConnectionManager(store, {
      // tiny backoff so the reconnect attempts fire quickly in the test
      backoffMs: () => 10,
      sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
      onEvent: (repository, frame) => hub.ingest(repository, frame),
      onDisconnect: (repository, failures) => policy.onDisconnect(repository, failures),
      onConnect: (repository) => policy.onConnect(repository),
    });
    managers.push(manager);
    manager.track({ repository: "acme/deg", host: core.host, port: core.port, token: core.token });

    // Kill the container: reconnect attempts cross the threshold -> offline.
    const { port, token } = core;
    await core.stop();
    await waitFor(() => received.some((f) => f.envelope.payload && (f.envelope.payload as Record<string, unknown>).status === "offline"));
    expect(registry.get("acme/deg")!.status).toBe("offline");

    // Revive on the same endpoint: the stream reconnects -> ready.
    const revived = await startCore({ repository: "acme/deg", port, token });
    cores.push(revived);
    await waitFor(() => received.some((f) => (f.envelope.payload as Record<string, unknown>)?.status === "ready"));
    expect(registry.get("acme/deg")!.status).toBe("running");

    // Order on the wire: offline before ready.
    const statuses = received
      .map((f) => (f.envelope.payload as Record<string, unknown>)?.status)
      .filter((s): s is string => typeof s === "string");
    expect(statuses.indexOf("offline")).toBeLessThan(statuses.indexOf("ready"));
  });
});
