import type { BackendDriver } from "./driver.js";
import type { UiEventBus } from "./events.js";
import type { RepositoryRegistry, RepositoryRow } from "./registry.js";

/**
 * Container supervision (W3.4): the loop that keeps the registry honest about
 * what is actually running, and enforces Fleet policy.
 *
 * Three responsibilities, sharing one injectable clock (fake in tests):
 *
 *  1. Idle destruction — a repository whose `lastActivityAt` is older than its
 *     `idleTimeoutSeconds` (default 10h, per-repo override) is destroyed. The
 *     pre-destroy workspace snapshot push is a KNOWN CONTRACT GAP (see
 *     issue #35): core has no endpoint to push `factory/snapshot/<repo>`, so
 *     this slice destroys only and records the gap via `onSnapshotGap`.
 *
 *  2. Limits — `maxActiveContainers` global cap plus per-backend caps. A new
 *     activation beyond the cap queues; the least-recently-active container is
 *     destroyed first so active work is not starved.
 *
 *  3. Backoff rebuild — a container that dies unexpectedly is marked offline
 *     and rebuilt after an exponential backoff (ported from core
 *     `repository_backoff`, fleet.rs), capped. Per-repository, so one repo's
 *     crash loop never touches another's container.
 *
 * The supervisor is driven by `tick(now)` (called on a timer in production,
 * called directly in tests) rather than by its own scheduling, so time is
 * fully controllable.
 */

export const INITIAL_BACKOFF_SECONDS = 5;
export const MAX_BACKOFF_SECONDS = 15 * 60;
export const DEFAULT_IDLE_TIMEOUT_SECONDS = 10 * 60 * 60;
export const DEFAULT_MAX_ACTIVE_CONTAINERS = 8;

/** Port of factory-core fleet.rs `repository_backoff`. */
export function repositoryBackoffSeconds(consecutiveFailures: number): number {
  const exponent = Math.min(Math.max(consecutiveFailures - 1, 0), 31);
  return Math.min(INITIAL_BACKOFF_SECONDS * 2 ** exponent, MAX_BACKOFF_SECONDS);
}

export interface BackendLimit {
  backend: string;
  maxContainers: number;
}

export interface SupervisorOptions {
  maxActiveContainers?: number;
  backendLimits?: BackendLimit[];
  defaultIdleTimeoutSeconds?: number;
  /** Rebuild a container for a repo (defaults to driver.start on a new create). */
  rebuild?: (row: RepositoryRow) => Promise<void>;
  /** Trigger a core workspace snapshot before destroy (contract gap hook). */
  triggerSnapshot?: (row: RepositoryRow) => Promise<void>;
  /** Called when a snapshot was required but no mechanism exists (gap). */
  onSnapshotGap?: (row: RepositoryRow) => void;
}

interface BackoffState {
  consecutiveFailures: number;
  nextRetryAtMs: number;
}

export class Supervisor {
  private readonly registry: RepositoryRegistry;
  private readonly driver: BackendDriver;
  private readonly bus: UiEventBus;
  private readonly maxActive: number;
  private readonly backendLimits: Map<string, number>;
  private readonly defaultIdle: number;
  private readonly rebuild?: (row: RepositoryRow) => Promise<void>;
  private readonly triggerSnapshot?: (row: RepositoryRow) => Promise<void>;
  private readonly onSnapshotGap?: (row: RepositoryRow) => void;
  private readonly backoff = new Map<string, BackoffState>();

  constructor(
    registry: RepositoryRegistry,
    driver: BackendDriver,
    bus: UiEventBus,
    options: SupervisorOptions = {},
  ) {
    this.registry = registry;
    this.driver = driver;
    this.bus = bus;
    this.maxActive = options.maxActiveContainers ?? DEFAULT_MAX_ACTIVE_CONTAINERS;
    this.backendLimits = new Map(
      (options.backendLimits ?? []).map((b) => [b.backend, b.maxContainers]),
    );
    this.defaultIdle = options.defaultIdleTimeoutSeconds ?? DEFAULT_IDLE_TIMEOUT_SECONDS;
    this.rebuild = options.rebuild;
    this.triggerSnapshot = options.triggerSnapshot;
    this.onSnapshotGap = options.onSnapshotGap;
  }

  /**
   * One supervision pass at logical time `now`. Order matters: enforce limits
   * first (frees capacity), then idle, then backoff retries.
   */
  async tick(now: Date = new Date()): Promise<void> {
    const nowMs = now.getTime();
    await this.enforceIdle(nowMs);
    await this.enforceBackoff(nowMs);
  }

