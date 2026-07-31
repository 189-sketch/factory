import { describe, expect, it } from "vitest";
import { ContainerNotFoundError, DEFAULT_RESOURCE_LIMITS } from "../src/driver.js";
import { FakeDriver } from "../src/fake-driver.js";

// Unit seam: the BackendDriver contract exercised through FakeDriver. These
// tests pin the lifecycle semantics orchestration relies on (create does not
// start; start publishes a port; remove deletes; missing ids raise
// ContainerNotFoundError) and the call-sequence recording used by W3.3+ tests.
describe("FakeDriver (BackendDriver contract)", () => {
  it("creates without starting; start publishes host/port", async () => {
    const driver = new FakeDriver();
    const id = await driver.create({
      image: "factory-core:codex",
      name: "repo",
      env: { FACTORY_GIT_URL: "https://github.com/owner/repo" },
      labels: { repository: "owner/repo" },
      exposedPort: 7788,
    });
    let info = await driver.inspect(id);
    expect(info.running).toBe(false);
    expect(info.port).toBeNull();

    await driver.start(id);
    info = await driver.inspect(id);
    expect(info.running).toBe(true);
    expect(info.host).toBe("127.0.0.1");
    expect(info.port).toBeGreaterThan(0);
  });

  it("applies default resource limits and honours overrides", async () => {
    const driver = new FakeDriver();
    const idDefault = await driver.create({
      image: "img",
      name: "a",
      env: {},
      labels: {},
      exposedPort: 7788,
    });
    let info = await driver.inspect(idDefault);
    expect(info.labels["__cpus"]).toBe(String(DEFAULT_RESOURCE_LIMITS.cpus));
    expect(info.labels["__memoryBytes"]).toBe(String(DEFAULT_RESOURCE_LIMITS.memoryBytes));

    const idOverride = await driver.create({
      image: "img",
      name: "b",
      env: {},
      labels: {},
      exposedPort: 7788,
      resources: { cpus: 2 },
    });
    info = await driver.inspect(idOverride);
    expect(info.labels["__cpus"]).toBe("2");
  });

  it("records the mutating call sequence", async () => {
    const driver = new FakeDriver();
    const id = await driver.create({
      image: "img",
      name: "repo",
      env: {},
      labels: {},
      exposedPort: 7788,
    });
    await driver.start(id);
    await driver.stop(id);
    await driver.remove(id);
    expect(driver.calls).toEqual([
      "create:repo",
      `start:${id}`,
      `stop:${id}`,
      `remove:${id}`,
    ]);
  });

  it("remove deletes the container; inspect then raises not-found", async () => {
    const driver = new FakeDriver();
    const id = await driver.create({
      image: "img",
      name: "repo",
      env: {},
      labels: {},
      exposedPort: 7788,
    });
    await driver.remove(id);
    await expect(driver.inspect(id)).rejects.toBeInstanceOf(ContainerNotFoundError);
  });

  it("emits lifecycle events to subscribers", async () => {
    const driver = new FakeDriver();
    const seen: string[] = [];
    const sub = driver.events((event) => seen.push(event.action));
    const id = await driver.create({
      image: "img",
      name: "repo",
      env: {},
      labels: {},
      exposedPort: 7788,
    });
    await driver.start(id);
    await driver.stop(id);
    await driver.remove(id);
    expect(seen).toEqual(["start", "die", "destroy"]);
    sub.close();
  });

  it("ensureNetwork is idempotent and recorded", async () => {
    const driver = new FakeDriver();
    await driver.ensureNetwork("factory-egress");
    await driver.ensureNetwork("factory-egress");
    expect(driver.hasNetwork("factory-egress")).toBe(true);
    expect(driver.calls.filter((c) => c === "ensureNetwork:factory-egress")).toHaveLength(2);
  });

  it("failOnCreate makes create reject (for rollback tests)", async () => {
    const driver = new FakeDriver();
    driver.failOnCreate = new Error("image not found");
    await expect(
      driver.create({ image: "img", name: "x", env: {}, labels: {}, exposedPort: 1 }),
    ).rejects.toThrow("image not found");
  });
});
