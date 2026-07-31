import {
  ContainerNotFoundError,
  DEFAULT_RESOURCE_LIMITS,
  type BackendDriver,
  type BackendEvent,
  type ContainerInfo,
  type ContainerSpec,
} from "./driver.js";

/**
 * In-memory BackendDriver used by unit tests.
 *
 * It models the container lifecycle faithfully enough for orchestration and
 * supervision tests (create/start/stop/remove/inspect, an event stream, and a
 * network set) while recording every mutating call so tests can assert the
 * exact sequence of driver operations a flow produced. No docker required.
 */
export class FakeDriver implements BackendDriver {
  readonly name: string;

  /** Ordered record of mutating driver calls, e.g. "create:repo", "start:id". */
  readonly calls: string[] = [];

  /** Events emitted via emit(); forwarded to all subscribers. */
  private readonly subscribers = new Set<(event: BackendEvent) => void>();
  private readonly containers = new Map<string, ContainerInfo>();
  private readonly networks = new Set<string>();
  private nextId = 1;

  /** When set, create() rejects with this error (to exercise rollback paths). */
  failOnCreate: Error | null = null;
  /** When set, start() rejects with this error. */
  failOnStart: Error | null = null;

  constructor(name = "docker") {
    this.name = name;
  }

  async list(): Promise<ContainerInfo[]> {
    return [...this.containers.values()];
  }

  async create(spec: ContainerSpec): Promise<string> {
    this.calls.push(`create:${spec.name}`);
    if (this.failOnCreate) throw this.failOnCreate;
    const id = `fake-${this.nextId++}`;
    const resources = { ...DEFAULT_RESOURCE_LIMITS, ...(spec.resources ?? {}) };
    this.containers.set(id, {
      id,
      name: spec.name,
      image: spec.image,
      state: "created",
      running: false,
      labels: { ...spec.labels },
      host: null,
      port: null,
    });
    // Stash resolved resources on the labels for assertion convenience.
    this.containers.get(id)!.labels["__cpus"] = String(resources.cpus);
    this.containers.get(id)!.labels["__memoryBytes"] = String(resources.memoryBytes);
    return id;
  }

  async start(id: string): Promise<void> {
    this.calls.push(`start:${id}`);
    if (this.failOnStart) throw this.failOnStart;
    const info = this.require(id);
    info.state = "running";
    info.running = true;
    info.host = "127.0.0.1";
    info.port = 30000 + Number.parseInt(id.replace("fake-", ""), 10);
    this.emit({ action: "start", containerId: id, time: new Date().toISOString(), attributes: {} });
  }

  async stop(id: string): Promise<void> {
    this.calls.push(`stop:${id}`);
    const info = this.require(id);
    info.state = "exited";
    info.running = false;
    this.emit({ action: "die", containerId: id, time: new Date().toISOString(), exitCode: 0, attributes: {} });
  }

  async remove(id: string): Promise<void> {
    this.calls.push(`remove:${id}`);
    this.containers.delete(id);
    this.emit({ action: "destroy", containerId: id, time: new Date().toISOString(), attributes: {} });
  }

  async inspect(id: string): Promise<ContainerInfo> {
    return { ...this.require(id) };
  }

  events(onEvent: (event: BackendEvent) => void): { close(): void } {
    this.subscribers.add(onEvent);
    return { close: () => this.subscribers.delete(onEvent) };
  }

  async ensureNetwork(name: string): Promise<void> {
    this.calls.push(`ensureNetwork:${name}`);
    this.networks.add(name);
  }

  /** Test helper: push a backend event to all subscribers. */
  emit(event: BackendEvent): void {
    for (const subscriber of this.subscribers) subscriber(event);
  }

  /** Test helper: force a container's state (e.g. simulate a crash). */
  setState(id: string, state: string): void {
    const info = this.require(id);
    info.state = state;
    info.running = state === "running";
  }

  hasNetwork(name: string): boolean {
    return this.networks.has(name);
  }

  private require(id: string): ContainerInfo {
    const info = this.containers.get(id);
    if (!info) throw new ContainerNotFoundError(id);
    return info;
  }
}
