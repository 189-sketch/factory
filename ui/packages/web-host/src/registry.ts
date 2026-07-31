/**
 * Repository registry: durable record of every repository the ui orchestrates.
 *
 * Backed by a local SQLite `repositories` table (better-sqlite3). The registry
 * is the single source of truth for "which repositories exist, on which
 * backend, in what state, mapped to which container". Container orchestration
 * (W3.2+), the onboarding pipeline (W3.3) and the aggregation layer (W4) all
 * read and write through this module; the HTTP layer (W3.1) is a thin adapter.
 *
 * The canonical identity (`repository` column, "owner/repo" lower-cased) is
 * the primary key — see identity.ts. All lookups are by that canonical form.
 */

import Database from "better-sqlite3";
import { canonicalGithubIdentity } from "./identity.js";

/** Execution backend kinds. `microvm` is reserved but not implemented (W3). */
export const BACKEND_KINDS = ["docker", "podman-rootless", "remote", "microvm"] as const;
export type BackendKind = (typeof BACKEND_KINDS)[number];

/**
 * Lifecycle status of a registered repository.
 *
 *  - `registering`  row created, container not yet up (onboarding in progress)
 *  - `running`      container up and healthy
 *  - `paused`       paused by operator (batch op), not scheduled
 *  - `offline`      container unreachable (supervision marks this, W3.4)
 *  - `destroyed`    container torn down (idle / manual), workspace snapshotted
 */
export const REPOSITORY_STATUSES = [
  "registering",
  "running",
  "paused",
  "offline",
  "destroyed",
] as const;
export type RepositoryStatus = (typeof REPOSITORY_STATUSES)[number];

export interface RepositoryRow {
  /** Canonical "owner/repo" identity (primary key). */
  repository: string;
  provider: string;
  backend: BackendKind;
  credentialRef: string | null;
  /** Idle timeout in seconds before auto-destroy; null = default. */
  idleTimeoutSeconds: number | null;
  branch: string | null;
  triggerLabels: string[];
  status: RepositoryStatus;
  containerId: string | null;
  host: string | null;
  port: number | null;
  /** Per-container bearer token (never logged; only its hash goes to labels). */
  apiToken: string | null;
  /** ISO-8601 timestamp of last observed activity; drives idle destruction. */
  lastActivityAt: string | null;
  /** Current onboarding step, for progress reporting (W3.3). */
  onboardStep: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface CreateRepositoryInput {
  /** Any supported git remote; normalized to canonical "owner/repo". */
  repository: string;
  provider: string;
  backend?: BackendKind;
  credentialRef?: string | null;
  idleTimeoutSeconds?: number | null;
  branch?: string | null;
  triggerLabels?: string[];
  status?: RepositoryStatus;
}

export interface UpdateRepositoryInput {
  provider?: string;
  backend?: BackendKind;
  credentialRef?: string | null;
  idleTimeoutSeconds?: number | null;
  branch?: string | null;
  triggerLabels?: string[];
  status?: RepositoryStatus;
  containerId?: string | null;
  host?: string | null;
  port?: number | null;
  apiToken?: string | null;
  lastActivityAt?: string | null;
  onboardStep?: string | null;
}

export class RepositoryConflictError extends Error {
  constructor(repository: string) {
    super(`repository already registered: ${repository}`);
    this.name = "RepositoryConflictError";
  }
}

const SCHEMA = `
CREATE TABLE IF NOT EXISTS repositories (
  repository           TEXT PRIMARY KEY,
  provider             TEXT NOT NULL,
  backend              TEXT NOT NULL DEFAULT 'docker',
  credential_ref       TEXT,
  idle_timeout_seconds INTEGER,
  branch               TEXT,
  trigger_labels       TEXT NOT NULL DEFAULT '[]',
  status               TEXT NOT NULL DEFAULT 'registering',
  container_id         TEXT,
  host                 TEXT,
  port                 INTEGER,
  api_token            TEXT,
  last_activity_at     TEXT,
  onboard_step         TEXT,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL
);
`;

interface RawRow {
  repository: string;
  provider: string;
  backend: string;
  credential_ref: string | null;
  idle_timeout_seconds: number | null;
  branch: string | null;
  trigger_labels: string;
  status: string;
  container_id: string | null;
  host: string | null;
  port: number | null;
  api_token: string | null;
  last_activity_at: string | null;
  onboard_step: string | null;
  created_at: string;
  updated_at: string;
}

function toRow(raw: RawRow): RepositoryRow {
  return {
    repository: raw.repository,
    provider: raw.provider,
    backend: raw.backend as BackendKind,
    credentialRef: raw.credential_ref,
    idleTimeoutSeconds: raw.idle_timeout_seconds,
    branch: raw.branch,
    triggerLabels: JSON.parse(raw.trigger_labels) as string[],
    status: raw.status as RepositoryStatus,
    containerId: raw.container_id,
    host: raw.host,
    port: raw.port,
    apiToken: raw.api_token,
    lastActivityAt: raw.last_activity_at,
    onboardStep: raw.onboard_step,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
  };
}

export class RepositoryRegistry {
  private readonly db: Database.Database;

  private constructor(db: Database.Database) {
    this.db = db;
  }

  /** Open (or create) a registry at the given SQLite file path. */
  static open(path: string): RepositoryRegistry {
    const db = new Database(path);
    db.pragma("journal_mode = WAL");
    db.exec(SCHEMA);
    return new RepositoryRegistry(db);
  }

