import type { OnboardingPipeline } from "./onboard.js";
import { canonicalGithubIdentity } from "./identity.js";
import type { RepositoryRegistry } from "./registry.js";

/**
 * fleet.toml one-shot import (W3.5).
 *
 * Parses the legacy fleet.toml `[[repository]]` blocks and folds them into the
 * registry, then triggers onboarding for each (idempotent — an already-running
 * repository is left untouched). This is the upgrade path that keeps an
 * existing fleet configuration from being lost when moving to ui-held state.
 *
 * The parser is deliberately small: it understands `[[repository]]` tables
 * with string / number / array-of-string values, which covers the fleet.toml
 * schema. It is not a general TOML parser.
 */

export interface FleetRepository {
  /** Raw git URL or remote, normalized on import. */
  url: string;
  provider: string;
  branch?: string;
  backend?: string;
  triggerLabels?: string[];
  idleTimeoutSeconds?: number;
}

export interface ImportItemResult {
  repository: string;
  ok: boolean;
  /** "onboarded" | "already-registered" | "error" */
  outcome: "onboarded" | "already-registered" | "error";
  error?: string;
}

export interface ImportResult {
  total: number;
  results: ImportItemResult[];
}

/** Parse fleet.toml content into repository entries. */
export function parseFleetToml(content: string): FleetRepository[] {
  const repos: FleetRepository[] = [];
  let current: Record<string, unknown> | null = null;

  const flush = () => {
    if (!current) return;
    const url = stringValue(current, "url") ?? stringValue(current, "git_url");
    const provider = stringValue(current, "provider") ?? "github";
    if (url) {
      repos.push({
        url,
        provider,
        branch: stringValue(current, "branch"),
        backend: stringValue(current, "backend"),
        triggerLabels: arrayValue(current, "trigger_labels"),
        idleTimeoutSeconds: numberValue(current, "idle_timeout_seconds"),
      });
    }
    current = null;
  };

  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line === "" || line.startsWith("#")) continue;
    if (line === "[[repository]]") {
      flush();
      current = {};
      continue;
    }
    if (line.startsWith("[")) {
      // A different table ends the current repository block.
      flush();
      continue;
    }
    if (!current) continue;
    const match = /^([A-Za-z0-9_]+)\s*=\s*(.+)$/.exec(line);
    if (!match) continue;
    const [, key, rawValue] = match;
    current[key!] = parseValue(rawValue!.trim());
  }
  flush();
  return repos;
}

/** Import fleet.toml content: register + onboard each repository. */
export async function importFleet(
  content: string,
  registry: RepositoryRegistry,
  pipeline: OnboardingPipeline,
): Promise<ImportResult> {
  const entries = parseFleetToml(content);
  const results: ImportItemResult[] = [];

  for (const entry of entries) {
    let repository: string;
    try {
      repository = canonicalGithubIdentity(entry.url);
    } catch (error) {
      results.push({
        repository: entry.url,
        ok: false,
        outcome: "error",
        error: errorMessage(error),
      });
      continue;
    }

    const existing = registry.get(repository);
    if (existing && existing.status === "running") {
      results.push({ repository, ok: true, outcome: "already-registered" });
      continue;
    }

    const onboarded = await pipeline.onboard({
      gitUrl: entry.url,
      provider: entry.provider,
      branch: entry.branch ?? null,
      triggerLabels: entry.triggerLabels,
      idleTimeoutSeconds: entry.idleTimeoutSeconds ?? null,
      backend: entry.backend,
    });
    if (onboarded.ok) {
      results.push({ repository, ok: true, outcome: "onboarded" });
    } else {
      results.push({
        repository,
        ok: false,
        outcome: "error",
        error: `${onboarded.step}: ${onboarded.error}`,
      });
    }
  }

  return { total: entries.length, results };
}

function parseValue(raw: string): unknown {
  if (raw.startsWith('"') && raw.endsWith('"')) {
    return raw.slice(1, -1);
  }
  if (raw.startsWith("[") && raw.endsWith("]")) {
    const inner = raw.slice(1, -1).trim();
    if (inner === "") return [];
    return inner
      .split(",")
      .map((part) => part.trim())
      .filter((part) => part.length > 0)
      .map((part) =>
        part.startsWith('"') && part.endsWith('"') ? part.slice(1, -1) : part,
      );
  }
  if (/^-?\d+$/.test(raw)) {
    return Number.parseInt(raw, 10);
  }
  return raw;
}

function stringValue(obj: Record<string, unknown>, key: string): string | undefined {
  const value = obj[key];
  return typeof value === "string" ? value : undefined;
}

function numberValue(obj: Record<string, unknown>, key: string): number | undefined {
  const value = obj[key];
  return typeof value === "number" ? value : undefined;
}

function arrayValue(obj: Record<string, unknown>, key: string): string[] | undefined {
  const value = obj[key];
  return Array.isArray(value) ? (value as string[]) : undefined;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
