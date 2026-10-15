import { analyticsState } from "./analytics-state.js";
import { tokenUsageSummary } from "./run-metrics.js";

import { dashboardAttention } from "./runs-board.js";
const stamp = job => Date.parse(job.updated_at || job.created_at) || 0;
const latest = jobs => [...jobs].sort((a, b) => stamp(b) - stamp(a));

export function dashboardState({ jobs = [], workers = [], automations, days = "7", loaded, error, now = new Date() }) {
  const base = analyticsState({ jobs, days, loaded, error, now });
  if (base.kind === "loading" || base.kind === "error") return base;
  const periodJobs = base.metrics.tasks;
  const active = latest(jobs.filter(job => ["running", "queued"].includes(job.state)));
  const ready = latest(jobs.filter(job => job.state === "awaiting_merge"));
  const attention = latest(jobs.filter(dashboardAttention));
  const counts = { succeeded: 0, failed: 0, cancelled: 0, unfinished: 0 };
  for (const job of periodJobs) {
    if (job.state === "succeeded") counts.succeeded++;
    else if (["failed", "timed_out"].includes(job.state)) counts.failed++;
    else if (job.state === "cancelled") counts.cancelled++;
    else counts.unfinished++;
  }
  // Calendar days in the viewer's timezone, including DST transitions. Never
  // bucket by UTC then label those dates as local days.
  const dateKey = date => `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
  const dates = Array.from({ length: Number(days) }, (_, i) => {
    const date = new Date(now);
    date.setHours(0, 0, 0, 0);
    date.setDate(date.getDate() - Number(days) + i + 1);
    return { key: dateKey(date), date, count: 0 };
  });
  const byDate = new Map(dates.map(day => [day.key, day]));
  for (const job of periodJobs) {
    const day = byDate.get(dateKey(new Date(job.created_at)));
    if (day) day.count++;
  }
  const timedRuns = base.runs.filter(run => Number.isSafeInteger(run.duration_millis) && run.duration_millis >= 0);
  return {
    ...base, active, ready, attention, dates, counts,
    running: active.filter(job => job.state === "running").length,
    queued: active.filter(job => job.state === "queued").length,
    recent: latest(periodJobs).slice(0, 6),
    timedMillis: timedRuns.length ? timedRuns.reduce((total, run) => total + run.duration_millis, 0) : null,
    timedRuns: timedRuns.length,
    usage: tokenUsageSummary(base.runs),
    onlineWorkers: workers.filter(worker => worker.connected).length,
    totalWorkers: workers.length,
    scheduled: Array.isArray(automations) ? automations.filter(a => a.cron && !a.paused).length : null,
    automationCount: Array.isArray(automations) ? automations.length : null,
  };
}