  /** Open an in-memory registry (used by tests). */
  static inMemory(): RepositoryRegistry {
    return RepositoryRegistry.open(":memory:");
  }

  close(): void {
    this.db.close();
  }

  create(input: CreateRepositoryInput): RepositoryRow {
    const repository = canonicalGithubIdentity(input.repository);
    const now = new Date().toISOString();
    const row: RepositoryRow = {
      repository,
      provider: input.provider,
      backend: input.backend ?? "docker",
      credentialRef: input.credentialRef ?? null,
      idleTimeoutSeconds: input.idleTimeoutSeconds ?? null,
      branch: input.branch ?? null,
      triggerLabels: input.triggerLabels ?? [],
      status: input.status ?? "registering",
      containerId: null,
      host: null,
      port: null,
      apiToken: null,
      lastActivityAt: null,
      onboardStep: null,
      createdAt: now,
      updatedAt: now,
    };
    try {
      this.db
        .prepare(
          `INSERT INTO repositories (
             repository, provider, backend, credential_ref, idle_timeout_seconds,
             branch, trigger_labels, status, container_id, host, port, api_token,
             last_activity_at, onboard_step, created_at, updated_at
           ) VALUES (
             @repository, @provider, @backend, @credentialRef, @idleTimeoutSeconds,
             @branch, @triggerLabels, @status, @containerId, @host, @port, @apiToken,
             @lastActivityAt, @onboardStep, @createdAt, @updatedAt
           )`,
        )
        .run({
          ...row,
          triggerLabels: JSON.stringify(row.triggerLabels),
        });
    } catch (error) {
      if (isUniqueViolation(error)) {
        throw new RepositoryConflictError(repository);
      }
      throw error;
    }
    return row;
  }

  get(repository: string): RepositoryRow | null {
    const canonical = canonicalGithubIdentity(repository);
    const raw = this.db
      .prepare(`SELECT * FROM repositories WHERE repository = ?`)
      .get(canonical) as RawRow | undefined;
    return raw ? toRow(raw) : null;
  }

  /** Look up by canonical identity without re-normalizing (internal use). */
  private getByCanonical(canonical: string): RepositoryRow | null {
    const raw = this.db
      .prepare(`SELECT * FROM repositories WHERE repository = ?`)
      .get(canonical) as RawRow | undefined;
    return raw ? toRow(raw) : null;
  }

  list(): RepositoryRow[] {
    const raws = this.db
      .prepare(`SELECT * FROM repositories ORDER BY created_at ASC, repository ASC`)
      .all() as RawRow[];
    return raws.map(toRow);
  }

  update(repository: string, patch: UpdateRepositoryInput): RepositoryRow | null {
    const canonical = canonicalGithubIdentity(repository);
    const existing = this.getByCanonical(canonical);
    if (!existing) {
      return null;
    }
    const merged: RepositoryRow = {
      ...existing,
      provider: patch.provider ?? existing.provider,
      backend: patch.backend ?? existing.backend,
      credentialRef:
        patch.credentialRef !== undefined ? patch.credentialRef : existing.credentialRef,
      idleTimeoutSeconds:
        patch.idleTimeoutSeconds !== undefined
          ? patch.idleTimeoutSeconds
          : existing.idleTimeoutSeconds,
      branch: patch.branch !== undefined ? patch.branch : existing.branch,
      triggerLabels: patch.triggerLabels ?? existing.triggerLabels,
      status: patch.status ?? existing.status,
      containerId:
        patch.containerId !== undefined ? patch.containerId : existing.containerId,
      host: patch.host !== undefined ? patch.host : existing.host,
      port: patch.port !== undefined ? patch.port : existing.port,
      apiToken: patch.apiToken !== undefined ? patch.apiToken : existing.apiToken,
      lastActivityAt:
        patch.lastActivityAt !== undefined
          ? patch.lastActivityAt
          : existing.lastActivityAt,
      onboardStep:
        patch.onboardStep !== undefined ? patch.onboardStep : existing.onboardStep,
      updatedAt: new Date().toISOString(),
    };
    this.db
      .prepare(
        `UPDATE repositories SET
           provider = @provider,
           backend = @backend,
           credential_ref = @credentialRef,
           idle_timeout_seconds = @idleTimeoutSeconds,
           branch = @branch,
           trigger_labels = @triggerLabels,
           status = @status,
           container_id = @containerId,
           host = @host,
           port = @port,
           api_token = @apiToken,
           last_activity_at = @lastActivityAt,
           onboard_step = @onboardStep,
           updated_at = @updatedAt
         WHERE repository = @repository`,
      )
      .run({
        ...merged,
        triggerLabels: JSON.stringify(merged.triggerLabels),
      });
    return merged;
  }

  delete(repository: string): boolean {
    const canonical = canonicalGithubIdentity(repository);
    const result = this.db
      .prepare(`DELETE FROM repositories WHERE repository = ?`)
      .run(canonical);
    return result.changes > 0;
  }
}

function isUniqueViolation(error: unknown): boolean {
  return (
    error instanceof Error &&
    "code" in error &&
    (error as { code?: string }).code === "SQLITE_CONSTRAINT_PRIMARYKEY"
  ) || (error instanceof Error && /UNIQUE constraint failed/.test(error.message));
}
