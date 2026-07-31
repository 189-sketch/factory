import { describe, expect, it } from "vitest";
import { UiEventBus } from "../src/events.js";
import { FakeDriver } from "../src/fake-driver.js";
import { importFleet, parseFleetToml } from "../src/fleet-import.js";
import { OnboardingPipeline } from "../src/onboard.js";
import { RepositoryRegistry } from "../src/registry.js";

// fleet.toml import seam: real parser + real pipeline over FakeDriver. Asserts
// parsing, registration, onboarding fan-out, and idempotency.

const SAMPLE = `
# legacy fleet configuration
[[repository]]
url = "git@github.com:Owner/A.git"
provider = "codex"
branch = "main"
trigger_labels = ["bug", "agent"]

[[repository]]
url = "https://github.com/owner/b"
provider = "claude"
idle_timeout_seconds = 3600

[settings]
idle_timeout = 10
`;

function setup() {
  const registry = RepositoryRegistry.inMemory();
  const driver = new FakeDriver();
  const bus = new UiEventBus();
  const pipeline = new OnboardingPipeline(registry, driver, bus, {
    waitForReady: async () => {},
  });
  return { registry, driver, pipeline };
}

describe("parseFleetToml", () => {
  it("parses [[repository]] blocks with strings, numbers, and arrays", () => {
    const repos = parseFleetToml(SAMPLE);
    expect(repos).toHaveLength(2);
    expect(repos[0]).toMatchObject({
      url: "git@github.com:Owner/A.git",
      provider: "codex",
      branch: "main",
      triggerLabels: ["bug", "agent"],
    });
    expect(repos[1]).toMatchObject({
      url: "https://github.com/owner/b",
      provider: "claude",
      idleTimeoutSeconds: 3600,
    });
  });

  it("ignores non-repository tables and comments", () => {
    const repos = parseFleetToml(SAMPLE);
    expect(repos.find((r) => r.url.includes("settings"))).toBeUndefined();
  });
});

describe("importFleet", () => {
  it("registers and onboards each repository", async () => {
    const { registry, pipeline } = setup();
    const result = await importFleet(SAMPLE, registry, pipeline);
    expect(result.total).toBe(2);
    expect(result.results.every((r) => r.ok && r.outcome === "onboarded")).toBe(true);
    expect(registry.get("owner/a")!.status).toBe("running");
    expect(registry.get("owner/b")!.status).toBe("running");
    expect(registry.get("owner/a")!.triggerLabels).toEqual(["bug", "agent"]);
    expect(registry.get("owner/b")!.idleTimeoutSeconds).toBe(3600);
    registry.close();
  });

  it("is idempotent: re-import leaves running repositories untouched", async () => {
    const { registry, driver, pipeline } = setup();
    await importFleet(SAMPLE, registry, pipeline);
    const createsAfterFirst = driver.calls.filter((c) => c.startsWith("create:")).length;

    const second = await importFleet(SAMPLE, registry, pipeline);
    expect(second.results.every((r) => r.outcome === "already-registered")).toBe(true);
    // No new containers created on re-import.
    expect(driver.calls.filter((c) => c.startsWith("create:")).length).toBe(
      createsAfterFirst,
    );
    registry.close();
  });

  it("reports an error entry for an unnormalizable url and continues", async () => {
    const { registry, pipeline } = setup();
    const bad = `
[[repository]]
url = "https://example.com/not/github"
provider = "codex"

[[repository]]
url = "git@github.com:Owner/Good.git"
provider = "codex"
`;
    const result = await importFleet(bad, registry, pipeline);
    expect(result.results[0]!.ok).toBe(false);
    expect(result.results[0]!.outcome).toBe("error");
    expect(result.results[1]!.ok).toBe(true);
    expect(registry.get("owner/good")!.status).toBe("running");
    registry.close();
  });
});
