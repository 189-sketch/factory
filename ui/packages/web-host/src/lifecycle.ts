/**
 * Degradation policy (W4.6, #44): turn connection-layer signals into the
 * renderer-facing lifecycle events, so a card greys out promptly instead of
 * silently waiting on a dead container.
 *
 * Wired to the ContainerConnectionManager (W4.2) and the registry:
 *
 *  - A container whose SSE keeps failing (reconnect attempts reach
 *    `offlineAfterFailures`) is marked `offline` in the registry and a
 *    ui-synthesized `repo.health{status:"offline"}` is emitted to the renderer.
 *  - When the container's stream re-establishes, the registry is marked
 *    `running` and `repo.health{status:"ready"}` is emitted (card turns green).
 *  - A destroy (idle / manual, surfaced via the backend event stream) closes
 *    the connection, marks the registry `destroyed`, and emits
 *    `repo.health{status:"destroyed"}`.
 *
 * Degradation is per-repository and isolated: one repo's flapping only affects
 * its own card. The threshold and event sink are injectable for tests.
 */

import type { RepositoryRegistry } from "./registry.js";

export interface DegradationOptions {
  /** Reconnect failures before a repo is marked offline. Default 3. */
  offlineAfterFailures?: number;
  /** Emit a ui-synthesized lifecycle event (the fan-out hub's ingest). */
  emit: (repository: string, status: "offline" | "ready" | "destroyed", extra?: Record<string, unknown>) => void;
}

const DEFAULT_OFFLINE_AFTER_FAILURES = 3;

export class DegradationPolicy {
  private readonly registry: RepositoryRegistry;
  private readonly offlineAfterFailures: number;
  private readonly emit: DegradationOptions["emit"];

  constructor(registry: RepositoryRegistry, options: DegradationOptions) {
    this.registry = registry;
    this.offlineAfterFailures =
      options.offlineAfterFailures ?? DEFAULT_OFFLINE_AFTER_FAILURES;
    this.emit = options.emit;
  }

  /**
   * A connection dropped and a reconnect is scheduled. When failures cross the
   * threshold the repo is degraded to offline (idempotent — only the
   * transition emits an event). Wire to the manager's `onDisconnect`.
   */
  onDisconnect(repository: string, consecutiveFailures: number): void {
    if (consecutiveFailures < this.offlineAfterFailures) return;
    const row = this.registry.get(repository);
    if (!row || row.status === "offline" || row.status === "destroyed") return;
    this.registry.update(repository, { status: "offline" });
    this.emit(repository, "offline", { consecutive_failures: consecutiveFailures });
  }

  /**
   * A connection (re)established. If the repo was offline it is now ready.
   * Wire to the manager's `onConnect`.
   */
  onConnect(repository: string): void {
    const row = this.registry.get(repository);
    if (!row || row.status !== "offline") return;
    this.registry.update(repository, { status: "running" });
    this.emit(repository, "ready");
  }

  /**
   * The container was destroyed (idle timeout or manual). The caller (the
   * supervisor's backend-event path) has already removed it; here we close the
   * book on the renderer. Wire to the backend `destroy` event.
   */
  onDestroyed(repository: string): void {
    const row = this.registry.get(repository);
    if (row && row.status !== "destroyed") {
      this.registry.update(repository, { status: "destroyed" });
    }
    this.emit(repository, "destroyed");
  }
}
