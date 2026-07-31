import type { Express } from "express";
import request from "supertest";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createApp } from "../src/app.js";
import { UiEventBus } from "../src/events.js";
import { FakeDriver } from "../src/fake-driver.js";
import { OnboardingPipeline } from "../src/onboard.js";
import { RepositoryRegistry } from "../src/registry.js";

let registry: RepositoryRegistry;
let driver: FakeDriver;
let app: Express;

beforeEach(() => {
  registry = RepositoryRegistry.inMemory();
  driver = new FakeDriver();
  const bus = new UiEventBus();
  const pipeline = new OnboardingPipeline(registry, driver, bus, {
    waitForReady: async () => {},
  });
  app = createApp(registry, { pipeline, bus, driver });
});

afterEach(() => {
  registry.close();
});

describe("POST /ui/api/repos/batch", () => {
  it("pauses multiple repositories and reports per-repo outcomes", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/A.git", provider: "github" });
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/B.git", provider: "github" });

    const res = await request(app)
      .post("/ui/api/repos/batch")
      .send({ action: "pause", repositories: ["owner/a", "owner/b"] });
    expect(res.status).toBe(200);
    expect(res.body.action).toBe("pause");
    expect(res.body.results).toHaveLength(2);
    expect(res.body.results.every((r: { ok: boolean }) => r.ok)).toBe(true);
  });

  it("returns per-repo errors without aborting the batch", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/A.git", provider: "github" });
    const res = await request(app)
      .post("/ui/api/repos/batch")
      .send({ action: "destroy", repositories: ["ghost/nope", "owner/a"] });
    expect(res.status).toBe(200);
    expect(res.body.results[0].ok).toBe(false);
    expect(res.body.results[1].ok).toBe(true);
  });

  it("rejects an invalid action with 400", async () => {
    const res = await request(app)
      .post("/ui/api/repos/batch")
      .send({ action: "explode", repositories: ["owner/a"] });
    expect(res.status).toBe(400);
    expect(res.body.error.code).toBe("invalid_request");
  });

  it("rejects an empty repositories list with 400", async () => {
    const res = await request(app)
      .post("/ui/api/repos/batch")
      .send({ action: "pause", repositories: [] });
    expect(res.status).toBe(400);
  });
});

describe("POST /ui/api/import-fleet", () => {
  it("imports fleet.toml content, registering and onboarding repositories", async () => {
    const content = `
[[repository]]
url = "git@github.com:Owner/A.git"
provider = "codex"

[[repository]]
url = "git@github.com:Owner/B.git"
provider = "claude"
`;
    const res = await request(app).post("/ui/api/import-fleet").send({ content });
    expect(res.status).toBe(200);
    expect(res.body.total).toBe(2);
    expect(res.body.results.every((r: { ok: boolean }) => r.ok)).toBe(true);
    expect(registry.get("owner/a")!.status).toBe("running");
    expect(registry.get("owner/b")!.status).toBe("running");
  });

  it("rejects a missing content field with 400", async () => {
    const res = await request(app).post("/ui/api/import-fleet").send({});
    expect(res.status).toBe(400);
    expect(res.body.error.code).toBe("invalid_request");
  });
});