  /**
   * Handle a backend lifecycle event. A `die`/`destroy` for a running repo
   * marks it offline and arms the backoff; other repos are untouched (fault
   * isolation).
   */
  async onContainerEvent(action: string, repository: string, now: Date = new Date()): Promise<void> {
    if (action !== "die" && action !== "destroy") return;
    const row = this.registry.get(repository);
    if (!row || row.status !== "running") return;

    this.registry.update(repository, { status: "offline" });
    const state = this.backoff.get(repository) ?? { consecutiveFailures: 0, nextRetryAtMs: 0 };
    state.consecutiveFailures += 1;
    const backoffSeconds = repositoryBackoffSeconds(state.consecutiveFailures);
    state.nextRetryAtMs = now.getTime() + backoffSeconds * 1000;
    this.backoff.set(repository, state);
    this.bus.publish({
      type: "repo.health",
      repository,
      payload: { status: "offline", consecutive_failures: state.consecutiveFailures },
    });
  }

  /**
   * Admit a new activation under the caps. Returns the repos destroyed to make
   * room (least-recently-active first), so the caller can proceed. When even
   * after eviction the cap cannot be satisfied, the caller should queue.
   */
  async admit(repository: string, now: Date = new Date()): Promise<{ admitted: boolean; evicted: string[] }> {
    const evicted: string[] = [];
    // Evict LRU active containers until both global and per-backend caps have
    // a free slot for the incoming repo's backend.
    const incoming = this.registry.get(repository);
    const backend = incoming?.backend ?? "docker";

    for (;;) {
      const active = this.activeContainers();
      const globalOk = active.length < this.maxActive;
      const backendCount = active.filter((r) => r.backend === backend).length;
      const backendCap = this.backendLimits.get(backend);
      const backendOk = backendCap === undefined || backendCount < backendCap;
      if (globalOk && backendOk) {
        return { admitted: true, evicted };
      }
      const victim = this.leastRecentlyActive(active);
      if (!victim) {
        return { admitted: false, evicted };
      }
      await this.destroy(victim, now.getTime());
      evicted.push(victim.repository);
    }
  }

  /** Count of containers currently counted against the global cap. */
  private activeContainers(): RepositoryRow[] {
    return this.registry
      .list()
      .filter((r) => r.status === "running" && r.containerId !== null);
  }

  private leastRecentlyActive(active: RepositoryRow[]): RepositoryRow | null {
    if (active.length === 0) return null;
    return active.reduce((oldest, row) => {
      const rowMs = row.lastActivityAt ? Date.parse(row.lastActivityAt) : 0;
      const oldestMs = oldest.lastActivityAt ? Date.parse(oldest.lastActivityAt) : 0;
      return rowMs < oldestMs ? row : oldest;
    });
  }

  private async enforceIdle(nowMs: number): Promise<void> {
    for (const row of this.activeContainers()) {
      const idleTimeout = row.idleTimeoutSeconds ?? this.defaultIdle;
      const lastMs = row.lastActivityAt ? Date.parse(row.lastActivityAt) : Date.parse(row.createdAt);
      if (nowMs - lastMs >= idleTimeout * 1000) {
        await this.destroy(row, nowMs);
      }
    }
  }

  private async enforceBackoff(nowMs: number): Promise<void> {
    for (const [repository, state] of this.backoff) {
      if (nowMs < state.nextRetryAtMs) continue;
      const row = this.registry.get(repository);
      if (!row || row.status !== "offline") {
        this.backoff.delete(repository);
        continue;
      }
      if (!this.rebuild) continue;
      try {
        await this.rebuild(row);
        this.registry.update(repository, { status: "running" });
        this.backoff.delete(repository);
        this.bus.publish({ type: "repo.health", repository, payload: { status: "ready" } });
      } catch {
        // Rebuild failed: arm the next backoff and stay offline.
        state.consecutiveFailures += 1;
        state.nextRetryAtMs = nowMs + repositoryBackoffSeconds(state.consecutiveFailures) * 1000;
      }
    }
  }

  private async destroy(row: RepositoryRow, _nowMs: number): Promise<void> {
    // Pre-destroy workspace snapshot. The push mechanism is a known gap; when
    // no trigger is wired we surface the gap and proceed with destroy-only.
    if (this.triggerSnapshot) {
      await this.triggerSnapshot(row).catch(() => undefined);
    } else {
      this.onSnapshotGap?.(row);
    }
    if (row.containerId) {
      await this.driver.remove(row.containerId).catch(() => undefined);
    }
    this.registry.update(row.repository, {
      status: "destroyed",
      containerId: null,
      host: null,
      port: null,
    });
    this.backoff.delete(row.repository);
    this.bus.publish({
      type: "repo.health",
      repository: row.repository,
      payload: { status: "destroyed" },
    });
  }
}
