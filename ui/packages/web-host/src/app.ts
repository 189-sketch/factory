import express, { type Express, type Request, type Response } from "express";
import { ZodError, z } from "zod";
import { applyBatch } from "./batch.js";
import type { BackendDriver } from "./driver.js";
import type { UiEventBus } from "./events.js";
import { importFleet } from "./fleet-import.js";
import { NormalizationError } from "./identity.js";
import type { OnboardingPipeline } from "./onboard.js";
import {
  RepositoryConflictError,
  type RepositoryRegistry,
  type RepositoryRow,
} from "./registry.js";
import {
  createRepositorySchema,
  updateRepositorySchema,
  type ApiErrorBody,
  type RepositoryDto,
} from "./schemas.js";

/**
 * HTTP adapter over the repository registry.
 *
 * This layer is deliberately thin: it parses/validates the wire body (zod),
 * translates to domain types, calls the registry, and maps domain results and
 * errors onto status codes. No orchestration logic lives here.
 *
 * Error contract: every failure returns `{ error: { code, message } }` with a
 * machine-readable `code`, matching the A2 error envelope used across Factory.
 */

function toDto(row: RepositoryRow): RepositoryDto {
  return {
    repository: row.repository,
    provider: row.provider,
    backend: row.backend,
    credential_ref: row.credentialRef,
    idle_timeout_seconds: row.idleTimeoutSeconds,
    branch: row.branch,
    trigger_labels: row.triggerLabels,
    status: row.status,
    container_id: row.containerId,
    host: row.host,
    port: row.port,
    last_activity_at: row.lastActivityAt,
    onboard_step: row.onboardStep,
    created_at: row.createdAt,
    updated_at: row.updatedAt,
  };
}

function errorBody(code: string, message: string): ApiErrorBody {
  return { error: { code, message } };
}

/**
 * Express 5 types a wildcard route param as `string | string[]`; our
 * `:repository(*)` segment is always a single "owner/repo" path, so coerce to
 * a plain string (joining in the unlikely array case).
 */
function paramString(value: string | string[] | undefined): string {
  if (Array.isArray(value)) {
    return value.join("/");
  }
  return value ?? "";
}

function sendError(res: Response, status: number, code: string, message: string): void {
  res.status(status).json(errorBody(code, message));
}

const onboardBodySchema = z.object({
  git_url: z.string().min(1, "git_url is required"),
  provider: z.string().min(1, "provider is required"),
  branch: z.string().nullable().optional(),
  trigger_labels: z.array(z.string()).optional(),
  idle_timeout_seconds: z.number().int().positive().nullable().optional(),
  backend: z.string().optional(),
  credential_ref: z.string().nullable().optional(),
});

const batchBodySchema = z.object({
  action: z.enum(["pause", "resume", "destroy"]),
  repositories: z.array(z.string().min(1)).min(1, "repositories must be non-empty"),
});

const importFleetBodySchema = z.object({
  content: z.string().min(1, "content is required"),
});

export interface AppDeps {
  pipeline?: OnboardingPipeline;
  bus?: UiEventBus;
  driver?: BackendDriver;
}

