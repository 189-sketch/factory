import Docker from "dockerode";
import {
  ContainerNotFoundError,
  DEFAULT_RESOURCE_LIMITS,
  type BackendDriver,
  type BackendEvent,
  type ContainerInfo,
  type ContainerSpec,
  type ResourceLimits,
} from "./driver.js";

/**
 * Docker / Podman implementation of BackendDriver, built on dockerode.
 *
 * Connection resolution (dockerode already honours these):
 *  - `DOCKER_HOST=tcp://host:port` for a remote daemon.
 *  - Default local socket (`/var/run/docker.sock`, or the rootless Podman
 *    socket path when `DOCKER_HOST` points at it) — rootless Podman exposes a
 *    docker-compatible API, so no special-casing is needed beyond the socket.
 *  - On Windows, the named pipe `//./pipe/docker_engine`.
 *
 * The driver is stateless; every call goes straight to the daemon. A fixed
 * label `factory.managed=true` scopes `list()` to Factory-owned containers.
 */

const MANAGED_LABEL = "factory.managed";

export interface DockerDriverOptions {
  /** Passed through to dockerode (socketPath / host+port / protocol). */
  dockerode?: Docker.DockerOptions;
  /** Resource defaults applied when a spec omits them. */
  defaultResources?: ResourceLimits;
}

export class DockerDriver implements BackendDriver {
  readonly name = "docker";

  private readonly docker: Docker;
  private readonly defaultResources: ResourceLimits;

  constructor(options: DockerDriverOptions = {}) {
    this.docker = new Docker(options.dockerode);
    this.defaultResources = options.defaultResources ?? DEFAULT_RESOURCE_LIMITS;
  }

  async list(): Promise<ContainerInfo[]> {
    const containers = await this.docker.listContainers({
      all: true,
      filters: { label: [`${MANAGED_LABEL}=true`] },
    });
    return Promise.all(containers.map((c) => this.inspect(c.Id)));
  }

  async create(spec: ContainerSpec): Promise<string> {
    const resources: ResourceLimits = {
      ...this.defaultResources,
      ...(spec.resources ?? {}),
    };
    const env = Object.entries(spec.env).map(([key, value]) => `${key}=${value}`);
    const container = await this.docker.createContainer({
      Image: spec.image,
      name: spec.name,
      Env: env,
      Labels: { ...spec.labels, [MANAGED_LABEL]: "true" },
      ExposedPorts: { [`${spec.exposedPort}/tcp`]: {} },
      HostConfig: {
        // 0 lets the daemon pick a free host port; read back via inspect.
        PortBindings: { [`${spec.exposedPort}/tcp`]: [{ HostPort: "0" }] },
        NanoCpus: Math.round(resources.cpus * 1e9),
        Memory: resources.memoryBytes,
        ...(spec.network ? { NetworkMode: spec.network } : {}),
      },
      ...(spec.network ? { NetworkingConfig: { EndpointsConfig: { [spec.network]: {} } } } : {}),
    });
    return container.id;
  }

  async start(id: string): Promise<void> {
    await this.docker.getContainer(id).start();
  }

  async stop(id: string): Promise<void> {
    await this.docker.getContainer(id).stop();
  }

  async remove(id: string): Promise<void> {
    await this.docker.getContainer(id).remove({ force: true });
  }

  async inspect(id: string): Promise<ContainerInfo> {
    let data: Docker.ContainerInspectInfo;
    try {
      data = await this.docker.getContainer(id).inspect();
    } catch (error) {
      if (isNotFound(error)) {
        throw new ContainerNotFoundError(id);
      }
      throw error;
    }
    return toInfo(data);
  }

  events(onEvent: (event: BackendEvent) => void): { close(): void } {
    let closed = false;
    let stream: NodeJS.ReadableStream | undefined;
    const filters = { label: [`${MANAGED_LABEL}=true`] };

    this.docker
      .getEvents({ filters })
      .then((s) => {
        if (closed) {
          destroy(s);
          return;
        }
        stream = s as NodeJS.ReadableStream;
        let buffer = "";
        stream.on("data", (chunk: Buffer | string) => {
          buffer += chunk.toString();
          let newline: number;
          while ((newline = buffer.indexOf("\n")) !== -1) {
            const line = buffer.slice(0, newline).trim();
            buffer = buffer.slice(newline + 1);
            if (line.length === 0) continue;
            const parsed = safeParseEvent(line);
            if (parsed) onEvent(parsed);
          }
        });
        stream.on("error", () => {
          /* transport errors surface via supervision's reconnect loop */
        });
      })
      .catch(() => {
        /* daemon unreachable; supervision reconnects */
      });

    return {
      close: () => {
        closed = true;
        if (stream) destroy(stream);
      },
    };
  }

  async ensureNetwork(name: string): Promise<void> {
    const existing = await this.docker.listNetworks({ filters: { name: [name] } });
    if (existing.some((n) => n.Name === name)) {
      return;
    }
    await this.docker.createNetwork({ Name: name, Driver: "bridge" });
  }
}

function toInfo(data: Docker.ContainerInspectInfo): ContainerInfo {
  const ports = data.NetworkSettings?.Ports ?? {};
  let host: string | null = null;
  let port: number | null = null;
  for (const bindings of Object.values(ports)) {
    const first = bindings?.[0];
    if (first?.HostPort) {
      host = first.HostIp && first.HostIp !== "0.0.0.0" ? first.HostIp : "127.0.0.1";
      port = Number.parseInt(first.HostPort, 10);
      break;
    }
  }
  const state = data.State?.Status ?? "unknown";
  return {
    id: data.Id ?? "",
    name: (data.Name ?? "").replace(/^\//, ""),
    image: data.Config?.Image ?? "",
    state,
    running: state === "running",
    labels: data.Config?.Labels ?? {},
    host,
    port,
  };
}

function safeParseEvent(line: string): BackendEvent | null {
  try {
    const raw = JSON.parse(line) as {
      Action?: string;
      status?: string;
      id?: string;
      timeNano?: number;
      time?: number;
      Actor?: { ID?: string; Attributes?: Record<string, string> & { exitCode?: string } };
    };
    const action = raw.Action ?? raw.status ?? "";
    const containerId = raw.Actor?.ID ?? raw.id ?? "";
    if (!containerId) return null;
    const timeMs = raw.timeNano ? Math.floor(raw.timeNano / 1e6) : (raw.time ?? 0) * 1000;
    const exitCodeRaw = raw.Actor?.Attributes?.exitCode;
    return {
      action,
      containerId,
      time: timeMs ? new Date(timeMs).toISOString() : new Date().toISOString(),
      exitCode: exitCodeRaw !== undefined ? Number.parseInt(exitCodeRaw, 10) : undefined,
      attributes: (raw.Actor?.Attributes as Record<string, string>) ?? {},
    };
  } catch {
    return null;
  }
}

function isNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "statusCode" in error &&
    (error as { statusCode?: number }).statusCode === 404
  );
}

function destroy(stream: NodeJS.ReadableStream): void {
  const maybeDestroy = stream as NodeJS.ReadableStream & { destroy?: () => void };
  if (typeof maybeDestroy.destroy === "function") {
    maybeDestroy.destroy();
  }
}
