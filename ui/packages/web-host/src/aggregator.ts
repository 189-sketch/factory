/**
 * Event normalization + run.activity batching (W4.3, #41).
 *
 * The aggregator receives raw per-container frames (W4.2) and produces the
 * single normalized stream the renderer consumes over `/ui/events`:
 *
 *  - The envelope already carries `repository`; it is forwarded as-is. When the
 *    envelope is missing `repository` (or it disagrees with the connection's
 *    repo), the aggregator stamps the canonical one so the renderer can always
 *    bucket by repository.
 *  - Unknown event types and unknown envelope/payload fields pass through
 *    unchanged (additive-only): the web-host never needs an upgrade when the
 *    core evolves additively.
 *  - High-frequency `run.activity` frames for the same run are coalesced over a
 *    short window (default 100ms) into one frame carrying the batch, so a burst
 *    doesn't flood the renderer. Low-frequency events are never batched.
 *
 * The aggregator is a pure, injectable-clock transform — the SSE fan-out and
 * per-frontend cursors live in the hub (W4.4). No `api_token` ever appears in
 * the normalized output (envelope + payload are forwarded, never credentials).
 */

import type { ContainerFrame } from "./connections.js";

/** A normalized event ready for fan-out. The envelope is the core's, with
 * `repository` guaranteed present. Unknown shapes are passed through. */
export interface NormalizedEvent {
  /** Event type (envelope `type`, or the SSE `event:` for unknown shapes). */
  type: string;
  /** Canonical repository this event belongs to. */
  repository: string;
  /** The full (possibly augmented) envelope JSON. */
  envelope: Record<string, unknown>;
}

export interface AggregatorOptions {
  /** Batching window for run.activity, ms. Default 100. */
  batchWindowMs?: number;
  /** Injectable clock/scheduler for deterministic tests. */
  setTimeoutFn?: (fn: () => void, ms: number) => unknown;
  clearTimeoutFn?: (handle: unknown) => void;
  /** Sink for normalized events (the fan-out hub). */
  emit: (event: NormalizedEvent) => void;
}

const DEFAULT_BATCH_WINDOW_MS = 100;

interface PendingActivity {
  repository: string;
  runId: number | null;
  frames: Record<string, unknown>[];
  timer: unknown;
}

export class EventAggregator {
  private readonly batchWindowMs: number;
  private readonly setTimeoutFn: (fn: () => void, ms: number) => unknown;
  private readonly clearTimeoutFn: (handle: unknown) => void;
  private readonly emit: (event: NormalizedEvent) => void;
  /** Pending run.activity batches keyed by `${repository}:${runId}`. */
  private readonly pending = new Map<string, PendingActivity>();

  constructor(options: AggregatorOptions) {
    this.batchWindowMs = options.batchWindowMs ?? DEFAULT_BATCH_WINDOW_MS;
    this.setTimeoutFn =
      options.setTimeoutFn ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimeoutFn =
      options.clearTimeoutFn ?? ((handle) => clearTimeout(handle as never));
    this.emit = options.emit;
  }

  /**
   * Ingest one raw container frame. `repository` is the connection's canonical
   * repo (used to stamp envelopes that lack or mis-state it).
   */
  ingest(repository: string, frame: ContainerFrame): void {
    const envelope = parseEnvelope(frame.data);
    const type = envelopeType(envelope) ?? frame.event;

    // Stamp/forward the repository. When the envelope is missing it (or the
    // frame is a bare payload with no envelope), make it explicit.
    const normalizedEnvelope: Record<string, unknown> = envelope
      ? { ...envelope, repository: (envelope.repository as string) ?? repository }
      : { repository, type, payload: safePayload(frame.data) };
    if (!normalizedEnvelope.repository) normalizedEnvelope.repository = repository;

    if (type === "run.activity") {
      this.bufferActivity(repository, normalizedEnvelope);
      return;
    }
    this.emit({ type, repository, envelope: normalizedEnvelope });
  }

  /** Flush any pending run.activity batches (e.g. on shutdown). */
  flush(): void {
    for (const key of [...this.pending.keys()]) {
      this.flushBatch(key);
    }
  }

  private bufferActivity(repository: string, envelope: Record<string, unknown>): void {
    const runId = (envelope.run_id as number | null) ?? null;
    const key = `${repository}:${runId ?? "none"}`;
    const existing = this.pending.get(key);
    if (existing) {
      existing.frames.push(envelope);
      return; // timer already armed; window restarts only after flush.
    }
    const timer = this.setTimeoutFn(() => this.flushBatch(key), this.batchWindowMs);
    this.pending.set(key, { repository, runId, frames: [envelope], timer });
  }

  private flushBatch(key: string): void {
    const pending = this.pending.get(key);
    if (!pending) return;
    this.pending.delete(key);
    this.clearTimeoutFn(pending.timer);
    if (pending.frames.length === 1) {
      // A lone activity frame passes through untouched (no pointless batch).
      this.emit({
        type: "run.activity",
        repository: pending.repository,
        envelope: pending.frames[0]!,
      });
      return;
    }
    // Coalesce N frames for the same run into one batched frame.
    this.emit({
      type: "run.activity",
      repository: pending.repository,
      envelope: {
        type: "run.activity",
        repository: pending.repository,
        run_id: pending.runId,
        payload: { batched: true, count: pending.frames.length },
        batch: pending.frames,
      },
    });
  }
}

function parseEnvelope(data: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(data);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

function envelopeType(envelope: Record<string, unknown> | null): string | null {
  const type = envelope?.type;
  return typeof type === "string" ? type : null;
}

function safePayload(data: string): Record<string, unknown> {
  return parseEnvelope(data) ?? { raw: data };
}
