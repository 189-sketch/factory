export function reduceActivity(events, runState) {
  // Raw history is retained. Reconstruct identity classification and original
  // names so pre-fix runs get the same graph without rewriting their history.
  const native = nativeIdentities(events);
  const children = new Map();
  const logs = [];
  for (const event of events) {
    if (event.type === "agent.activity") {
      try {
        const activity = JSON.parse(event.message);
        if (!activity.native_id || native.roots.has(activity.native_id)) continue;
        if (native.classified && !native.agents.has(activity.native_id)) continue;
        const definition = native.agents.get(activity.native_id);
        if (definition?.name) activity.name = definition.name;
        const prior = children.get(activity.native_id);
        if (activity.status === "unknown" && prior) activity.status = prior.status;
        if (prior?.name) activity.name = prior.name;
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

function nativeIdentities(events) {
  const agents = new Map(), roots = new Set(), streams = new Map();
  let classified = false;
  const observe = value => {
    if (value.type === "system" && value.subtype === "task_started") {
      if (value.task_type) classified = true;
      if (value.task_type === "local_agent") agents.set(value.task_id || value.agent_id, {name: value.description});
    }
    if (value.id === 4 && value.result?.thread?.id) roots.add(value.result.thread.id);
    const item = value.params?.item;
    if (item?.type === "subAgentActivity") {
      classified = true;
      if (["started", "completed", "failed", "interrupted"].includes(item.kind)) {
        const prior = agents.get(item.agentThreadId);
        agents.set(item.agentThreadId, {name: prior?.name || item.agentPath});
      }
    }
  };
  for (const event of events) {
    if (event.type !== "process.output" || event.encoding !== "base64") continue;
    try {
      const key = event.stream || "stdout";
      let state = streams.get(key);
      if (!state) { state = {decoder: new TextDecoder(), pending: ""}; streams.set(key, state); }
      const bytes = Uint8Array.from(atob(event.data), char => char.charCodeAt(0));
      state.pending += state.decoder.decode(bytes, {stream: true});
      let end;
      while ((end = state.pending.indexOf("\n")) >= 0) {
        const line = state.pending.slice(0, end); state.pending = state.pending.slice(end + 1);
        try { observe(JSON.parse(line)); } catch { /* Non-JSON output is diagnostic only. */ }
      }
      if (state.pending.length > 1 << 20) state.pending = "";
    } catch { /* Malformed raw output does not hide normalized activity. */ }
  }
  for (const state of streams.values()) { try { observe(JSON.parse(state.pending)); } catch { /* Partial live line. */ } }
  return {agents, roots, classified};
}

export function pollRunActivity(run) {
  if (["running", "queued"].includes(run?.state)) return true;
  // A cancelled run may still be uploading its terminal events. Bound polling
  // for old histories that predate stop acknowledgments.
  return run?.state === "cancelled" && run?.phase === "stopping";
}
