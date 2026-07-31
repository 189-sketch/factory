import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  RepositoryConflictError,
  RepositoryRegistry,
} from "../src/registry.js";

// Registry tests use a real SQLite file in a temp dir (not :memory:) so the
// schema and persistence semantics are exercised exactly as in production.
let dir: string;
let registry: RepositoryRegistry;

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-registry-"));
  registry = RepositoryRegistry.open(join(dir, "registry.db"));
});

afterEach(() => {
  registry.close();
  rmSync(dir, { recursive: true, force: true });
});

describe("RepositoryRegistry", () => {
  it("creates a repository with normalized identity and defaults", () => {
    const row = registry.create({
      repository: "git@github.com:Owner/Repo.git",
      provider: "github",
    });
    expect(row.repository).toBe("owner/repo");
    expect(row.backend).toBe("docker");
    expect(row.status).toBe("registering");
    expect(row.triggerLabels).toEqual([]);
    expect(row.containerId).toBeNull();
    expect(row.createdAt).toBeTruthy();
  });

  it("rejects duplicate canonical identity regardless of input form", () => {
    registry.create({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    expect(() =>
      registry.create({ repository: "https://github.com/owner/repo", provider: "github" }),
    ).toThrow(RepositoryConflictError);
  });

  it("reads back a repository by any equivalent remote form", () => {
    registry.create({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    const found = registry.get("ssh://git@github.com/OWNER/REPO");
    expect(found).not.toBeNull();
    expect(found!.repository).toBe("owner/repo");
  });

  it("returns null for an unknown repository", () => {
    expect(registry.get("git@github.com:Ghost/Nope.git")).toBeNull();
  });

  it("lists repositories in creation order", () => {
    registry.create({ repository: "git@github.com:Owner/A.git", provider: "github" });
    registry.create({ repository: "git@github.com:Owner/B.git", provider: "github" });
    const all = registry.list();
    expect(all.map((r) => r.repository)).toEqual(["owner/a", "owner/b"]);
  });

  it("updates a subset of fields and bumps updated_at", async () => {
    const created = registry.create({
      repository: "git@github.com:Owner/Repo.git",
      provider: "github",
    });
    // Ensure a distinct timestamp.
    await new Promise((resolve) => setTimeout(resolve, 5));
    const updated = registry.update("owner/repo", {
      status: "running",
      containerId: "abc123",
      port: 7788,
      triggerLabels: ["bug", "agent"],
    });
    expect(updated).not.toBeNull();
    expect(updated!.status).toBe("running");
    expect(updated!.containerId).toBe("abc123");
    expect(updated!.port).toBe(7788);
    expect(updated!.triggerLabels).toEqual(["bug", "agent"]);
    // Untouched fields preserved.
    expect(updated!.provider).toBe("github");
    expect(updated!.createdAt).toBe(created.createdAt);
    expect(updated!.updatedAt >= created.updatedAt).toBe(true);
  });

  it("supports explicit null patch to clear a nullable field", () => {
    registry.create({
      repository: "git@github.com:Owner/Repo.git",
      provider: "github",
      branch: "main",
    });
    const updated = registry.update("owner/repo", { branch: null });
    expect(updated!.branch).toBeNull();
  });

  it("returns null when updating a missing repository", () => {
    expect(registry.update("git@github.com:Ghost/Nope.git", { status: "running" })).toBeNull();
  });

  it("deletes a repository and reports whether one was removed", () => {
    registry.create({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    expect(registry.delete("owner/repo")).toBe(true);
    expect(registry.delete("owner/repo")).toBe(false);
    expect(registry.get("owner/repo")).toBeNull();
  });

  it("persists rows across reopen", () => {
    const dbPath = join(dir, "registry.db");
    registry.create({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    registry.close();

    const reopened = RepositoryRegistry.open(dbPath);
    try {
      const found = reopened.get("owner/repo");
      expect(found).not.toBeNull();
      expect(found!.provider).toBe("github");
    } finally {
      reopened.close();
    }
    // Rebind so afterEach close() does not double-close.
    registry = RepositoryRegistry.open(join(dir, "other.db"));
  });
});
