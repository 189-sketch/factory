const zeroTime = "0001-01-01T00:00:00Z";

export function completedRuns(jobs, days, now = new Date()) {
  const since = new Date(now);
  since.setHours(0, 0, 0, 0);
  since.setDate(since.getDate() - Number(days) + 1);
  return jobs
    .flatMap((job) => job.runs)
    .filter((run) => Number.isSafeInteger(run.duration_millis) && run.duration_millis >= 0 && validDate(run.completed_at) && Date.parse(run.completed_at) >= since.getTime())
    .sort((left, right) => Date.parse(right.completed_at) - Date.parse(left.completed_at));
}

export function formatDurationMillis(milliseconds) {
  if (!Number.isSafeInteger(milliseconds) || milliseconds < 0) return "Unavailable";
  if (milliseconds < 1000) return `${milliseconds}ms`;
  const seconds = Math.floor(milliseconds / 1000);
  const remainder = milliseconds % 1000;
  if (seconds < 60) return remainder ? `${seconds}.${String(remainder).padStart(3, "0").replace(/0+$/, "")}s` : `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return `${minutes}m ${remainingSeconds}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m ${remainingSeconds}s`;
}

export function formatTokenUsage(value) {
  return Number.isSafeInteger(value) && value >= 0 ? value.toLocaleString() : "Unavailable";
}

function validDate(value) { return Boolean(value && value !== zeroTime && Number.isFinite(Date.parse(value))); }
