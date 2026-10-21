// Only render readable evidence, never a serialized runner response.
export function outcomeText(value, depth = 0) {
  if (typeof value !== "string" || depth > 2) return "";
  const text = value.trim().replace(/^```(?:json)?\s*\n?([\s\S]*?)\n?```$/i, "$1").trim();
  if (/^[{[]/.test(text)) {
    try {
      const data = JSON.parse(text);
      return outcomeText(data.error || data.summary || data.message, depth + 1);
    } catch { return ""; }
  }
  const plain = text.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1").replace(/[`*#]/g, "").replace(/\s+/g, " ").trim();
  return plain.length > 240 ? `${plain.slice(0, 239)}…` : plain;
}

export function automationNextRun(a) {
  return a.cron && !a.paused && a.next_due && Number.isFinite(Date.parse(a.next_due)) ? a.next_due : null;
}

function pullURL(value) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.hostname === "github.com" && !url.username && !url.password && !url.port && /^\/[^/]+\/[^/]+\/pull\/[1-9]\d*$/.test(url.pathname) ? url.href : null;
  } catch { return null; }
}

export function automationOutcome(a, job) {
  if (!a.last_job) return { text: "Never run" };
  const runLink = { href: `#/runs/${encodeURIComponent(a.last_job)}`, label: "View run" };
  if (!job || job.id !== a.last_job) return { text: "Last run details unavailable.", link: runLink };
  if (job.publication_hold) return { text: "Publication held private. Review the hold in run history.", link: runLink, held: true };
  const latest = job.runs?.at(-1);
  const summary = outcomeText(latest?.summary);
  const report = outcomeText(latest?.report);
  const active = { queued: "Run queued.", running: "Run in progress." }[job.state];
  if (active) return { text: active, link: runLink };
  if (job.state === "failed" || latest?.state === "failed") {
    const evidence = outcomeText(latest?.error) || summary || report;
    return { text: evidence ? `Failed: ${evidence}` : "Run failed. Open the run to inspect its logs before retrying.", link: runLink };
  }
  const connectionCheck = /^pong(?:[.!]?$|\s*[—–:]\s*|\s+-\s+)/i.test(summary || report);
  if (connectionCheck) return { text: "Connection check returned PONG; no recurring work delivered.", link: runLink };
  const text = summary || report || ({ succeeded: "Run completed; no outcome summary recorded.", cancelled: "Run cancelled.", blocked: "Run blocked. Open the run for details.", awaiting_approval: "Specification awaiting approval.", awaiting_merge: "PR awaiting merge." }[job.state] || "No outcome summary recorded.");
  const pr = ["succeeded", "awaiting_merge"].includes(job.state) ? pullURL(job.workflow?.factory?.pr_url) : null;
  return { text, link: pr ? { href: pr, label: "View PR", external: true } : { ...runLink, label: latest?.report ? "View report" : "View run" } };
}
