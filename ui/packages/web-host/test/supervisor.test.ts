import { describe, expect, it } from "vitest";
import { UiEventBus } from "../src/events.js";
import { FakeDriver } from "../src/fake-driver.js";
import { RepositoryRegistry, type RepositoryRow } from "../src/registry.js";
import {
  DEFAULT_IDLE_TIMEOUT_SECONDS,
  DEFAULT_MAX_ACTIVE_CONTAINERS,
  Supervisor,
  repositoryBackoffSeconds,
} from "../src/supervisor.js";

// Supervision seam: FakeDriver + injectable clock. Time is driven explicitly
// by passing `now` to tick/admit/onContainerEvent — no real timers.

const HOUR = 3600_000;

function setup(options: ConstructorParameters<typeof Supervisor>[3] = {}) {
  const registry = RepositoryRegistry.inMemory();
  const driver = new FakeDriver();
  const bus = new UiEventBus();
  const supervisor = new Supervisor(registry, driver, bus, options);
  return { registry, driver, bus, supervisor };
}

function seedRunning(
  registry: RepositoryRegistry,
  driver: FakeDriver,
  repo: string,
  lastActivityAt: string,
  backend = "docker",
): RepositoryRow {
  registry.create({ repository: repo, provider: "github", backend: backend as never });
  const id = `c-${repo.replace("/", "-")}`;
  // Register the container in the fake driver so remove() has something to delete.
  void driver;
  return registry.update(repo, {
    status: "running",
    containerId: id,
    host: "127.0.0.1",
    port: 7788,
    lastActivityAt,
  })!;
}

describe("repositoryBackoffSeconds (port of core repository_backoff)", () => {
  it("doubles from 5s and caps at 900s", () => {
    expect(repositoryBackoffSeconds(1)).toBe(5);
    expect(repositoryBackoffSeconds(2)).toBe(10);
    expect(repositoryBackoffSeconds(8)).toBe(640);
    expect(repositoryBackoffSeconds(9)).toBe(900);
    expect(repositoryBackoffSeconds(100)).toBe(900);
  });
});

describe("idle destruction", () => {
  it("destroys a container idle beyond the default 10h timeout", async () => {
    const { registry, driver, supervisor } = setup();
    const t0 = new Date("2026-07-31T00:00:00Z");
    seedRunning(registry, driver, "owner/a", t0.toISOString());
    const containerId = registry.get("owner/a")!.containerId!;

    const later = new Date(t0.getTime() + (DEFAULT_IDLE_TIMEOUT_SECONDS + 60) * 1000);
    await supervisor.tick(later);

    expect(registry.get("owner/a")!.status).toBe("destroyed");
    expect(driver.calls).toContain(`remove:${containerId}`);
    registry.close();
  });

  it("keeps an active container under the timeout", async () => {
    const { registry, supervisor } = setup();
    const t0 = new Date("2026-07-31T00:00:00Z");
    seedRunning(registry, new FakeDriver(), "owner/a", t0.toISOString());
    await supervisor.tick(new Date(t0.getTime() + HOUR));
    expect(registry.get("owner/a")!.status).toBe("running");
    registry.close();
  });

  it("honours a per-repo idle timeout override", async () => {
    const { registry, supervisor } = setup();
    const t0 = new Date("2026-07-31T00:00:00Z");
    const row = seedRunning(registry, new FakeDriver(), "owner/a", t0.toISOString());
    registry.update("owner/a", { idleTimeoutSeconds: 3600 }); // 1h
    // 2h later: beyond the 1h override but within the 10h default.
    await supervisor.tick(new Date(t0.getTime() + 2 * HOUR));
    expect(registry.get("owner/a")!.status).toBe("destroyed");
    void row;
    registry.close();
  });

  it("invokes onSnapshotGap when no snapshot trigger is wired (contract gap)", async () => {
    const gaps: string[] = [];
    const { registry, supervisor } = setup({ onSnapshotGap: (row) => gaps.push(row.repository) });
    const t0 = new Date("2026-07-31T00:00:00Z");
    seedRunning(registry, new FakeDriver(), "owner/a", t0.toISOString());
    await supervisor.tick(new Date(t0.getTime() + (DEFAULT_IDLE_TIMEOUT_SECONDS + 60) * 1000));
    expect(gaps).toEqual(["owner/a"]);
    registry.close();
  });
});

