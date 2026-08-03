/**
 * W4.2 (#40): ContainerConnectionManager against the real core (W4.1 harness).
 *
 * Behavioural assertions on the manager's external contract — the frames it
 * delivers per repository, the cursor it persists, and the backoff/reconnect
 * schedule — not on its internal Map/Set structure.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  ContainerConnectionManager,
  defaultBackoffMs,
  type ContainerFrame,
} from "../src/connections.js";
import { CursorStore } from "../src/cursors.js";
import { startCore, type CoreHandle } from "./core-harness.js";

const VALID = (n: string) => ({ from: null, to: "queued", workflow: "wf", ticket: { id: n } });

let dir: string;
let store: CursorStore;
let cores: CoreHandle[];
let managers: ContainerConnectionManager[];

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-conn-"));
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

interface ManagerHooks {
  onDisconnect?: (repo: string, failures: number) => void;
  onConnect?: (repo: string) => void;
}

function makeManager(
  delivered: { repository: string; frame: ContainerFrame }[],
  sleeps: number[],
  hooks: ManagerHooks = {},
) {
  const manager = new ContainerConnectionManager(store, {
    // Don't actually sleep; just record the backoff duration requested.
    sleep: (ms) => {
      sleeps.push(ms);
      return Promise.resolve();
    },
    onEvent: (repository, frame) => delivered.push({ repository, frame }),
    onDisconnect: hooks.onDisconnect,
    onConnect: hooks.onConnect,
  });
  managers.push(manager);
  return manager;
}

async function waitFor(condition: () => boolean, timeoutMs = 8000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error("waitFor timed out");
    await new Promise((r) => setTimeout(r, 25));
  }
}

describe("defaultBackoffMs", () => {
  it("grows exponentially and caps at 30s", () => {
    expect(defaultBackoffMs(1)).toBe(250);
    expect(defaultBackoffMs(2)).toBe(500);
    expect(defaultBackoffMs(3)).toBe(1000);
    expect(defaultBackoffMs(100)).toBe(30_000);
  });
});

describe("ContainerConnectionManager (real core)", () => {
  it("delivers a container's events tagged by repository", async () => {
    const core = await startCore({ repository: "acme/one" });
    cores.push(core);
    const delivered: { repository: string; frame: ContainerFrame }[] = [];
    const manager = makeManager(delivered, []);
    manager.track({ repository: "acme/one", host: core.host, port: core.port, token: core.token });

    const id = core.emitEvent("task.state", VALID("1"));
    await waitFor(() => delivered.some((d) => d.frame.id === id && d.repository === "acme/one"));
    expect(delivered.some((d) => d.frame.event === "task.state")).toBe(true);
  });

  it("reconnects from the persisted per-repo cursor after a drop, delivering only the gap", async () => {
    const core = await startCore({ repository: "acme/gap" });
    cores.push(core);
    // Seed a cursor as if we'd already seen event 1.
    const first = core.emitEvent("task.state", VALID("seed"));
    store.advance("acme/gap", first);

    const delivered: { repository: string; frame: ContainerFrame }[] = [];
    const manager = makeManager(delivered, []);
    manager.track({ repository: "acme/gap", host: core.host, port: core.port, token: core.token });

    // Commit two more events after the cursor; only they should be delivered.
    const a = core.emitEvent("task.state", VALID("a"));
    const b = core.emitEvent("task.state", VALID("b"));
    await waitFor(() => delivered.filter((d) => d.frame.event === "task.state").length >= 2);
    const ids = delivered.filter((d) => d.frame.event === "task.state").map((d) => d.frame.id);
    expect(ids).toEqual([a, b]);
    expect(ids.every((i) => i > first)).toBe(true);
  });

  it("keeps two repos' cursors and streams independent", async () => {
    const coreA = await startCore({ repository: "acme/aaa" });
    const coreB = await startCore({ repository: "acme/bbb" });
    cores.push(coreA, coreB);
    const delivered: { repository: string; frame: ContainerFrame }[] = [];
    const manager = makeManager(delivered, []);
    manager.track({ repository: "acme/aaa", host: coreA.host, port: coreA.port, token: coreA.token });
    manager.track({ repository: "acme/bbb", host: coreB.host, port: coreB.port, token: coreB.token });

    const idA = coreA.emitEvent("task.state", VALID("a"));
    const idB = coreB.emitEvent("task.state", VALID("b"));
    await waitFor(
      () =>
        delivered.some((d) => d.repository === "acme/aaa" && d.frame.id === idA) &&
        delivered.some((d) => d.repository === "acme/bbb" && d.frame.id === idB),
    );
    // Per-repo cursors advanced independently.
    expect(store.get("acme/aaa")).toBeGreaterThanOrEqual(idA);
    expect(store.get("acme/bbb")).toBeGreaterThanOrEqual(idB);
  });

  it("backs off and reconnects when a container is unreachable, then recovers", async () => {
    const core = await startCore({ repository: "acme/flap" });
    cores.push(core);
    const delivered: { repository: string; frame: ContainerFrame }[] = [];
    const sleeps: number[] = [];
    const disconnects: number[] = [];
    let connects = 0;
    const manager = makeManager(delivered, sleeps, {
      onDisconnect: (_repo, failures) => disconnects.push(failures),
      onConnect: () => {
        connects += 1;
      },
    });
    manager.track({ repository: "acme/flap", host: core.host, port: core.port, token: core.token });

    // Establish the first connection.
    await waitFor(() => connects >= 1);
    // Kill the core: the stream drops, the manager backs off (recorded sleeps)
    // and keeps trying without throwing.
    const { port, token } = core;
    await core.stop();
    await waitFor(() => disconnects.length >= 1);
    expect(sleeps.length).toBeGreaterThanOrEqual(1);
    expect(sleeps[0]).toBe(defaultBackoffMs(1));

    // Start a replacement core on the SAME port/token; the manager reconnects
    // and resumes delivery (its tracked endpoint is unchanged).
    const revived = await startCore({ repository: "acme/flap", port, token });
    cores.push(revived);
    const id = revived.emitEvent("task.state", VALID("after"));
    await waitFor(() => delivered.some((d) => d.frame.id === id && d.frame.event === "task.state"));
    expect(connects).toBeGreaterThanOrEqual(2);
  });
});
