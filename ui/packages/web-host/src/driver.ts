/**
 * BackendDriver: the pluggable seam between web-host orchestration and a
 * container execution backend.
 *
 * W3's docker implementation (dockerode) is the only concrete driver; the
 * interface is the contract that microVM and remote backends will implement
 * later. Onboarding (W3.3), supervision (W3.4) and the aggregation layer (W4)
 * program against this interface, never against dockerode directly, so a
 * backend can be swapped without touching orchestration logic.
 *
 * Semantics notes:
 *  - `create` allocates + configures but does not start; `start` transitions to
 *    running. This mirrors docker's create/start split and lets onboarding
 *    label and inspect before boot.
 *  - `events` is a backend-wide lifecycle stream (die/start/destroy) used by
 *    supervision for crash detection and idle bookkeeping.
 *  - All methods reject on transport/daemon errors; "not found" on a targeted
 *    operation rejects with `ContainerNotFoundError` so callers can distinguish
 *    a vanished container from a daemon failure.
 */

export interface ResourceLimits {
  /** CPU cores (maps to docker NanoCpus). Default 4. */
  cpus: number;
  /** Memory in bytes (maps to docker Memory). Default 8 GiB. */
  memoryBytes: number;
}

export const DEFAULT_RESOURCE_LIMITS: ResourceLimits = {
  cpus: 4,
  memoryBytes: 8 * 1024 * 1024 * 1024,
};

export interface ContainerSpec {
  /** Image reference, e.g. `factory-core:codex`. */
  image: string;
  /** Container name (unique per repository). */
  name: string;
  /** Environment variables (credential injection is simplified to env in W3). */
  env: Record<string, string>;
  /** Labels for discovery (`factory.managed`, repository, provider, token hash). */
  labels: Record<string, string>;
  /** Container port to publish; host port chosen by the driver (0 = random). */
  exposedPort: number;
  /** Resource limits (defaults applied when omitted). */
  resources?: Partial<ResourceLimits>;
  /** Docker network to attach (egress allow-list skeleton, W3.3). */
  network?: string;
}

export interface ContainerInfo {
  id: string;
  name: string;
  image: string;
  /** Backend-reported state: created/running/exited/dead/... */
  state: string;
  /** True when state === running. */
  running: boolean;
  labels: Record<string, string>;
  /** Host address and published port for reaching the container's API. */
  host: string | null;
  port: number | null;
}

/** A single lifecycle event from the backend. */
export interface BackendEvent {
  /** Event action, e.g. "start" | "die" | "destroy" | "stop". */
  action: string;
  /** Container id the event concerns. */
  containerId: string;
  /** ISO-8601 timestamp. */
  time: string;
  /** Exit code for die/destroy events, when known. */
  exitCode?: number;
  /** Raw backend attributes (labels etc.) for correlation. */
  attributes: Record<string, string>;
}

export class ContainerNotFoundError extends Error {
  constructor(id: string) {
    super(`container not found: ${id}`);
    this.name = "ContainerNotFoundError";
  }
}

export interface BackendDriver {
  /** Driver name, e.g. "docker". Used for per-backend limits and registry. */
  readonly name: string;

  /** List containers managed by Factory (label `factory.managed=true`). */
  list(): Promise<ContainerInfo[]>;

  /** Create (but do not start) a container; returns its id. */
  create(spec: ContainerSpec): Promise<string>;

  /** Start a created/stopped container. */
  start(id: string): Promise<void>;

  /** Stop a running container (graceful, then kill after driver timeout). */
  stop(id: string): Promise<void>;

  /** Remove a container (must be stopped; force-removes if still running). */
  remove(id: string): Promise<void>;

  /** Inspect a container; rejects with ContainerNotFoundError when absent. */
  inspect(id: string): Promise<ContainerInfo>;

  /**
   * Subscribe to the backend lifecycle event stream. The returned handle's
   * `close` unsubscribes. Used by supervision (W3.4) for crash/offline
   * detection; may be a thin wrapper over the docker events endpoint.
   */
  events(onEvent: (event: BackendEvent) => void): { close(): void };

  /** Ensure a named network exists (egress allow-list skeleton). */
  ensureNetwork(name: string): Promise<void>;
}
