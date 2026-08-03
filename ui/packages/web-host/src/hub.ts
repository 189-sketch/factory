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

  /** Flush pending run.activity batches (e.g. before shutdown). */
  flush(): void {
    this.aggregator.flush();
  }

  /**
   * Subscribe a frontend connection. Returns an unsubscribe handle. When
   * `lastSeenSeq` is provided and still within the buffer, the missed frames
   * are replayed to this subscriber immediately (W4.4); the caller decides
   * what to do when the cursor has fallen outside the buffer (resync).
   */
  subscribe(
    handler: (frame: HubFrame) => void,
    lastSeenSeq?: number,
  ): { close(): void; missed: HubFrame[]; resyncRequired: boolean } {
    this.subscribers.add(handler);
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
    return {
      missed,
      resyncRequired,
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
