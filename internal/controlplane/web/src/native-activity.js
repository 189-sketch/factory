export function reduceActivity(events, runState) {
  const children = new Map();
  const logs = [];
  for (const event of events) {
    if (event.type === "agent.activity") {
      try {
        const activity = JSON.parse(event.message);
        if (!activity.native_id) continue;
        const prior = children.get(activity.native_id);
        children.set(activity.native_id, { ...prior, ...activity, started_at: prior?.started_at || event.time, updated_at: event.time });
      } catch { /* Raw event remains available in diagnostics. */ }
    }
    if (event.type === "agent.message") {
      try { const message = JSON.parse(event.message); if (message.primary) logs.push(message.text); } catch { /* Ignore malformed display data. */ }
    }
  }
  const stopped = ["interrupted", "cancelled", "timed_out"].includes(runState);
  return { children: [...children.values()].map(child => stopped && child.status === "running" ? { ...child, status: "interrupted" } : child), logs };
}
