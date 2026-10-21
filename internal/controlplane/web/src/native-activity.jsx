import React, { useEffect, useState, useId } from "react";
import Markdown from "react-markdown";
import { Check, Circle, CircleAlert, LoaderCircle } from "lucide-react";
import { reduceActivity, pollRunActivity } from "./native-activity.js";
import { formatDurationMillis } from "./run-metrics.js";

export function useRunEvents(run) {
  const [snapshot, setSnapshot] = useState({ id: run?.id, events: [], error: "", loaded: false });
  useEffect(() => {
    if (!run?.id) return;
    let stopped = false;
    let cursor = 0;
    let timer;
    const controller = new AbortController();
    setSnapshot({ id: run.id, events: [], error: "", loaded: false });
    async function poll() {
      let retry = false;
      try {
        let more = true;
        while (more && !stopped) {
          const response = await fetch(`/api/v1/runs/${encodeURIComponent(run.id)}/events?after=${cursor}`, { signal: controller.signal });
          if (!response.ok) throw new Error(`Unable to load activity (${response.status}). Retrying.`);
          const page = await response.json();
          if (stopped) return;
          setSnapshot(previous => ({ id: run.id, events: [...previous.events, ...page.events], error: "", loaded: true }));
          cursor = page.cursor;
          more = page.events.length === 128;
        }
      } catch (failure) {
        retry = true;
        if (!stopped) setSnapshot(previous => ({ ...previous, error: failure.message, loaded: true }));
      }
      if (!stopped && (retry || pollRunActivity(run))) timer = setTimeout(poll, 2000);
    }
    poll();
    return () => { stopped = true; clearTimeout(timer); controller.abort(); };
  }, [run?.id, run?.state, run?.phase, run?.completed_at]);
  const current = snapshot.id === run?.id ? snapshot : { events: [], error: "", loaded: false };
  return { ...current, ...reduceActivity(current.events, run?.state) };
}

export function RunMarkdown({ children }) {
  return <div className="run-prose"><Markdown skipHtml>{String(children || "")}</Markdown></div>;
}

const statusLabels = { running: "Running", completed: "Completed", failed: "Failed", interrupted: "Interrupted", cancelled: "Cancelled", unknown: "Status unknown" };
function activityDuration(child) {
  const start = Date.parse(child.started_at);
  const end = Date.parse(child.updated_at);
  return Number.isFinite(start) && Number.isFinite(end) && end > start && child.status !== "running" ? formatDurationMillis(end - start) : null;
}

export function ExecutionGraph({ activities, runState, idPrefix }) {
  const titleID = useId();
  if (!activities.length) return null;
  const width = Math.max(560, activities.length * 220);
  const center = width / 2;
  const primaryStatus = runState === "succeeded" ? "Completed" : runState === "running" ? "Running" : runState === "failed" ? "Failed" : "Stopped";
  return <section className="execution-map" aria-labelledby={titleID}>
    <div className="run-section-heading"><h2 id={titleID}>Execution map</h2><span>{activities.length} native subagents</span></div>
    <p className="run-section-description">The primary agent delegated these tasks. Select an agent to read its report.</p>
    <div className="execution-map-scroll" tabIndex={0} aria-label="Delegation graph, scroll horizontally to see all agents">
      <div className="execution-map-canvas" style={{ width }}>
        <svg viewBox={`0 0 ${width} 200`} className="execution-map-lines" aria-hidden="true">
          <path d={`M ${center} 66 V 100`} />
          {activities.map((child, i) => <path key={child.native_id} d={`M ${center} 100 H ${i * 220 + (width - activities.length * 220) / 2 + 110} V 132`} />)}
        </svg>
        <div className="execution-node execution-primary" style={{ left: center - 100 }}><strong>Primary agent</strong><span>{primaryStatus}</span></div>
        {activities.map((child, i) => <a className={`execution-node execution-child run-status-${child.status}`} style={{ left: i * 220 + (width - activities.length * 220) / 2 + 10 }} key={child.native_id} href={`#${encodeURIComponent(`${idPrefix}-agent-${child.native_id}`)}`} onClick={event => { event.preventDefault(); const report = document.getElementById(`${idPrefix}-agent-${child.native_id}`); report?.scrollIntoView({ block: "center", behavior: "instant" }); report?.focus({ preventScroll: true }); }}><strong>{child.name || "Unnamed subagent"}</strong><span>{statusLabels[child.status] || "Status unknown"}{activityDuration(child) && ` · ${activityDuration(child)}`}</span></a>)}
      </div>
    </div>
    <p className="execution-map-note">Connections show delegation, not a required order between tasks.</p>
  </section>;
}

export function ActivityTimeline({ children: activities, loaded = true, runState, idPrefix }) {
  const generatedID = useId();
  const scope = idPrefix || generatedID;
  const completed = activities.filter(child => child.status === "completed").length;
  const running = activities.filter(child => child.status === "running").length;
  return <section className="run-agents" aria-label="Agent activity">
    <div className="run-section-heading"><h2>Agent activity</h2>{activities.length > 0 && <span>{running ? `${running} running` : `${completed} of ${activities.length} completed`}</span>}</div>
    <p className="run-section-description">Work delegated by the primary agent. Each entry is a separate native subagent.</p>
    {!activities.length && <p className="run-empty" role="status">{!loaded ? "Loading agent activity…" : runState === "running" ? "The primary agent is working. Delegated tasks will appear here." : "No subagent activity was reported for this attempt."}</p>}
    <ol className="run-timeline">{activities.map(child => {
      const duration = activityDuration(child);
      const Icon = child.status === "completed" ? Check : child.status === "running" ? LoaderCircle : ["failed", "interrupted"].includes(child.status) ? CircleAlert : Circle;
      return <li key={child.native_id} id={`${scope}-agent-${child.native_id}`} tabIndex={-1} className="run-timeline-item">
        <span className={`run-timeline-marker run-status-${child.status}`}><Icon size={15} aria-hidden="true" className={child.status === "running" ? "run-spinner" : ""} /></span>
        <div className="run-agent-body"><div className="run-agent-heading"><h3>{child.name || "Unnamed subagent"}</h3><span className={`run-agent-status run-status-${child.status}`}>{statusLabels[child.status] || "Status unknown"}{duration && <span className="run-agent-duration"> · {duration}</span>}</span></div>
          {child.summary ? <RunMarkdown>{child.summary}</RunMarkdown> : <p className="run-agent-pending">{child.status === "running" ? "Working. Its report will appear here." : "No report was returned."}</p>}
        </div>
      </li>;
    })}</ol>
  </section>;
}

export function PrimaryLog({ logs }) {
  return <section className="run-log" aria-label="Primary agent messages"><h2>Primary log</h2><p className="run-section-description">Messages from the agent coordinating this attempt.</p>{logs.length ? <ol>{logs.map((text, index) => <li key={index}><RunMarkdown>{readableMessage(text)}</RunMarkdown></li>)}</ol> : <p className="run-empty">No primary messages were reported.</p>}</section>;
}

function readableMessage(text) {
  try {
    const result = JSON.parse(text);
    if (["complete", "failed", "blocked"].includes(result.outcome) && typeof result.summary === "string") return result.summary;
  } catch { /* Ordinary messages are already readable text. */ }
  return text;
}

export function EventDiagnostics({ events }) {
  return <section className="run-diagnostics" aria-label="Raw event diagnostics"><h2>Raw event diagnostics</h2><p className="run-section-description">The original event records, for troubleshooting.</p><pre tabIndex={0}>{events.length ? events.map(event => JSON.stringify(event)).join("\n") : "No event records are available."}</pre></section>;
}
