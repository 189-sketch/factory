/**
 * W4.5 (#43): control-plane routing, single-flight, idempotent passthrough.
 *
 * Routing correctness is verified against a real core (W4.1 harness); the
 * single-flight collapse is verified with a counting fetch seam, since the
 * dedup is a web-host-side behaviour, not something the core observes.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ControlPlaneRouter } from "../src/controlplane.js";
import { RepositoryRegistry } from "../src/registry.js";
import { startCore, type CoreHandle } from "./core-harness.js";

let dir: string;
let registry: RepositoryRegistry;
let cores: CoreHandle[];

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-cp-"));
  registry = RepositoryRegistry.open(join(dir, "registry.db"));
  cores = [];
});

afterEach(async () => {
  for (const c of cores) await c.stop();
  registry.close();
  rmSync(dir, { recursive: true, force: true });
});

/** Register a repository row pointing at a running core. */
function registerRunning(core: CoreHandle): void {
  registry.create({ repository: core.repository, provider: "github", status: "running" });
  registry.update(core.repository, {
    containerId: "c1",
    host: core.host,
    port: core.port,
    apiToken: core.token,
  });
}

describe("ControlPlaneRouter routing (real core)", () => {
  it("routes a read to the correct container by repository", async () => {
    const core = await startCore({ repository: "acme/route" });
    cores.push(core);
    registerRunning(core);
    const router = new ControlPlaneRouter(registry);
    const result = await router.forward("acme/route", "/tasks");
    expect(result.status).toBe(200);
    expect(result.body).toEqual([]);
  });

  it("forwards status from the right container among several", async () => {
    const a = await startCore({ repository: "acme/one" });
    const b = await startCore({ repository: "acme/two" });
    cores.push(a, b);
    registerRunning(a);
    registerRunning(b);
    const router = new ControlPlaneRouter(registry);
    const statusB = await router.forward("acme/two", "/status");
    expect(statusB.status).toBe(200);
    expect((statusB.body as Record<string, unknown>).status).toBeDefined();
  });

  it("passes client_request_id through to the core for an idempotent write", async () => {
    const core = await startCore({ repository: "acme/write" });
    cores.push(core);
    registerRunning(core);
    const router = new ControlPlaneRouter(registry);
    // onboard accepts {client_request_id}; a repeat of the same id is idempotent.
    const first = await router.forward("acme/write", "/onboard", {
      method: "POST",
      body: { client_request_id: "req-1" },
    });
    const second = await router.forward("acme/write", "/onboard", {
      method: "POST",
      body: { client_request_id: "req-1" },
    });
    // Both reach the core; the second is an idempotent replay (same status).
    expect(first.status).toBe(second.status);
  });

  it("returns 404 for an unregistered repository", async () => {
    const router = new ControlPlaneRouter(registry);
    const result = await router.forward("acme/ghost", "/tasks");
    expect(result.status).toBe(404);
    expect(result.body).toMatchObject({ error: { code: "not_found" } });
  });

  it("returns 409 when the repository has no live endpoint", async () => {
    registry.create({ repository: "acme/down", provider: "github", status: "registering" });
    const router = new ControlPlaneRouter(registry);
    const result = await router.forward("acme/down", "/tasks");
    expect(result.status).toBe(409);
    expect(result.body).toMatchObject({ error: { code: "no_endpoint" } });
  });

  it("returns 502 when the container is unreachable", async () => {
    registry.create({ repository: "acme/lost", provider: "github", status: "running" });
    registry.update("acme/lost", {
      containerId: "gone",
      host: "127.0.0.1",
      port: 1, // nothing listens here
      apiToken: "tok",
    });
    const router = new ControlPlaneRouter(registry, { timeoutMs: 1000 });
    const result = await router.forward("acme/lost", "/tasks");
    expect(result.status).toBe(502);
    expect(result.body).toMatchObject({ error: { code: "upstream_unreachable" } });
  });
});

describe("ControlPlaneRouter single-flight", () => {
  it("collapses concurrent identical writes into one upstream call", async () => {
    registry.create({ repository: "acme/sf", provider: "github", status: "running" });
    registry.update("acme/sf", { containerId: "c", host: "h", port: 9, apiToken: "t" });

    let upstreamCalls = 0;
    const fetchFn = (async () => {
      upstreamCalls += 1;
      // Hold the request open briefly so the concurrent calls overlap.
      await new Promise((r) => setTimeout(r, 50));
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    }) as typeof fetch;
    const router = new ControlPlaneRouter(registry, { fetchFn });

    const [r1, r2, r3] = await Promise.all([
      router.forward("acme/sf", "/runs/1/cancel", { method: "POST", body: {} }),
      router.forward("acme/sf", "/runs/1/cancel", { method: "POST", body: {} }),
      router.forward("acme/sf", "/runs/1/cancel", { method: "POST", body: {} }),
    ]);
    expect(upstreamCalls).toBe(1);
    expect(r1.status).toBe(200);
    expect(r2).toEqual(r1);
    expect(r3).toEqual(r1);
  });

  it("does not single-flight concurrent reads", async () => {
    registry.create({ repository: "acme/sf", provider: "github", status: "running" });
    registry.update("acme/sf", { containerId: "c", host: "h", port: 9, apiToken: "t" });
    let upstreamCalls = 0;
    const fetchFn = (async () => {
      upstreamCalls += 1;
      return new Response("[]", { status: 200 });
    }) as typeof fetch;
    const router = new ControlPlaneRouter(registry, { fetchFn });
    await Promise.all([
      router.forward("acme/sf", "/tasks"),
      router.forward("acme/sf", "/tasks"),
    ]);
    expect(upstreamCalls).toBe(2);
  });

  it("does not leak the api_token into error responses", async () => {
    registry.create({ repository: "acme/leak", provider: "github", status: "running" });
    registry.update("acme/leak", {
      containerId: "c", host: "127.0.0.1", port: 1, apiToken: "super-secret-token",
    });
    const router = new ControlPlaneRouter(registry, { timeoutMs: 500 });
    const result = await router.forward("acme/leak", "/tasks");
    expect(JSON.stringify(result.body)).not.toContain("super-secret-token");
  });
});
