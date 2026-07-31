import type { Express } from "express";
import request from "supertest";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createApp } from "../src/app.js";
import { RepositoryRegistry } from "../src/registry.js";

// HTTP API seam: supertest drives the real Express app against a real
// (in-memory) SQLite registry. Only external behaviour is asserted: status
// codes, response bodies, and the error envelope — never internal structure.
let registry: RepositoryRegistry;
let app: Express;

beforeEach(() => {
  registry = RepositoryRegistry.inMemory();
  app = createApp(registry);
});

afterEach(() => {
  registry.close();
});

describe("GET /ui/api/health", () => {
  it("returns ok", async () => {
    const res = await request(app).get("/ui/api/health");
    expect(res.status).toBe(200);
    expect(res.body.status).toBe("ok");
  });
});

describe("POST /ui/api/repos", () => {
  it("creates a repository and returns 201 with canonical identity", async () => {
    const res = await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    expect(res.status).toBe(201);
    expect(res.body.repository).toBe("owner/repo");
    expect(res.body.status).toBe("registering");
    expect(res.headers["cache-control"]).toContain("no-cache");
  });

  it("rejects a missing provider with 400 invalid_request", async () => {
    const res = await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git" });
    expect(res.status).toBe(400);
    expect(res.body.error.code).toBe("invalid_request");
  });

  it("rejects an unnormalizable remote with 400 invalid_repository", async () => {
    const res = await request(app)
      .post("/ui/api/repos")
      .send({ repository: "https://example.com/Owner/Repo", provider: "github" });
    expect(res.status).toBe(400);
    expect(res.body.error.code).toBe("invalid_repository");
  });

  it("rejects a duplicate canonical repository with 409 conflict", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    const res = await request(app)
      .post("/ui/api/repos")
      .send({ repository: "https://github.com/owner/repo", provider: "github" });
    expect(res.status).toBe(409);
    expect(res.body.error.code).toBe("conflict");
  });
});

describe("GET /ui/api/repos", () => {
  it("lists repositories", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/A.git", provider: "github" });
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/B.git", provider: "github" });
    const res = await request(app).get("/ui/api/repos");
    expect(res.status).toBe(200);
    expect(res.body.repositories.map((r: { repository: string }) => r.repository)).toEqual([
      "owner/a",
      "owner/b",
    ]);
  });
});

describe("GET /ui/api/repos/:repository", () => {
  it("returns a single repository by owner/repo path", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    const res = await request(app).get("/ui/api/repos/owner/repo");
    expect(res.status).toBe(200);
    expect(res.body.repository).toBe("owner/repo");
  });

  it("returns 404 for an unknown repository", async () => {
    const res = await request(app).get("/ui/api/repos/ghost/nope");
    expect(res.status).toBe(404);
    expect(res.body.error.code).toBe("not_found");
  });
});

describe("PATCH /ui/api/repos/:repository", () => {
  it("updates fields and returns the new row", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    const res = await request(app)
      .patch("/ui/api/repos/owner/repo")
      .send({ status: "running", container_id: "abc123", port: 7788 });
    expect(res.status).toBe(200);
    expect(res.body.status).toBe("running");
    expect(res.body.container_id).toBe("abc123");
    expect(res.body.port).toBe(7788);
  });

  it("rejects an unknown field (strict schema) with 400", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    const res = await request(app)
      .patch("/ui/api/repos/owner/repo")
      .send({ bogus_field: "x" });
    expect(res.status).toBe(400);
    expect(res.body.error.code).toBe("invalid_request");
  });

  it("returns 404 when patching a missing repository", async () => {
    const res = await request(app)
      .patch("/ui/api/repos/ghost/nope")
      .send({ status: "running" });
    expect(res.status).toBe(404);
  });
});

describe("DELETE /ui/api/repos/:repository", () => {
  it("deletes a repository and returns 204", async () => {
    await request(app)
      .post("/ui/api/repos")
      .send({ repository: "git@github.com:Owner/Repo.git", provider: "github" });
    const del = await request(app).delete("/ui/api/repos/owner/repo");
    expect(del.status).toBe(204);
    const get = await request(app).get("/ui/api/repos/owner/repo");
    expect(get.status).toBe(404);
  });

  it("returns 404 when deleting a missing repository", async () => {
    const res = await request(app).delete("/ui/api/repos/ghost/nope");
    expect(res.status).toBe(404);
  });
});
