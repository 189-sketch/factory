import { createHash, randomBytes } from "node:crypto";
import type { BackendDriver } from "./driver.js";
import type { UiEventBus } from "./events.js";
import { canonicalGithubIdentity } from "./identity.js";
import type { RepositoryRegistry, RepositoryRow } from "./registry.js";

/**
 * Onboarding pipeline (W3.3): turn a submitted git URL into a running,
 * discoverable container — idempotently, with per-step progress and full
 * rollback on failure.
 *
 * Steps (each recorded to the registry `onboard_step` and published to the
 * ui event bus):
 *   validate  → normalize + validate the form
 *   pull      → create the container (image factory-core:<provider>)
 *   inject    → build env (FACTORY_GIT_URL, random FACTORY_API_TOKEN, creds*)
 *   clone     → start the container (agent-entrypoint clones + inits, W1)
 *   ready     → poll core /api/v1/health until ready
 *
 * *Credential injection is the simplified env path (known downgrade, upgraded
 * to the high-sensitivity tmpfs payload by W5). The api_token only ever leaves
 * this process as a SHA-256 hash prefix on the container label.
 *
 * Idempotency: a second onboard for an already-running repository returns the
 * existing registration without creating a new container.
 */

export const ONBOARD_STEPS = ["validate", "pull", "inject", "clone", "ready"] as const;
export type OnboardStep = (typeof ONBOARD_STEPS)[number];

/** Default egress network (allow-list skeleton; proxy hardening is later). */
export const DEFAULT_NETWORK = "factory-egress";

export interface OnboardInput {
  gitUrl: string;
  provider: string;
  branch?: string | null;
  triggerLabels?: string[];
  idleTimeoutSeconds?: number | null;
  backend?: string;
  credentialRef?: string | null;
}

export type OnboardResult =
  | { ok: true; idempotent: boolean; repository: RepositoryRow }
  | { ok: false; step: OnboardStep; error: string; repository?: RepositoryRow };

export interface OnboardOptions {
  /** Image resolver; defaults to factory-core:<provider>. */
  imageForProvider?: (provider: string) => string;
  /** Egress network name; defaults to factory-egress. */
  network?: string;
  /** Health poller; injectable for tests. Defaults to HTTP GET /api/v1/health. */
  waitForReady?: (host: string, port: number, token: string) => Promise<void>;
  /** Token generator; injectable for determinism in tests. */
  generateToken?: () => string;
}

export class OnboardingPipeline {
  private readonly registry: RepositoryRegistry;
  private readonly driver: BackendDriver;
  private readonly bus: UiEventBus;
  private readonly imageForProvider: (provider: string) => string;
  private readonly network: string;
  private readonly waitForReady: (host: string, port: number, token: string) => Promise<void>;
  private readonly generateToken: () => string;

  constructor(
    registry: RepositoryRegistry,
    driver: BackendDriver,
    bus: UiEventBus,
    options: OnboardOptions = {},
  ) {
    this.registry = registry;
    this.driver = driver;
    this.bus = bus;
    this.imageForProvider =
      options.imageForProvider ?? ((provider) => `factory-core:${provider}`);
    this.network = options.network ?? DEFAULT_NETWORK;
    this.waitForReady = options.waitForReady ?? defaultWaitForReady;
    this.generateToken = options.generateToken ?? (() => randomBytes(32).toString("hex"));
  }