export function createApp(registry: RepositoryRegistry, deps: AppDeps = {}): Express {
  const app = express();
  app.use(express.json());
  // Never cache registry responses.
  app.use((_req, res, next) => {
    res.setHeader("Cache-Control", "no-cache");
    next();
  });

  app.get("/ui/api/health", (_req: Request, res: Response) => {
    res.status(200).json({ status: "ok" });
  });

  // Onboarding pipeline: submit a git URL, get a running container (or an
  // idempotent existing registration). Progress streams over /ui/events.
  app.post("/ui/api/onboard", async (req: Request, res: Response) => {
    if (!deps.pipeline) {
      sendError(res, 503, "unavailable", "onboarding pipeline not configured");
      return;
    }
    try {
      const body = onboardBodySchema.parse(req.body ?? {});
      const result = await deps.pipeline.onboard({
        gitUrl: body.git_url,
        provider: body.provider,
        branch: body.branch ?? null,
        triggerLabels: body.trigger_labels,
        idleTimeoutSeconds: body.idle_timeout_seconds ?? null,
        backend: body.backend,
        credentialRef: body.credential_ref ?? null,
      });
      if (!result.ok) {
        res
          .status(502)
          .json(errorBody("onboard_failed", `${result.step}: ${result.error}`));
        return;
      }
      res.status(result.idempotent ? 200 : 201).json(toDto(result.repository));
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  // Batch management: apply pause/resume/destroy across many repositories,
  // isolating per-repo failures and returning per-repo outcomes.
  app.post("/ui/api/repos/batch", async (req: Request, res: Response) => {
    if (!deps.driver) {
      sendError(res, 503, "unavailable", "backend driver not configured");
      return;
    }
    try {
      const body = batchBodySchema.parse(req.body ?? {});
      const result = await applyBatch(registry, deps.driver, body.action, body.repositories);
      res.status(200).json(result);
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  // fleet.toml one-shot import: parse [[repository]] blocks, register and
  // onboard each (idempotent for already-running repositories).
  app.post("/ui/api/import-fleet", async (req: Request, res: Response) => {
    if (!deps.pipeline) {
      sendError(res, 503, "unavailable", "onboarding pipeline not configured");
      return;
    }
    try {
      const body = importFleetBodySchema.parse(req.body ?? {});
      const result = await importFleet(body.content, registry, deps.pipeline);
      res.status(200).json(result);
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  // ui-synthesized event stream (onboarding progress now, container
  // aggregation in W4). SSE with no buffering.
  app.get("/ui/events", (req: Request, res: Response) => {
    if (!deps.bus) {
      sendError(res, 503, "unavailable", "event bus not configured");
      return;
    }
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    });
    res.write(`retry: 3000\n\n`);
    const subscription = deps.bus.subscribe((event) => {
      res.write(`event: ${event.type}\n`);
      res.write(`data: ${JSON.stringify(event)}\n\n`);
    });
    req.on("close", () => subscription.close());
  });

  app.get("/ui/api/repos", (_req: Request, res: Response) => {
    res.status(200).json({ repositories: registry.list().map(toDto) });
  });

  app.post("/ui/api/repos", (req: Request, res: Response) => {
    try {
      const body = createRepositorySchema.parse(req.body ?? {});
      const created = registry.create({
        repository: body.repository,
        provider: body.provider,
        backend: body.backend,
        credentialRef: body.credential_ref ?? null,
        idleTimeoutSeconds: body.idle_timeout_seconds ?? null,
        branch: body.branch ?? null,
        triggerLabels: body.trigger_labels,
        status: body.status,
      });
      res.status(201).json(toDto(created));
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  app.get("/ui/api/repos/*splat", (req: Request, res: Response) => {
    try {
      const row = registry.get(paramString(req.params.splat));
      if (!row) {
        sendError(res, 404, "not_found", "repository not registered");
        return;
      }
      res.status(200).json(toDto(row));
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  app.patch("/ui/api/repos/*splat", (req: Request, res: Response) => {
    try {
      const body = updateRepositorySchema.parse(req.body ?? {});
      const updated = registry.update(paramString(req.params.splat), {
        provider: body.provider,
        backend: body.backend,
        credentialRef: body.credential_ref,
        idleTimeoutSeconds: body.idle_timeout_seconds,
        branch: body.branch,
        triggerLabels: body.trigger_labels,
        status: body.status,
        containerId: body.container_id,
        host: body.host,
        port: body.port,
        apiToken: body.api_token,
        lastActivityAt: body.last_activity_at,
        onboardStep: body.onboard_step,
      });
      if (!updated) {
        sendError(res, 404, "not_found", "repository not registered");
        return;
      }
      res.status(200).json(toDto(updated));
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  app.delete("/ui/api/repos/*splat", (req: Request, res: Response) => {
    try {
      const removed = registry.delete(paramString(req.params.splat));
      if (!removed) {
        sendError(res, 404, "not_found", "repository not registered");
        return;
      }
      res.status(204).end();
    } catch (error) {
      handleRouteError(res, error);
    }
  });

  // Centralized error mapping so every route shares one error envelope.
  function handleRouteError(res: Response, error: unknown): void {
    if (error instanceof ZodError) {
      const message = error.issues
        .map((issue) => `${issue.path.join(".") || "body"}: ${issue.message}`)
        .join("; ");
      sendError(res, 400, "invalid_request", message);
      return;
    }
    if (error instanceof NormalizationError) {
      sendError(res, 400, "invalid_repository", error.message);
      return;
    }
    if (error instanceof RepositoryConflictError) {
      sendError(res, 409, "conflict", error.message);
      return;
    }
    sendError(res, 500, "internal", "unexpected error");
  }

  return app;
}