describe("limits + LRU eviction", () => {
  it("evicts the least-recently-active container when the global cap is hit", async () => {
    const { registry, supervisor } = setup({ maxActiveContainers: 2 });
    const t0 = new Date("2026-07-31T00:00:00Z").getTime();
    const driver = new FakeDriver();
    seedRunning(registry, driver, "owner/old", new Date(t0 - 3 * HOUR).toISOString());
    seedRunning(registry, driver, "owner/mid", new Date(t0 - 2 * HOUR).toISOString());
    registry.create({ repository: "owner/new", provider: "github" });

    const result = await supervisor.admit("owner/new", new Date(t0));
    expect(result.admitted).toBe(true);
    expect(result.evicted).toEqual(["owner/old"]);
    expect(registry.get("owner/old")!.status).toBe("destroyed");
    expect(registry.get("owner/mid")!.status).toBe("running");
    registry.close();
  });

  it("enforces a per-backend cap independent of the global cap", async () => {
    const { registry, supervisor } = setup({
      maxActiveContainers: DEFAULT_MAX_ACTIVE_CONTAINERS,
      backendLimits: [{ backend: "docker", maxContainers: 1 }],
    });
    const t0 = new Date("2026-07-31T00:00:00Z").getTime();
    const driver = new FakeDriver();
    seedRunning(registry, driver, "owner/a", new Date(t0 - HOUR).toISOString(), "docker");
    registry.create({ repository: "owner/b", provider: "github", backend: "docker" as never });

    const result = await supervisor.admit("owner/b", new Date(t0));
    expect(result.admitted).toBe(true);
    expect(result.evicted).toEqual(["owner/a"]);
    registry.close();
  });

  it("admits without eviction when under the cap", async () => {
    const { registry, supervisor } = setup({ maxActiveContainers: 8 });
    const t0 = new Date("2026-07-31T00:00:00Z").getTime();
    seedRunning(registry, new FakeDriver(), "owner/a", new Date(t0).toISOString());
    registry.create({ repository: "owner/b", provider: "github" });
    const result = await supervisor.admit("owner/b", new Date(t0));
    expect(result.admitted).toBe(true);
    expect(result.evicted).toEqual([]);
    registry.close();
  });
});

describe("backoff rebuild + fault isolation", () => {
  it("marks a crashed repo offline and rebuilds after the backoff", async () => {
    const rebuilt: string[] = [];
    const { registry, supervisor } = setup({
      rebuild: async (row) => {
        rebuilt.push(row.repository);
      },
    });
    const t0 = new Date("2026-07-31T00:00:00Z").getTime();
    seedRunning(registry, new FakeDriver(), "owner/a", new Date(t0).toISOString());

    await supervisor.onContainerEvent("die", "owner/a", new Date(t0));
    expect(registry.get("owner/a")!.status).toBe("offline");

    // Too early: backoff (5s) not yet elapsed.
    await supervisor.tick(new Date(t0 + 2000));
    expect(rebuilt).toEqual([]);
    expect(registry.get("owner/a")!.status).toBe("offline");

    // After the 5s backoff: rebuild fires and repo returns to running.
    await supervisor.tick(new Date(t0 + 6000));
    expect(rebuilt).toEqual(["owner/a"]);
    expect(registry.get("owner/a")!.status).toBe("running");
    registry.close();
  });

  it("isolates a crash to the affected repo only", async () => {
    const { registry, supervisor } = setup({ rebuild: async () => {} });
    const t0 = new Date("2026-07-31T00:00:00Z").getTime();
    seedRunning(registry, new FakeDriver(), "owner/a", new Date(t0).toISOString());
    seedRunning(registry, new FakeDriver(), "owner/b", new Date(t0).toISOString());

    await supervisor.onContainerEvent("die", "owner/a", new Date(t0));
    expect(registry.get("owner/a")!.status).toBe("offline");
    expect(registry.get("owner/b")!.status).toBe("running");
    registry.close();
  });

  it("backs off exponentially when rebuild keeps failing", async () => {
    let attempts = 0;
    const { registry, supervisor } = setup({
      rebuild: async () => {
        attempts += 1;
        throw new Error("still broken");
      },
    });
    const t0 = new Date("2026-07-31T00:00:00Z").getTime();
    seedRunning(registry, new FakeDriver(), "owner/a", new Date(t0).toISOString());
    await supervisor.onContainerEvent("die", "owner/a", new Date(t0));

    await supervisor.tick(new Date(t0 + 6000)); // first retry at +5s, fails
    expect(attempts).toBe(1);
    expect(registry.get("owner/a")!.status).toBe("offline");

    // Next backoff is 10s from the failure time; +8s is too early.
    await supervisor.tick(new Date(t0 + 6000 + 8000));
    expect(attempts).toBe(1);
    // +11s after the first failed retry: second retry fires.
    await supervisor.tick(new Date(t0 + 6000 + 11000));
    expect(attempts).toBe(2);
    registry.close();
  });
});
