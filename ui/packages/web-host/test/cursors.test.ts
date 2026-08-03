import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CursorStore } from "../src/cursors.js";

// Cursor persistence uses a real temp-file SQLite DB so the durable semantics
// (restart resume) are exercised exactly as in production.
let dir: string;
let dbPath: string;

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "web-host-cursors-"));
  dbPath = join(dir, "cursors.db");
});

afterEach(() => {
  rmSync(dir, { recursive: true, force: true });
});

describe("CursorStore", () => {
  it("defaults a repository to cursor 0 before any frame", () => {
    const store = CursorStore.open(dbPath);
    try {
      expect(store.get("git@github.com:Owner/Repo.git")).toBe(0);
    } finally {
      store.close();
    }
  });

  it("advances and reads back a per-repo cursor, normalized by identity", () => {
    const store = CursorStore.open(dbPath);
    try {
      store.advance("git@github.com:Owner/Repo.git", 42);
      expect(store.get("https://github.com/owner/repo")).toBe(42);
    } finally {
      store.close();
    }
  });

  it("keeps cursors strictly per-repo (no cross-contamination)", () => {
    const store = CursorStore.open(dbPath);
    try {
      store.advance("owner/a", 10);
      store.advance("owner/b", 99);
      expect(store.get("owner/a")).toBe(10);
      expect(store.get("owner/b")).toBe(99);
    } finally {
      store.close();
    }
  });

  it("never regresses (a lower write is ignored)", () => {
    const store = CursorStore.open(dbPath);
    try {
      store.advance("owner/repo", 50);
      store.advance("owner/repo", 30);
      expect(store.get("owner/repo")).toBe(50);
      store.advance("owner/repo", 51);
      expect(store.get("owner/repo")).toBe(51);
    } finally {
      store.close();
    }
  });

  it("persists cursors across reopen (restart resume)", () => {
    const store = CursorStore.open(dbPath);
    store.advance("owner/repo", 123);
    store.close();

    const reopened = CursorStore.open(dbPath);
    try {
      expect(reopened.get("owner/repo")).toBe(123);
    } finally {
      reopened.close();
    }
  });

  it("deletes a cursor", () => {
    const store = CursorStore.open(dbPath);
    try {
      store.advance("owner/repo", 5);
      expect(store.delete("owner/repo")).toBe(true);
      expect(store.get("owner/repo")).toBe(0);
      expect(store.delete("owner/repo")).toBe(false);
    } finally {
      store.close();
    }
  });
});
