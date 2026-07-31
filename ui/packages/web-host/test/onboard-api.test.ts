import type { Express } from "express";
import request from "supertest";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createApp } from "../src/app.js";
import { UiEventBus } from "../src/events.js";
import { FakeDriver } from "../src/fake-driver.js";
import { OnboardingPipeline } from "../src/onboard.js";
import { RepositoryRegistry } from "../src/registry.js";

// HTTP seam for onboarding: supertest drives the real app with the pipeline
// wired to a FakeDriver, asserting status codes and the error envelope.

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
  app = createApp(registry, { pipeline, bus });
});

afterEach(() => {
  registry.close();
});

describe("POST /ui/api/onboard", () => {
  it("returns 201 with the running repository on success", async () => {
    const res = await request(app)
      .post("/ui/api/onboard")
      .send({ git_url: "git@github.com:Owner/Repo.git", provider: "codex" });
    expect(res.status).toBe(201);
    expect(res.body.repository).toBe("owner/repo");
    expect(res.body.status).toBe("running");
    expect(res.body.port).toBeGreaterThan(0);
    expect(res.headers["cache-control"]).toContain("no-cache");
  });

  it("returns 200 (idempotent) when the repository is already running", async () => {
    await request(app)
      .post("/ui/api/onboard")
      .send({ git_url: "git@github.com:Owner/Repo.git", provider: "codex" });
    const res = await request(app)
      .post("/ui/api/onboard")
      .send({ git_url: "https://github.com/owner/repo", provider: "codex" });
    expect(res.status).toBe(200);
    expect(res.body.repository).toBe("owner/repo");
  });

  it("returns 400 when git_url is missing", async () => {
    const res = await request(app).post("/ui/api/onboard").send({ provider: "codex" });
    expect(res.status).toBe(400);
    expect(res.body.error.code).toBe("invalid_request");
  });

  it("returns 502 onboard_failed when the remote is not a GitHub repo", async () => {
    const res = await request(app)
      .post("/ui/api/onboard")
      .send({ git_url: "https://example.com/a/b", provider: "codex" });
    expect(res.status).toBe(502);
    expect(res.body.error.code).toBe("onboard_failed");
    expect(res.body.error.message).toMatch(/^validate:/);
  });
});

describe("GET /ui/events", () => {
  it("streams onboarding progress as SSE frames", async () => {
    const received: string[] = [];
    await new Promise<void>((resolve, reject) => {
      let buffer = "";
      const req = request(app)
        .get("/ui/events")
        .buffer(false)
        .parse((res, cb) => {
          res.on("data", (chunk: Buffer) => {
            buffer += chunk.toString();
            received.push(chunk.toString());
            // Once the SSE preamble arrives, trigger an onboard on another
            // connection so progress frames flow into this stream.
            if (buffer.includes("retry: 3000") && !buffer.includes("__fired")) {
              buffer += "__fired";
              void request(app)
                .post("/ui/api/onboard")
                .send({ git_url: "git@github.com:Owner/Repo.git", provider: "codex" })
                .then(() => {
                  // Give frames a tick to flush, then end the response cleanly.
                  setTimeout(() => (res as unknown as import("node:http").IncomingMessage).destroy(), 100);
                })
                .catch(reject);
            }
          });
          res.on("end", () => cb(null, true));
          res.on("error", () => cb(null, true));
        });
      req.end((err) => (err ? reject(err) : resolve()));
    });

    const text = received.join("");
    expect(text).toContain("retry: 3000");
    expect(text).toContain("event: onboard.progress");
    expect(text).toContain('"step":"pull"');
    expect(text).toContain("event: onboard.ready");
  });
});
