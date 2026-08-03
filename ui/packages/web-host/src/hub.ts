/**
 * Fan-out hub (W4.3, #41): the single `/ui/events` stream the renderer
 * connects to, fed by every container's normalized events.
 *
 * The hub owns the EventAggregator and subscribes each frontend connection.
 * Every fanned-out frame gets a ui-side monotonically-increasing `seq` that is
 * independent of any container's per-repo cursor — this is the cursor a
 * frontend uses for `Last-Event-ID` reconnect (W4.4). A short ring buffer of
 * recent frames lets a reconnecting frontend backfill what it missed.
 *
 * The renderer sees one mixed stream; it buckets events by `repository`.
 */

import { EventAggregator, type NormalizedEvent } from "./aggregator.js";
import type { ContainerFrame } from "./connections.js";

/** A frame on the renderer-facing `/ui/events` stream. */
export interface HubFrame {
  /** ui-side global cursor (monotonic, independent of per-repo cursors). */
  seq: number;
  /** Event type (used as the SSE `event:` field). */
  type: string;
  /** Canonical repository. */
  repository: string;
  /** Full normalized envelope JSON. */
  envelope: Record<string, unknown>;
}

export interface HubOptions {
  /** Ring buffer capacity for reconnect backfill. Default 500. */
  bufferSize?: number;
  /** run.activity batch window (forwarded to the aggregator). */
  batchWindowMs?: number;
  /** Injectable scheduler (forwarded to the aggregator). */
  setTimeoutFn?: (fn: () => void, ms: number) => unknown;
  clearTimeoutFn?: (handle: unknown) => void;
}

const DEFAULT_BUFFER_SIZE = 500;

export class EventHub {
  private readonly aggregator: EventAggregator;
  private readonly subscribers = new Set<(frame: HubFrame) => void>();
  private readonly bufferSize: number;
  private readonly buffer: HubFrame[] = [];
  private seq = 0;

  constructor(options: HubOptions = {}) {
    this.bufferSize = options.bufferSize ?? DEFAULT_BUFFER_SIZE;
    this.aggregator = new EventAggregator({
      batchWindowMs: options.batchWindowMs,
      setTimeoutFn: options.setTimeoutFn,
      clearTimeoutFn: options.clearTimeoutFn,
      emit: (event) => this.fanOut(event),
    });
  }

  /** Ingest a raw frame from a container connection (called by W4.2's onEvent). */
  ingest(repository: string, frame: ContainerFrame): void {
    this.aggregator.ingest(repository, frame);
  }

  /** Number of live frontend subscribers (diagnostic / test seam). */
  subscriberCount(): number {
    return this.subscribers.size;
  }

  /** Flush pending run.activity batches (e.g. before shutdown). */
  flush(): void {
    this.aggregator.flush();
  }

  /**
   * Subscribe a frontend connection. Returns an unsubscribe handle plus the
   * backfill decision. When `lastSeenSeq` is provided and still within the
   * buffer, the missed frames are computed for the caller to replay; when it
   * has fallen outside the buffer, `resyncRequired` tells the caller to emit a
   * synthetic resync instead.
   *
   * Ordering guarantee: the handler is NOT registered for live frames until
   * `activate()` is called, so the caller can write the missed frames first and
   * then go live — a reconnecting frontend never sees a live frame interleaved
   * ahead of its backfill (which would rewind its derived state).
   */
  subscribe(
    handler: (frame: HubFrame) => void,
    lastSeenSeq?: number,
  ): { close(): void; activate(): void; missed: HubFrame[]; resyncRequired: boolean; liveSeq: number } {
    let missed: HubFrame[] = [];
    let resyncRequired = false;
    if (lastSeenSeq !== undefined && lastSeenSeq < this.seq) {
      const available = this.buffer.filter((f) => f.seq > lastSeenSeq);
      const oldestBuffered = this.buffer[0]?.seq;
      // If the oldest frame we still hold is newer than what the frontend
      // needs, part of the gap is gone for good — it must resync.
      resyncRequired =
        oldestBuffered !== undefined && oldestBuffered > lastSeenSeq + 1;
      missed = available;
    }
    // Frames that arrive between computing `missed` and activate() would be
    // missed by both; capture the live watermark so activate() can backfill
    // that sliver too, keeping the stream gap-free and in order.
    const liveSeq = this.seq;
    let active = false;
    const activate = () => {
      if (active) return;
      active = true;
      // Backfill anything that arrived after `missed` was computed, then
      // register for live frames.
      if (!resyncRequired) {
        for (const frame of this.buffer) {
          if (frame.seq > liveSeq && !missed.includes(frame)) handler(frame);
        }
      }
      this.subscribers.add(handler);
    };
    // A live-only subscription (no cursor to backfill) has no ordering hazard,
    // so it goes active immediately; the two-phase activate() only matters when
    // there is a backfill to write first.
    if (lastSeenSeq === undefined) {
      activate();
    }
    return {
      missed,
      resyncRequired,
      liveSeq,
      activate,
      close: () => this.subscribers.delete(handler),
    };
  }

  private fanOut(event: NormalizedEvent): void {
    this.seq += 1;
    const frame: HubFrame = {
      seq: this.seq,
      type: event.type,
      repository: event.repository,
      envelope: event.envelope,
    };
    this.buffer.push(frame);
    if (this.buffer.length > this.bufferSize) {
      this.buffer.splice(0, this.buffer.length - this.bufferSize);
    }
    for (const subscriber of this.subscribers) {
      subscriber(frame);
    }
  }
}
