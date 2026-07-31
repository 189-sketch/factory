import { createApp } from "./app.js";
import { DockerDriver } from "./docker-driver.js";
import { UiEventBus } from "./events.js";
import { OnboardingPipeline } from "./onboard.js";
import { RepositoryRegistry } from "./registry.js";

/**
 * Standalone web-host entrypoint (Web mode).
 *
 * Binds the registry + onboarding HTTP API to a local address. Electron mode
 * embeds this same package in the main process (A4 §6); this file is only the
 * Web-mode process bootstrap and stays minimal.
 *
 * Config via env:
 *   WEB_HOST_DB        SQLite file path for the registry (default ./web-host.db)
 *   WEB_HOST_PORT      listen port (default 4400)
 *   WEB_HOST_ADDRESS   bind address (default 127.0.0.1)
 *   DOCKER_HOST        docker/Podman daemon (dockerode default resolution)
 */

const dbPath = process.env.WEB_HOST_DB ?? "web-host.db";
const port = Number.parseInt(process.env.WEB_HOST_PORT ?? "4400", 10);
const address = process.env.WEB_HOST_ADDRESS ?? "127.0.0.1";

const registry = RepositoryRegistry.open(dbPath);
const driver = new DockerDriver();
const bus = new UiEventBus();
const pipeline = new OnboardingPipeline(registry, driver, bus);
const app = createApp(registry, { pipeline, bus });

const server = app.listen(port, address, () => {
  const bound = server.address();
  const boundPort = typeof bound === "object" && bound ? bound.port : port;
  // eslint-disable-next-line no-console
  console.log(`factory web-host listening on http://${address}:${boundPort} (db: ${dbPath})`);
});

function shutdown(signal: string): void {
  // eslint-disable-next-line no-console
  console.log(`received ${signal}, shutting down`);
  server.close(() => {
    registry.close();
    process.exit(0);
  });
}

process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
