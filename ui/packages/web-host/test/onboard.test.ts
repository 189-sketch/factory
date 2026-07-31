import { describe, expect, it } from "vitest";
import { UiEventBus, type UiEvent } from "../src/events.js";
import { FakeDriver } from "../src/fake-driver.js";
import { OnboardingPipeline, sanitizeError } from "../src/onboard.js";
import { RepositoryRegistry } from "../src/registry.js";

// Onboarding pipeline seam: FakeDriver records the exact container operations,
// an injected waitForReady controls readiness, and the UiEventBus is captured
// to assert the progress stream. No docker, no network.

function setup(overrides: Partial<ConstructorParameters<typeof OnboardingPipeline>[3]> = {}) {
  const registry = RepositoryRegistry.inMemory();
  const driver = new FakeDriver();
  const bus = new UiEventBus();
  const events: UiEvent[] = [];
  bus.subscribe((e) => events.push(e));
  const pipeline = new OnboardingPipeline(registry, driver, bus, {
    waitForReady: async () => {},
    generateToken: () => "test-token-0123456789abcdef",
    ...overrides,
  });
  return { registry, driver, bus, events, pipeline };
}

describe("OnboardingPipeline", () => {
  it("onboards end-to-end: create → label → start → ready → running", async () => {
    const { pipeline, registry, driver } = setup();
    const result = await pipeline.onboard({
      gitUrl: "git@github.com:Owner/Repo.git",
      provider: "codex",
    });
    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.idempotent).toBe(false);
    const row = registry.get("owner/repo")!;
    expect(row.status).toBe("running");
    expect(row.onboardStep).toBe("ready");
    expect(row.host).toBe("127.0.0.1");
    expect(row.port).toBeGreaterThan(0);
    // Container created then started, on the egress network.
    expect(driver.calls[0]).toBe("ensureNetwork:factory-egress");
    expect(driver.calls.some((c) => c.startsWith("create:"))).toBe(true);
    expect(driver.calls.some((c) => c.startsWith("start:"))).toBe(true);
    // api_token is stored, never the raw label value.
    expect(row.apiToken).toBe("test-token-0123456789abcdef");
  });

  it("labels the container with repository/provider and only a token HASH", async () => {
    const { pipeline, driver } = setup();
    const result = await pipeline.onboard({
      gitUrl: "https://github.com/owner/repo",
      provider: "claude",
    });
    expect(result.ok).toBe(true);
    const info = await driver.inspect((await driver.list())[0]!.id);
    expect(info.labels["factory.managed"]).toBe("true");
    expect(info.labels["factory.repository"]).toBe("owner/repo");
    expect(info.labels["factory.provider"]).toBe("claude");
    const hash = info.labels["factory.api_token_hash"];
    expect(hash).toMatch(/^[0-9a-f]{8}$/);
    expect(info.labels["factory.api_token_hash"]).not.toContain("test-token");
    // Raw token appears in no label.
    expect(Object.values(info.labels).join(" ")).not.toContain("test-token-0123456789abcdef");
  });

  it("uses the provider-specific image factory-core:<provider>", async () => {
    const { pipeline, driver } = setup();
    await pipeline.onboard({ gitUrl: "git@github.com:Owner/Repo.git", provider: "codex" });
    const info = await driver.inspect((await driver.list())[0]!.id);
    expect(info.image).toBe("factory-core:codex");
  });

  it("is idempotent: a second onboard returns the same running container", async () => {
    const { pipeline, driver } = setup();
    const first = await pipeline.onboard({
      gitUrl: "git@github.com:Owner/Repo.git",
      provider: "codex",
    });
    const creates = driver.calls.filter((c) => c.startsWith("create:")).length;
    const second = await pipeline.onboard({
      gitUrl: "https://github.com/owner/repo",
      provider: "codex",
    });
    expect(second.ok).toBe(true);
    if (second.ok) expect(second.idempotent).toBe(true);
    // No additional container was created.
    expect(driver.calls.filter((c) => c.startsWith("create:")).length).toBe(creates);
    expect(first.ok && second.ok && second.repository.containerId).toBe(
      first.ok ? (await driver.list())[0]!.id : null,
    );
  });

  it("publishes per-step progress events", async () => {
    const { pipeline, events } = setup();
    await pipeline.onboard({ gitUrl: "git@github.com:Owner/Repo.git", provider: "codex" });
    const steps = events
      .filter((e) => e.type === "onboard.progress")
      .map((e) => e.payload.step);
    expect(steps).toEqual(["pull", "inject", "clone", "ready"]);
    expect(events.some((e) => e.type === "onboard.ready")).toBe(true);
  });

  it("rolls back on container start failure: removes container, clears row", async () => {
    const registry = RepositoryRegistry.inMemory();
    const driver = new FakeDriver();
    driver.failOnStart = new Error("daemon exploded");
    const bus = new UiEventBus();
    const events: UiEvent[] = [];
    bus.subscribe((e) => events.push(e));
    const pipeline = new OnboardingPipeline(registry, driver, bus, {
      waitForReady: async () => {},
    });

    const result = await pipeline.onboard({
      gitUrl: "git@github.com:Owner/Repo.git",
      provider: "codex",
    });
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.error).toContain("daemon exploded");
    // Half-built container removed, registry row cleared.
    expect(await driver.list()).toHaveLength(0);
    expect(registry.get("owner/repo")).toBeNull();
    // Failure event carries the failing step.
    const failed = events.find((e) => e.type === "onboard.failed");
    expect(failed).toBeDefined();
    expect(failed!.payload.step).toBe("clone");
  });

  it("rolls back on readiness timeout and reports the ready step", async () => {
    const registry = RepositoryRegistry.inMemory();
    const driver = new FakeDriver();
    const bus = new UiEventBus();
    const pipeline = new OnboardingPipeline(registry, driver, bus, {
      waitForReady: async () => {
        throw new Error("container did not become ready");
      },
    });
    const result = await pipeline.onboard({
      gitUrl: "git@github.com:Owner/Repo.git",
      provider: "codex",
    });
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.step).toBe("ready");
    expect(await driver.list()).toHaveLength(0);
    expect(registry.get("owner/repo")).toBeNull();
  });

  it("fails validation for an unnormalizable git URL without touching the driver", async () => {
    const { pipeline, driver } = setup();
    const result = await pipeline.onboard({
      gitUrl: "https://example.com/not/github",
      provider: "codex",
    });
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.step).toBe("validate");
    expect(driver.calls).toHaveLength(0);
  });
});

describe("sanitizeError", () => {
  it("redacts embedded basic-auth credentials", () => {
    expect(sanitizeError(new Error("clone failed: https://user:secret123@github.com/x/y"))).toBe(
      "clone failed: https://***@github.com/x/y",
    );
  });

  it("redacts token query params and long hex", () => {
    expect(sanitizeError(new Error("bad https://h/x?token=abcdef123456&y=1"))).toContain(
      "token=***",
    );
    expect(sanitizeError(new Error("id 0123456789abcdef0123456789abcdef leaked"))).toBe(
      "id *** leaked",
    );
  });

  it("bounds the message length", () => {
    expect(sanitizeError(new Error("x".repeat(5000))).length).toBeLessThanOrEqual(500);
  });
});
