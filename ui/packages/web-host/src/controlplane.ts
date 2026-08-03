/**
 * Control-plane router (W4.5, #43): forward `/ui/api/{repository}/*` to the
 * right container's core `/api/v1/*`, keyed by repository.
 *
 * The renderer never learns a container's address or token — it addresses
 * every operation by `owner/repo`; the router looks up the registry row for
 * the (host, port, apiToken) endpoint and forwards with the container's bearer
 * token. Results stream back verbatim.
 *
 * Writes are single-flighted per (repository, method, path): a double-click /
 * retry / concurrent cancel collapses into one in-flight upstream request, and
 * the shared result is returned to every waiter. The caller's
 * `client_request_id` is passed through untouched — the core already makes a
 * repeated client_request_id idempotent, so retries are safe end to end.
 *
 * `api_token` is never logged or returned; forwarding failures surface as the
 * standard `{error:{code,message}}` envelope.
 */

import type { RepositoryRegistry } from "./registry.js";

export interface ControlPlaneOptions {
  /** Injectable fetch (test seam / determinism). */
  fetchFn?: typeof fetch;
  /** Timeout for a single upstream call, ms. Default 15000. */
  timeoutMs?: number;
}

export interface ForwardResult {
  status: number;
  body: unknown;
}

const DEFAULT_TIMEOUT_MS = 15_000;

export class ControlPlaneRouter {
  private readonly registry: RepositoryRegistry;
  private readonly fetchFn: typeof fetch;
  private readonly timeoutMs: number;
  /** In-flight write requests, keyed for single-flight dedup. */
  private readonly inflight = new Map<string, Promise<ForwardResult>>();

  constructor(registry: RepositoryRegistry, options: ControlPlaneOptions = {}) {
    this.registry = registry;
    this.fetchFn = options.fetchFn ?? fetch;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  }

  /**
   * Forward a request to the repository's container. `subpath` is the core
   * path after the version prefix, e.g. `/tasks` or `/runs/3/cancel`.
   * `write: true` enables single-flight dedup for the request.
   */
  async forward(
    repository: string,
    subpath: string,
    init: { method?: string; body?: unknown; write?: boolean } = {},
  ): Promise<ForwardResult> {
    const method = (init.method ?? "GET").toUpperCase();
    const isWrite = init.write ?? method !== "GET";
    const key = `${repository} ${method} ${subpath}`;

    if (isWrite) {
      const existing = this.inflight.get(key);
      if (existing) return existing;
      const pending = this.doForward(repository, subpath, method, init.body).finally(() => {
        this.inflight.delete(key);
      });
      this.inflight.set(key, pending);
      return pending;
    }
    return this.doForward(repository, subpath, method, init.body);
  }

  private async doForward(
    repository: string,
    subpath: string,
    method: string,
    body: unknown,
  ): Promise<ForwardResult> {
    const row = this.registry.get(repository);
    if (!row) {
      return { status: 404, body: errorBody("not_found", "repository not registered") };
    }
    if (!row.host || !row.port || !row.apiToken) {
      return {
        status: 409,
        body: errorBody("no_endpoint", `repository ${row.repository} has no live container endpoint`),
      };
    }
    const url = `http://${row.host}:${row.port}/api/v1${subpath}`;
    try {
      const res = await this.fetchFn(url, {
        method,
        headers: {
          Authorization: `Bearer ${row.apiToken}`,
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: AbortSignal.timeout(this.timeoutMs),
      });
      const text = await res.text();
      let parsed: unknown = null;
      try {
        parsed = text ? JSON.parse(text) : null;
      } catch {
        parsed = text;
      }
      return { status: res.status, body: parsed };
    } catch (error) {
      return {
        status: 502,
        body: errorBody(
          "upstream_unreachable",
          `failed to reach container for ${row.repository}: ${sanitize(error)}`,
        ),
      };
    }
  }
}

function errorBody(code: string, message: string): { error: { code: string; message: string } } {
  return { error: { code, message } };
}

/** Bound and scrub an upstream error so a token/host detail never leaks. */
function sanitize(error: unknown): string {
  const raw = error instanceof Error ? error.message : String(error);
  return raw
    .replace(/(https?:\/\/)[^/\s:@]+:[^/\s@]+@/g, "$1***@")
    .replace(/([?&](?:token|access_token|api[_-]?key|password)=)[^&\s]+/gi, "$1***")
    .slice(0, 300);
}
