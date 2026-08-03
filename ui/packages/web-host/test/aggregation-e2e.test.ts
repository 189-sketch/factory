/**
 * W4.3 (#41) integration: two real cores → connection manager → hub → one
 * renderer stream, bucketed by repository.
 *
 * This is the renderer-facing promise of W4.3: a single `/ui/events`-style
 * subscription receives every container's events, each tagged with its
 * repository so the renderer can split them into per-repo views.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ContainerConnectionManager } from "../src/connections.js";
import { CursorStore } from "../src/cursors.js";
import { EventHub, type HubFrame } from "../src/hub.js";
import { startCore, type CoreHandle } from "./core-harness.js";

const VALID = (n: string) => ({ from: null, to: "queued", workflow: "wf", ticket: { id: n } });

let dir: string;
let store: CursorStore;
let cores: CoreHandle[];
let managers: ContainerConnectionManager[];

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-w43-"));
  store = CursorStore.open(join(dir, "cursors.db"));
  cores = [];
  managers = [];
});

afterEach(async () => {
  for (const m of managers) await m.stop();
  for (const c of cores) await c.stop();
  store.close();
  rmSync(dir, { recursive: true, force: true });
});

async function waitFor(condition: () => boolean, timeoutMs = 8000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error("waitFor timed out");
    await new Promise((r) => setTimeout(r, 25));
  }
}

describe("W4.3 aggregation end-to-end (real cores)", () => {
  it("mixes two containers' events into one stream bucketed by repository", async () => {
    const coreA = await startCore({ repository: "acme/alpha" });
    const coreB = await startCore({ repository: "acme/beta" });
    cores.push(coreA, coreB);

    const hub = new EventHub();
    const received: HubFrame[] = [];
    hub.subscribe((f) => received.push(f));

    const manager = new ContainerConnectionManager(store, {
      onEvent: (repository, frame) => hub.ingest(repository, frame),
    });
    managers.push(manager);
    manager.track({ repository: "acme/alpha", host: coreA.host, port: coreA.port, token: coreA.token });
    manager.track({ repository: "acme/beta", host: coreB.host, port: coreB.port, token: coreB.token });

    const idA = coreA.emitEvent("task.state", VALID("a"));
    const idB = coreB.emitEvent("task.state", VALID("b"));

    await waitFor(
      () =>
        received.some((f) => f.repository === "acme/alpha") &&
        received.some((f) => f.repository === "acme/beta"),
    );

    // One stream, two repositories, ui-side seq is monotonic.
    const seqs = received.map((f) => f.seq);
    expect([...seqs].sort((x, y) => x - y)).toEqual(seqs);
    const repos = new Set(received.map((f) => f.repository));
    expect(repos.has("acme/alpha")).toBe(true);
    expect(repos.has("acme/beta")).toBe(true);
    // Each delivered frame carries a full envelope with its repository.
    for (const f of received) {
      expect(f.envelope.repository).toBe(f.repository);
    }
    void idA; void idB;
  });
});
