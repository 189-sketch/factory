/**
 * Per-repo SSE cursors (W4.2, #40).
 *
 * Each container's core keeps its own monotonically-increasing `event_id`
 * space, so the backfill cursor is strictly per-repository — repo A's cursor
 * must never be used for repo B. Persisted to a SQLite `repo_cursors` table so
 * a web-host restart resumes each container's stream from where it left off
 * instead of re-replaying or dropping the gap.
 *
 * Shares the registry's database file when constructed with the same path, so
 * the aggregation layer and the orchestration registry live in one SQLite
 * database (W3/W4 same-process decision).
 */

import Database from "better-sqlite3";
import { canonicalGithubIdentity } from "./identity.js";

const SCHEMA = `
CREATE TABLE IF NOT EXISTS repo_cursors (
  repository    TEXT PRIMARY KEY,
  last_event_id INTEGER NOT NULL DEFAULT 0,
  updated_at    TEXT NOT NULL
);
`;

export class CursorStore {
  private readonly db: Database.Database;

  private constructor(db: Database.Database) {
    this.db = db;
  }

  /** Open (or create) the cursor store at the given SQLite file path. */
  static open(path: string): CursorStore {
    const db = new Database(path);
    db.pragma("journal_mode = WAL");
    db.exec(SCHEMA);
    return new CursorStore(db);
  }

  /** In-memory store for tests. */
  static inMemory(): CursorStore {
    return CursorStore.open(":memory:");
  }

  close(): void {
    this.db.close();
  }

  /** Last committed event_id for a repository (0 = start from the beginning). */
  get(repository: string): number {
    const canonical = canonicalGithubIdentity(repository);
    const row = this.db
      .prepare(`SELECT last_event_id FROM repo_cursors WHERE repository = ?`)
      .get(canonical) as { last_event_id: number } | undefined;
    return row?.last_event_id ?? 0;
  }

  /**
   * Advance a repository's cursor. Only ever moves forward — a stale/lower
   * write (e.g. an old frame arriving after a newer one) is ignored so the
   * cursor never regresses and re-replays already-seen events.
   */
  advance(repository: string, eventId: number): void {
    const canonical = canonicalGithubIdentity(repository);
    const now = new Date().toISOString();
    this.db
      .prepare(
        `INSERT INTO repo_cursors (repository, last_event_id, updated_at)
         VALUES (?, ?, ?)
         ON CONFLICT(repository) DO UPDATE SET
           last_event_id = MAX(last_event_id, excluded.last_event_id),
           updated_at = excluded.updated_at`,
      )
      .run(canonical, eventId, now);
  }

  /** Drop a repository's cursor (e.g. when its registration is removed). */
  delete(repository: string): boolean {
    const canonical = canonicalGithubIdentity(repository);
    return (
      this.db.prepare(`DELETE FROM repo_cursors WHERE repository = ?`).run(canonical)
        .changes > 0
    );
  }
}
