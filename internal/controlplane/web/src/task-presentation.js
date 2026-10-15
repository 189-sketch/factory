export function taskPresentation(job) {
  const runs = job.runs || [];
  const latest = runs.at(-1);
  // Approval belongs to the next stage; its result belongs to the exact
  // producing attempt bound by the server, never an arbitrary older success.
  const result = job.state === "awaiting_approval"
    ? runs.find(run => run.id === latest?.reviewed_run_id)
    : latest;
  const current = job.workflow?.current_step ?? 0;
  return {
    result,
    history: runs.filter(run => run.id !== result?.id && run.id !== latest?.id),
    stages: (job.workflow?.steps || []).map((name, index) => {
      if (!job.workflow?.factory) return {
        name,
        current: index === current && job.state !== "succeeded",
        complete: index < current || job.state === "succeeded",
      };
      const attempts = runs.filter(run => run.command === name);
      const active = index === current && !["succeeded", "awaiting_merge"].includes(job.state);
      const skipped = !attempts.length && (index < current || job.state === "succeeded");
      const complete = name === "review"
        ? job.workflow.factory.verdict === "accepted" && ["awaiting_merge", "succeeded"].includes(job.state)
        : !active && attempts.some(run => run.outcome === "complete");
      return { name, current: active, complete, skipped };
    }),
  };
}
