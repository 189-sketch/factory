import express, { type Express, type Request, type Response } from "express";
import { ZodError } from "zod";
import { NormalizationError } from "./identity.js";
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

export function createApp(registry: RepositoryRegistry): Express {
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