  async onboard(input: OnboardInput): Promise<OnboardResult> {
    // ① validate + normalize
    let repository: string;
    try {
      if (!input.provider) throw new Error("provider is required");
      repository = canonicalGithubIdentity(input.gitUrl);
    } catch (error) {
      return this.fail("validate", error);
    }

    // ② idempotency: already registered and running → return as-is.
    const existing = this.registry.get(repository);
    if (existing && existing.status === "running" && existing.containerId) {
      return { ok: true, idempotent: true, repository: existing };
    }

    const row =
      existing ??
      this.registry.create({
        repository,
        provider: input.provider,
        backend: (input.backend as never) ?? "docker",
        credentialRef: input.credentialRef ?? null,
        idleTimeoutSeconds: input.idleTimeoutSeconds ?? null,
        branch: input.branch ?? null,
        triggerLabels: input.triggerLabels ?? [],
      });

    let containerId: string | null = null;
    try {
      // ③ pull (create) — image per provider, on the egress network.
      this.progress(repository, "pull");
      await this.driver.ensureNetwork(this.network);
      const token = this.generateToken();
      const tokenHash = createHash("sha256").update(token).digest("hex").slice(0, 8);
      const image = this.imageForProvider(input.provider);

      // ④ inject — env payload (simplified; W5 upgrades to tmpfs).
      this.progress(repository, "inject");
      const env: Record<string, string> = {
        FACTORY_GIT_URL: input.gitUrl,
        FACTORY_API_TOKEN: token,
        FACTORY_PROVIDER: input.provider,
      };
      if (input.branch) env.FACTORY_BRANCH = input.branch;

      containerId = await this.driver.create({
        image,
        name: containerName(repository),
        env,
        labels: {
          "factory.managed": "true",
          "factory.repository": repository,
          "factory.provider": input.provider,
          "factory.api_token_hash": tokenHash,
        },
        exposedPort: 7788,
        network: this.network,
      });
      this.registry.update(repository, {
        containerId,
        apiToken: token,
        status: "registering",
      });

      // ⑤ clone (start) — agent-entrypoint clones/inits/serves (W1).
      this.progress(repository, "clone");
      await this.driver.start(containerId);

      // ⑥ ready — poll core health on the published host:port.
      this.progress(repository, "ready");
      const info = await this.driver.inspect(containerId);
      if (!info.host || !info.port) {
        throw new Error("container started without a published host/port");
      }
      await this.waitForReady(info.host, info.port, token);

      const ready = this.registry.update(repository, {
        status: "running",
        host: info.host,
        port: info.port,
        onboardStep: "ready",
        lastActivityAt: new Date().toISOString(),
      })!;
      this.bus.publish({
        type: "onboard.ready",
        repository,
        payload: { host: info.host, port: info.port },
      });
      return { ok: true, idempotent: false, repository: ready };
    } catch (error) {
      // Rollback: destroy the half-built container and clear the row.
      const step = this.registry.get(repository)?.onboardStep ?? "pull";
      if (containerId) {
        await this.driver.remove(containerId).catch(() => undefined);
      }
      this.registry.delete(repository);
      const sanitized = sanitizeError(error);
      this.bus.publish({
        type: "onboard.failed",
        repository,
        payload: { step, error: sanitized },
      });
      return { ok: false, step: (step as OnboardStep) ?? "pull", error: sanitized };
    }
  }

  private progress(repository: string, step: OnboardStep): void {
    this.registry.update(repository, { onboardStep: step });
    this.bus.publish({ type: "onboard.progress", repository, payload: { step } });
  }

  private fail(step: OnboardStep, error: unknown): OnboardResult {
    return { ok: false, step, error: sanitizeError(error) };
  }
}

export function containerName(repository: string): string {
  return `factory-${repository.replace("/", "-")}`;
}

/**
 * Strip anything that looks like an embedded credential (token=..., //user:pass@,
 * long hex/base64 secrets) and bound the message, so a failing step never leaks
 * a secret into the registry, the event stream, or an HTTP response.
 */
export function sanitizeError(error: unknown): string {
  const raw = error instanceof Error ? error.message : String(error);
  return raw
    .replace(/(https?:\/\/)[^/\s:@]+:[^/\s@]+@/g, "$1***@")
    .replace(/([?&](?:token|access_token|api[_-]?key|password)=)[^&\s]+/gi, "$1***")
    .replace(/\b[0-9a-f]{32,}\b/gi, "***")
    .replace(/\bgh[psour]_[A-Za-z0-9]{20,}\b/g, "***")
    .slice(0, 500);
}

/** Default readiness probe: poll GET /api/v1/health until 200 or timeout. */
async function defaultWaitForReady(host: string, port: number, token: string): Promise<void> {
  const url = `http://${host}:${port}/api/v1/health`;
  const deadline = Date.now() + 60_000;
  let lastError: unknown = new Error("not ready");
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url, {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(3_000),
      });
      if (res.ok) return;
      lastError = new Error(`health returned ${res.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`container did not become ready: ${String(lastError)}`);
}
