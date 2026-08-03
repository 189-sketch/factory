/**
 * W4.6 (#44): degradation policy — offline / ready / destroyed synthesis.
 *
 * Unit-level: the policy's registry transitions and emitted lifecycle events,
 * driven directly (no containers). The threshold and emit sink are injected.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DegradationPolicy } from "../src/lifecycle.js";
import { RepositoryRegistry } from "../src/registry.js";

let dir: string;
let registry: RepositoryRegistry;
let emitted: { repository: string; status: string; extra?: Record<string, unknown> }[];

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-deg-"));
  registry = RepositoryRegistry.open(join(dir, "registry.db"));
  emitted = [];
});

afterEach(() => {
  registry.close();
  rmSync(dir, { recursive: true, force: true });
});

function policy(overrides: Partial<{ offlineAfterFailures: number }> = {}) {
  return new DegradationPolicy(registry, {
    offlineAfterFailures: overrides.offlineAfterFailures,
    emit: (repository, status, extra) => emitted.push({ repository, status, extra }),
  });
}

function running(repository: string): void {
  registry.create({ repository, provider: "github", status: "running" });
  registry.update(repository, { containerId: "c", host: "h", port: 1, apiToken: "t" });
}

describe("DegradationPolicy", () => {
  it("marks offline and emits repo.health{offline} once the failure threshold is reached", () => {
    running("acme/one");
    const p = policy({ offlineAfterFailures: 3 });
    p.onDisconnect("acme/one", 1);
    p.onDisconnect("acme/one", 2);
    expect(registry.get("acme/one")!.status).toBe("running"); // below threshold
    expect(emitted).toHaveLength(0);
    p.onDisconnect("acme/one", 3);
    expect(registry.get("acme/one")!.status).toBe("offline");
    expect(emitted).toEqual([
      { repository: "acme/one", status: "offline", extra: { consecutive_failures: 3 } },
    ]);
  });

  it("emits offline only once across repeated failures (no spam)", () => {
    running("acme/one");
    const p = policy({ offlineAfterFailures: 2 });
    p.onDisconnect("acme/one", 2);
    p.onDisconnect("acme/one", 3);
    p.onDisconnect("acme/one", 4);
    expect(emitted.filter((e) => e.status === "offline")).toHaveLength(1);
  });

  it("emits ready and flips back to running when the connection recovers", () => {
    running("acme/one");
    const p = policy({ offlineAfterFailures: 1 });
    p.onDisconnect("acme/one", 1);
    expect(registry.get("acme/one")!.status).toBe("offline");
    p.onConnect("acme/one");
    expect(registry.get("acme/one")!.status).toBe("running");
    expect(emitted.map((e) => e.status)).toEqual(["offline", "ready"]);
  });

  it("does not emit ready for a repo that was never offline", () => {
    running("acme/one");
    const p = policy();
    p.onConnect("acme/one");
    expect(emitted).toHaveLength(0);
    expect(registry.get("acme/one")!.status).toBe("running");
  });

  it("marks destroyed and emits repo.health{destroyed}", () => {
    running("acme/one");
    const p = policy();
    p.onDestroyed("acme/one");
    expect(registry.get("acme/one")!.status).toBe("destroyed");
    expect(emitted).toEqual([{ repository: "acme/one", status: "destroyed", extra: undefined }]);
  });

  it("does not resurrect a destroyed repo on reconnect", () => {
    running("acme/one");
    const p = policy({ offlineAfterFailures: 1 });
    p.onDisconnect("acme/one", 1);
    p.onDestroyed("acme/one");
    p.onConnect("acme/one"); // a late connect after destroy must not flip to running
    expect(registry.get("acme/one")!.status).toBe("destroyed");
  });

  it("isolates degradation per repository", () => {
    running("acme/one");
    running("acme/two");
    const p = policy({ offlineAfterFailures: 1 });
    p.onDisconnect("acme/one", 5);
    expect(registry.get("acme/one")!.status).toBe("offline");
    expect(registry.get("acme/two")!.status).toBe("running");
    expect(emitted.map((e) => e.repository)).toEqual(["acme/one"]);
  });
});
