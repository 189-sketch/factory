import { useMemo, useState } from "react";
import { Activity, CheckCircle2, Clock3, Play, XCircle } from "lucide-react";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const zeroTime = "0001-01-01T00:00:00Z";
const terminalStates = new Set(["succeeded", "failed", "timed_out", "cancelled"]);

export function Analytics({ jobs }) {
  const [days, setDays] = useState("30");
  const metrics = useMemo(() => calculateMetrics(jobs, days), [days, jobs]);

  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <h1 className="text-xl font-semibold tracking-tight">Run analytics</h1>
      <label className="w-full sm:w-40"><span className="field-label">Time window</span><select className="field-control" value={days} onChange={(event) => setDays(event.target.value)}><option value="7">Last 7 days</option><option value="30">Last 30 days</option></select></label>
    </header>

    <section className="grid grid-cols-2 gap-3 lg:grid-cols-4" aria-label="Execution metrics">
      <Metric label="Executions" value={metrics.executions.length} icon={Activity} />
      <Metric label="Success rate" value={metrics.terminal.length ? `${Math.round(metrics.succeeded.length / metrics.terminal.length * 100)}%` : "—"} icon={CheckCircle2} tone="text-success" />
      <Metric label="Median duration" value={metrics.medianDuration === null ? "—" : formatDuration(metrics.medianDuration)} icon={Clock3} />
      <Metric label="Active" value={metrics.active.length} icon={Play} tone="text-warning" />
    </section>

    <section className="grid gap-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(18rem,1fr)]">
      <Card className="p-4 sm:p-5"><h2 className="text-sm font-semibold">Executions over time</h2><Trend days={metrics.trend} /></Card>
      <Card className="p-4 sm:p-5"><h2 className="text-sm font-semibold">Outcomes</h2><Outcomes metrics={metrics} /></Card>
    </section>

    <section><h2 className="mb-3 text-sm font-semibold">By agent</h2><AgentTable agents={metrics.agents} /></section>
  </div>;
}

function Metric({ label, value, icon: Icon, tone = "text-muted-foreground" }) {
  return <Card className="p-4"><div className="flex items-center justify-between"><p className="text-xs text-muted-foreground">{label}</p><Icon className={cn("size-4", tone)} /></div><p className="mt-2 text-2xl font-semibold tabular-nums tracking-tight">{value}</p></Card>;
}

function Trend({ days }) {
  const maximum = Math.max(1, ...days.map((day) => day.total));
  if (!days.some((day) => day.total)) return <div className="grid h-48 place-items-center text-sm text-muted-foreground">No executions in this window.</div>;
  return <div className="mt-5">
    <div className="flex h-40 items-end gap-1.5 border-b border-border px-1">
      {days.map((day) => <div key={day.key} role="img" tabIndex={day.total ? 0 : -1} aria-label={`${day.label}: ${day.total} executions, ${day.succeeded} succeeded, ${day.failed} failed, ${day.timedOut} timed out, ${day.cancelled} cancelled, ${day.active} active`} className="group relative flex h-full min-w-0 flex-1 items-end rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/60">
        <div className="flex w-full flex-col-reverse overflow-hidden rounded-t-sm bg-muted" style={{ height: `${Math.max(day.total ? 8 : 2, day.total / maximum * 100)}%` }}>
          {day.succeeded > 0 && <span className="w-full bg-success" style={{ height: `${day.succeeded / day.total * 100}%` }} />}
          {day.failed > 0 && <span className="w-full bg-danger" style={{ height: `${day.failed / day.total * 100}%` }} />}
          {day.timedOut > 0 && <span className="w-full bg-warning" style={{ height: `${day.timedOut / day.total * 100}%` }} />}
          {day.cancelled > 0 && <span className="w-full bg-muted-foreground" style={{ height: `${day.cancelled / day.total * 100}%` }} />}
          {day.active > 0 && <span className="w-full bg-primary" style={{ height: `${day.active / day.total * 100}%` }} />}
        </div>
      </div>)}
    </div>
    <div className="mt-2 flex justify-between text-xs text-muted-foreground"><span>{days[0]?.label}</span><span>{days.at(-1)?.label}</span></div>
    <div className="mt-4 flex flex-wrap gap-4 text-xs text-muted-foreground"><Legend tone="bg-success" label="Succeeded" /><Legend tone="bg-danger" label="Failed" /><Legend tone="bg-warning" label="Timed out" /><Legend tone="bg-muted-foreground" label="Cancelled" /><Legend tone="bg-primary" label="Active" /></div>
  </div>;
}

function Legend({ tone, label }) { return <span className="flex items-center gap-1.5"><span className={cn("size-2 rounded-sm", tone)} />{label}</span>; }

