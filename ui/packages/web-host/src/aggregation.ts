/**
 * Aggregation assembly (W4, #45): wire the pieces built in W4.2–W4.6 into one
 * running whole — the connection manager, aggregator hub, control-plane router
 * and degradation policy, all over the shared registry.
 *
 * `createAggregation` is the single place that knows how the parts connect, so
 * both the standalone web-host process (index.ts) and the docker E2E test
 * build the same topology:
 *
 *  - every registry row currently `running` with a live endpoint is tracked;
 *  - a container's frames flow connection manager → hub → /ui/events;
 *  - connection drop/recover drives the degradation policy (offline/ready);
 *  - `notifyRunning` is called when a repo becomes ready (e.g. after onboard)
 *    to start tracking it, and `notifyDestroyed` when it is torn down.
 */

import { ContainerConnectionManager } from "./connections.js";
import { ControlPlaneRouter } from "./controlplane.js";
import { CursorStore } from "./cursors.js";
import { EventHub } from "./hub.js";
import { DegradationPolicy } from "./lifecycle.js";
import type { RepositoryRegistry, RepositoryRow } from "./registry.js";

export interface Aggregation {
  hub: EventHub;
  connectionManager: ContainerConnectionManager;
  controlPlane: ControlPlaneRouter;
  policy: DegradationPolicy;
  /** Start tracking a repo that just became running (call after onboard). */
  notifyRunning(repository: string): void;
  /** Stop tracking + mark destroyed (call from the backend destroy event). */
  notifyDestroyed(repository: string): void;
  /** Stop all connections. */
  stop(): Promise<void>;
}

export interface AggregationOptions {
  /** Override the cursor store (defaults to one beside the registry file). */
  cursors?: CursorStore;
  offlineAfterFailures?: number;
}

export function createAggregation(
  registry: RepositoryRegistry,
  options: AggregationOptions = {},
): Aggregation {
  const cursors = options.cursors ?? CursorStore.inMemory();
  const hub = new EventHub();

  const policy = new DegradationPolicy(registry, {
    offlineAfterFailures: options.offlineAfterFailures,
    emit: (repository, status, extra) =>
      hub.ingest(repository, {
        id: 0,
        event: "repo.health",
        data: JSON.stringify({
          v: 1,
          type: "repo.health",
          ts: new Date().toISOString(),
          repository,
          payload: { status, ...(extra ?? {}) },
        }),
      }),
  });

  const connectionManager = new ContainerConnectionManager(cursors, {
    onEvent: (repository, frame) => hub.ingest(repository, frame),
    onDisconnect: (repository, failures) => policy.onDisconnect(repository, failures),
    onConnect: (repository) => policy.onConnect(repository),
  });

  const controlPlane = new ControlPlaneRouter(registry);

  function trackRow(row: RepositoryRow): void {
    if (row.status !== "running") return;
    if (!row.host || !row.port || !row.apiToken) return;
    connectionManager.track({
      repository: row.repository,
      host: row.host,
      port: row.port,
      token: row.apiToken,
    });
  }

  function notifyRunning(repository: string): void {
    const row = registry.get(repository);
    if (row) trackRow(row);
  }

  function notifyDestroyed(repository: string): void {
    connectionManager.untrack(repository);
    policy.onDestroyed(repository);
  }

  // Track everything already running when the layer starts.
  for (const row of registry.list()) {
    trackRow(row);
  }

  return {
    hub,
    connectionManager,
    controlPlane,
    policy,
    notifyRunning,
    notifyDestroyed,
    stop: () => connectionManager.stop(),
  };
}
