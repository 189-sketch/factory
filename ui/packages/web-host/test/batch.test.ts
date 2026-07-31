import { describe, expect, it } from "vitest";
import { applyBatch } from "../src/batch.js";
import { FakeDriver } from "../src/fake-driver.js";
import { RepositoryRegistry } from "../src/registry.js";

// Batch ops seam: registry + FakeDriver, asserting per-repo outcomes and that
// one bad entry never aborts the batch.

function seed(registry: RepositoryRegistry, repo: string, withContainer = false) {
  const row = registry.create({ repository: repo, provider: "github" });
  if (withContainer) {
    registry.update(repo, { status: "running", containerId: `c-${repo}` });
  }
  return row;
}

describe("applyBatch", () => {
  it("pauses and resumes repositories, reporting status", async () => {
    const registry = RepositoryRegistry.inMemory();
    const driver = new FakeDriver();
    seed(registry, "git@github.com:Owner/A.git");
    seed(registry, "git@github.com:Owner/B.git");

    const paused = await applyBatch(registry, driver, "pause", ["owner/a", "owner/b"]);
    expect(paused.results.every((r) => r.ok && r.status === "paused")).toBe(true);
    expect(registry.get("owner/a")!.status).toBe("paused");

    const resumed = await applyBatch(registry, driver, "resume", ["owner/a"]);
    expect(resumed.results[0]!.status).toBe("running");
    expect(registry.get("owner/a")!.status).toBe("running");
    registry.close();
  });

  it("destroy tears down the container and marks the repo destroyed", async () => {
    const registry = RepositoryRegistry.inMemory();
    const driver = new FakeDriver();
    seed(registry, "git@github.com:Owner/A.git", true);
    const containerId = registry.get("owner/a")!.containerId!;

    const result = await applyBatch(registry, driver, "destroy", ["owner/a"]);
    expect(result.results[0]!.ok).toBe(true);
    expect(result.results[0]!.status).toBe("destroyed");
    expect(driver.calls).toContain(`remove:${containerId}`);
    const row = registry.get("owner/a")!;
    expect(row.status).toBe("destroyed");
    expect(row.containerId).toBeNull();
    registry.close();
  });

  it("isolates failures: unknown repo errors but the rest succeed", async () => {
    const registry = RepositoryRegistry.inMemory();
    const driver = new FakeDriver();
    seed(registry, "git@github.com:Owner/A.git");

    const result = await applyBatch(registry, driver, "pause", [
      "ghost/nope",
      "owner/a",
      "not a repo at all::",
    ]);
    expect(result.results).toHaveLength(3);
    expect(result.results[0]!.ok).toBe(false);
    expect(result.results[0]!.error).toMatch(/not registered/);
    expect(result.results[1]!.ok).toBe(true);
    expect(result.results[2]!.ok).toBe(false); // unnormalizable
    expect(registry.get("owner/a")!.status).toBe("paused");
    registry.close();
  });

  it("preserves input order in results", async () => {
    const registry = RepositoryRegistry.inMemory();
    const driver = new FakeDriver();
    seed(registry, "git@github.com:Owner/A.git");
    seed(registry, "git@github.com:Owner/B.git");
    const result = await applyBatch(registry, driver, "pause", ["owner/b", "owner/a"]);
    expect(result.results.map((r) => r.repository)).toEqual(["owner/b", "owner/a"]);
    registry.close();
  });
});