function Outcomes({ metrics }) {
  const rows = [
    ["Succeeded", metrics.succeeded.length, "bg-success", CheckCircle2],
    ["Failed", metrics.failed.length, "bg-danger", XCircle],
    ["Timed out", metrics.timedOut.length, "bg-danger", Clock3],
    ["Cancelled", metrics.cancelled.length, "bg-muted-foreground", XCircle],
  ];
  const maximum = Math.max(1, ...rows.map(([, value]) => value));
  return <div className="mt-5 space-y-4">{rows.map(([label, value, tone, Icon]) => <div key={label}><div className="mb-1.5 flex items-center justify-between text-xs"><span className="flex items-center gap-2 text-muted-foreground"><Icon className="size-3.5" />{label}</span><strong className="font-medium tabular-nums text-foreground">{value}</strong></div><div className="h-1.5 overflow-hidden rounded-full bg-muted"><div className={cn("h-full rounded-full", tone)} style={{ width: `${value / maximum * 100}%` }} /></div></div>)}</div>;
}

function AgentTable({ agents }) {
  if (!agents.length) return <Card className="grid place-items-center p-12 text-sm text-muted-foreground">No agent executions in this window.</Card>;
  return <Card className="overflow-hidden"><div className="hidden grid-cols-[minmax(10rem,1fr)_7rem_8rem_9rem] gap-4 border-b border-border bg-muted/35 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground sm:grid"><span>Agent</span><span>Runs</span><span>Success</span><span>Median duration</span></div>{agents.map((agent) => <div key={agent.name} className="grid gap-2 border-b border-border px-4 py-3.5 last:border-b-0 sm:grid-cols-[minmax(10rem,1fr)_7rem_8rem_9rem] sm:items-center sm:gap-4"><p className="text-sm font-medium">{agent.name}</p><p className="text-xs text-muted-foreground"><span className="sm:hidden">Runs · </span>{agent.runs.length}</p><p className="text-xs text-muted-foreground"><span className="sm:hidden">Success · </span>{agent.terminal.length ? `${Math.round(agent.succeeded.length / agent.terminal.length * 100)}%` : "—"}</p><p className="text-xs text-muted-foreground"><span className="sm:hidden">Median · </span>{agent.medianDuration === null ? "—" : formatDuration(agent.medianDuration)}</p></div>)}</Card>;
}

function calculateMetrics(jobs, days) {
  const sinceDate = new Date();
  sinceDate.setHours(0, 0, 0, 0);
  sinceDate.setDate(sinceDate.getDate() - Number(days) + 1);
  const since = sinceDate.getTime();
  const runs = jobs.flatMap((job) => job.runs.map((run) => ({ ...run, jobCreatedAt: job.created_at })));
  const executions = runs.filter((run) => run.state !== "pending" && run.state !== "skipped" && validDate(run.started_at) && Date.parse(run.started_at) >= since);
  const terminal = executions.filter((run) => terminalStates.has(run.state));
  const succeeded = terminal.filter((run) => run.state === "succeeded");
  const failed = terminal.filter((run) => run.state === "failed");
  const timedOut = terminal.filter((run) => run.state === "timed_out");
  const cancelled = terminal.filter((run) => run.state === "cancelled");
  const queued = runs.filter((run) => run.state === "queued" && validDate(run.jobCreatedAt) && Date.parse(run.jobCreatedAt) >= since);
  const active = [...executions.filter((run) => run.state === "running"), ...queued];
  const durations = terminal.map(runDuration).filter((value) => value !== null);
  const agentNames = [...new Set(executions.map((run) => run.agent))].sort();
  const agents = agentNames.map((name) => {
    const runs = executions.filter((run) => run.agent === name);
    const agentTerminal = runs.filter((run) => terminalStates.has(run.state));
    return { name, runs, terminal: agentTerminal, succeeded: agentTerminal.filter((run) => run.state === "succeeded"), medianDuration: median(agentTerminal.map(runDuration).filter((value) => value !== null)) };
  });
  return { executions, terminal, succeeded, failed, timedOut, cancelled, active, medianDuration: median(durations), agents, trend: trendDays(executions, days) };
}

function trendDays(runs, windowDays) {
  const count = Number(windowDays);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return Array.from({ length: count }, (_, index) => {
    const date = new Date(today);
    date.setDate(today.getDate() - (count - index - 1));
    const next = new Date(date);
    next.setDate(date.getDate() + 1);
    const matching = runs.filter((run) => { const value = Date.parse(run.started_at); return value >= date.getTime() && value < next.getTime(); });
    return { key: date.toISOString(), label: date.toLocaleDateString([], { day: "numeric", month: "short" }), total: matching.length, succeeded: matching.filter((run) => run.state === "succeeded").length, failed: matching.filter((run) => run.state === "failed").length, timedOut: matching.filter((run) => run.state === "timed_out").length, cancelled: matching.filter((run) => run.state === "cancelled").length, active: matching.filter((run) => run.state === "running" || run.state === "queued").length };
  });
}

function validDate(value) { return Boolean(value && value !== zeroTime && Number.isFinite(Date.parse(value))); }
function runDuration(run) { if (!validDate(run.started_at) || !validDate(run.completed_at)) return null; return Math.max(0, (Date.parse(run.completed_at) - Date.parse(run.started_at)) / 1000); }
function median(values) { if (!values.length) return null; const sorted = [...values].sort((a, b) => a - b); const middle = Math.floor(sorted.length / 2); return sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2; }
function formatDuration(seconds) { if (seconds < 60) return `${Math.round(seconds)}s`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ${Math.round(seconds % 60)}s`; return `${Math.floor(minutes / 60)}h ${minutes % 60}m`; }
