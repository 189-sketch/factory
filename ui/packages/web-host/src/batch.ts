import type { BackendDriver } from "./driver.js";
import { canonicalGithubIdentity } from "./identity.js";
import type { RepositoryRegistry, RepositoryRow } from "./registry.js";

/**
 * Registry management operations (W3.5): batch pause/resume/destroy and
 * fleet.toml import. Pure management-plane logic over the registry + driver —
 * independent of the supervision loop (W3.4).
 */

export type BatchAction = "pause" | "resume" | "destroy";

export interface BatchItemResult {
  repository: string;
  ok: boolean;
  status?: string;
  error?: string;
}

export interface BatchResult {
  action: BatchAction;
  results: BatchItemResult[];
}

/**
 * Apply one action to many repositories, isolating per-repo failures so a
 * single bad entry never aborts the batch. Results are returned in input order.
 */
export async function applyBatch(
  registry: RepositoryRegistry,
  driver: BackendDriver,
  action: BatchAction,
  repositories: string[],
): Promise<BatchResult> {
  const results: BatchItemResult[] = [];
  for (const repo of repositories) {
    results.push(await applyOne(registry, driver, action, repo));
  }
  return { action, results };
}

async function applyOne(
  registry: RepositoryRegistry,
  driver: BackendDriver,
  action: BatchAction,
  input: string,
): Promise<BatchItemResult> {
  let repository: string;
  try {
    repository = canonicalGithubIdentity(input);
  } catch (error) {
    return { repository: input, ok: false, error: message(error) };
  }

  const row = registry.get(repository);
  if (!row) {
    return { repository, ok: false, error: "repository not registered" };
  }

  try {
    switch (action) {
      case "pause": {
        const updated = registry.update(repository, { status: "paused" })!;
        return { repository, ok: true, status: updated.status };
      }
      case "resume": {
        const updated = registry.update(repository, { status: "running" })!;
        return { repository, ok: true, status: updated.status };
      }
      case "destroy": {
        return await destroyOne(registry, driver, row);
      }
    }
  } catch (error) {
    return { repository, ok: false, error: message(error) };
  }
}

async function destroyOne(
  registry: RepositoryRegistry,
  driver: BackendDriver,
  row: RepositoryRow,
): Promise<BatchItemResult> {
  // Best-effort container teardown; a missing container is not an error.
  if (row.containerId) {
    await driver.remove(row.containerId).catch(() => undefined);
  }
  const updated = registry.update(row.repository, {
    status: "destroyed",
    containerId: null,
    host: null,
    port: null,
  })!;
  return { repository: row.repository, ok: true, status: updated.status };
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
