import Docker from "dockerode";
import { beforeAll, describe, expect, it } from "vitest";
import { DockerDriver } from "../src/docker-driver.js";

/**
 * Integration seam: the real DockerDriver against a live daemon.
 *
 * Gated on daemon reachability — when no docker/Podman daemon answers (e.g. a
 * dev box without one, or CI before docker is up) the whole suite skips, so it
 * never fails spuriously. Set DOCKER_HOST to target a remote/rootless daemon.
 *
 * Uses the smallest available image and only runs when explicitly reachable.
 */

async function daemonReachable(): Promise<boolean> {
  try {
    const docker = new Docker();
    await docker.ping();
    return true;
  } catch {
    return false;
  }
}

let reachable = false;

beforeAll(async () => {
  reachable = await daemonReachable();
  if (reachable) {
    // The container-lifecycle test uses busybox; ensure it's present so the
    // test doesn't 404 on a daemon that hasn't pulled it yet (CI runners).
    const docker = new Docker();
    try {
      await docker.getImage("busybox:latest").inspect();
    } catch {
      await new Promise<void>((resolvePull, rejectPull) => {
        docker.pull("busybox:latest", (err: Error | null, stream: unknown) => {
          if (err) return rejectPull(err);
          docker.modem.followProgress(stream as NodeJS.ReadableStream, (e: Error | null) =>
            e ? rejectPull(e) : resolvePull(),
          );
        });
      });
    }
  }
}, 120000);

describe("DockerDriver (docker-required)", () => {
  it("pings the daemon", async () => {
    if (!reachable) {
      // Documented skip: no daemon available in this environment.
      expect(reachable).toBe(false);
      return;
    }
    const docker = new Docker();
    const pong = await docker.ping();
    expect(String(pong)).toMatch(/OK/i);
  });

  it("creates, starts, inspects, and removes a container with resource limits", async () => {
    if (!reachable) {
      expect(reachable).toBe(false);
      return;
    }
    const driver = new DockerDriver();
    const name = `webhost-it-${process.pid}`;
    const id = await driver.create({
      image: "busybox:latest",
      name,
      env: { FOO: "bar" },
      labels: { repository: "owner/repo" },
      exposedPort: 7788,
      resources: { cpus: 1, memoryBytes: 64 * 1024 * 1024 },
    });
    try {
      await driver.start(id);
      const info = await driver.inspect(id);
      expect(info.name).toBe(name);
      expect(info.labels["factory.managed"]).toBe("true");
      expect(info.labels["repository"]).toBe("owner/repo");
    } finally {
      await driver.remove(id);
    }
  }, 60000);

  it("ensureNetwork creates a bridge network idempotently", async () => {
    if (!reachable) {
      expect(reachable).toBe(false);
      return;
    }
    const driver = new DockerDriver();
    const network = `webhost-it-net-${process.pid}`;
    await driver.ensureNetwork(network);
    await driver.ensureNetwork(network); // idempotent, no throw
    const docker = new Docker();
    const found = await docker.listNetworks({ filters: { name: [network] } });
    expect(found.some((n) => n.Name === network)).toBe(true);
    await docker.getNetwork(found[0]!.Id).remove();
  }, 60000);
});
