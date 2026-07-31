/**
 * UiEventBus: in-process fan-out for ui-synthesized events.
 *
 * W3.3 uses it to push onboarding progress to `/ui/events` SSE subscribers;
 * W4 extends the same stream with aggregated container events. Events are
 * transient (no persistence) — a frontend that connects mid-onboard simply
 * misses earlier steps and catches up from the registry's `onboard_step`.
 */

export interface UiEvent {
  /** Event type, e.g. "onboard.progress" | "onboard.ready" | "onboard.failed". */
  type: string;
  repository: string;
  /** Free-form payload; always JSON-serializable. */
  payload: Record<string, unknown>;
  /** ISO-8601 timestamp. */
  time: string;
}

export class UiEventBus {
  private readonly subscribers = new Set<(event: UiEvent) => void>();

  publish(event: Omit<UiEvent, "time"> & { time?: string }): void {
    const full: UiEvent = { ...event, time: event.time ?? new Date().toISOString() };
    for (const subscriber of this.subscribers) {
      subscriber(full);
    }
  }

  subscribe(handler: (event: UiEvent) => void): { close(): void } {
    this.subscribers.add(handler);
    return { close: () => this.subscribers.delete(handler) };
  }
}
