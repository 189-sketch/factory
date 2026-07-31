import { z } from "zod";
import { BACKEND_KINDS, REPOSITORY_STATUSES } from "./registry.js";

/**
 * HTTP request/response schemas for the registry API.
 *
 * The API speaks in wire format (snake_case fields) at the boundary and is
 * translated to the registry's domain types inside the routes. zod is the
 * single source of truth for what a valid create/update payload looks like.
 */

export const createRepositorySchema = z.object({
  repository: z.string().min(1, "repository is required"),
  provider: z.string().min(1, "provider is required"),
  backend: z.enum(BACKEND_KINDS).optional(),
  credential_ref: z.string().nullable().optional(),
  idle_timeout_seconds: z.number().int().positive().nullable().optional(),
  branch: z.string().nullable().optional(),
  trigger_labels: z.array(z.string()).optional(),
  status: z.enum(REPOSITORY_STATUSES).optional(),
});
export type CreateRepositoryBody = z.infer<typeof createRepositorySchema>;

export const updateRepositorySchema = z
  .object({
    provider: z.string().min(1).optional(),
    backend: z.enum(BACKEND_KINDS).optional(),
    credential_ref: z.string().nullable().optional(),
    idle_timeout_seconds: z.number().int().positive().nullable().optional(),
    branch: z.string().nullable().optional(),
    trigger_labels: z.array(z.string()).optional(),
    status: z.enum(REPOSITORY_STATUSES).optional(),
    container_id: z.string().nullable().optional(),
    host: z.string().nullable().optional(),
    port: z.number().int().min(0).max(65535).nullable().optional(),
    api_token: z.string().nullable().optional(),
    last_activity_at: z.string().nullable().optional(),
    onboard_step: z.string().nullable().optional(),
  })
  .strict();
export type UpdateRepositoryBody = z.infer<typeof updateRepositorySchema>;

/** Wire shape of a repository as returned over HTTP. */
export interface RepositoryDto {
  repository: string;
  provider: string;
  backend: string;
  credential_ref: string | null;
  idle_timeout_seconds: number | null;
  branch: string | null;
  trigger_labels: string[];
  status: string;
  container_id: string | null;
  host: string | null;
  port: number | null;
  last_activity_at: string | null;
  onboard_step: string | null;
  created_at: string;
  updated_at: string;
}

export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
  };
}
