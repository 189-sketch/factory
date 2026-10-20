import React, { useEffect, useState } from "react";
import { reduceActivity } from "./native-activity.js";
import { formatTimestamp } from "./task-display.jsx";

export function NativeActivity({ run }) {
  const [events, setEvents] = useState([]);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!run?.id) return;
    let stopped = false;
    let cursor = 0;
    let timer;
    const controller = new AbortController();
    setEvents([]);
    async function poll() {
      try {
        let more = true;
        while (more && !stopped) {
          const response = await fetch(`/api/v1/runs/${encodeURIComponent(run.id)}/events?after=${cursor}`, { signal: controller.signal });
          if (!response.ok) throw new Error(`Activity unavailable (${response.status}). Retrying.`);
          const page = await response.json();
          if (stopped) return;
          setEvents(previous => [...previous, ...page.events]);
          cursor = page.cursor;
          more = page.events.length === 128;
        }
        setError("");
      } catch (failure) { if (!stopped) setError(failure.message); }
      if (!stopped) timer = setTimeout(poll, 2000);
    }
    poll();
    return () => { stopped = true; clearTimeout(timer); controller.abort(); };
  }, [run?.id]);
  const { children, logs } = reduceActivity(events, run?.state);
  return <section className="mt-6 space-y-4" aria-label="Agent activity">
    <h2 className="text-sm font-medium">Agent activity</h2>
    {error && <p role="status" className="text-sm text-danger">Connection lost. {error} The last observed activity is shown below.</p>}
    {!children.length && <p className="text-sm text-muted-foreground">No child activity reported.</p>}
    <ol className="space-y-4">{children.map(child => <li key={child.native_id} className="space-y-1 border-b pb-4">
      <div className="flex flex-wrap justify-between gap-2 text-sm"><strong className="break-all">{child.name || child.native_id}</strong><span>{child.status}</span></div>
      <p className="text-xs text-muted-foreground">Started {formatTimestamp(child.started_at)} · Updated {formatTimestamp(child.updated_at)}</p>
      {child.summary && <p className="whitespace-pre-wrap break-words text-sm">{child.summary}</p>}
    </li>)}</ol>
    <details><summary className="cursor-pointer text-sm">Primary log</summary><pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">{logs.join("\n") || "No primary messages reported."}</pre></details>
    <details><summary className="cursor-pointer text-sm">Raw event diagnostics</summary><pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-all text-xs">{events.map(event => JSON.stringify(event)).join("\n")}</pre></details>
  </section>;
}
