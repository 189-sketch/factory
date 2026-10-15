import React, { useId, useState } from "react";
import { taskPresentation } from "./task-presentation.js";

const labels = { triage: "Triage", spec: "Spec", implement: "Build", review: "Review" };
export const stageLabel = name => labels[name] || String(name || "").replaceAll("_", " ").replaceAll("-", " ").replace(/^./, letter => letter.toUpperCase());

export function stageProgress(job) {
  return taskPresentation(job).stages.map(stage => ({
    label: job.recipe === "single" ? "Agent" : job.recipe === "answer" ? "Answer" : stageLabel(stage.name),
    state: stage.complete ? "complete" : stage.skipped ? "skipped" : stage.current ? job.state === "running" ? "running" : ["failed", "blocked", "interrupted", "cancelled"].includes(job.state) ? "stopped" : "ready" : "waiting",
  }));
}

export function StageProgress({ job }) {
  const tooltipID = useId();
  const [dismissed,setDismissed] = useState(false);
  const stages = stageProgress(job);
  if (!stages.length) return <span className="text-muted-foreground">—</span>;
  const stateName = state => ({stopped:"Needs attention",ready:"Next",complete:"Complete",skipped:"Skipped",running:"Running",waiting:"Waiting"}[state]);
  return <span className="stage-progress" role="img" aria-label="Stage progress" aria-describedby={tooltipID} tabIndex={0} data-dismissed={dismissed} onFocus={()=>setDismissed(false)} onMouseEnter={()=>setDismissed(false)} onKeyDown={event=>{if(event.key==="Escape")setDismissed(true);}}>
    {stages.map((stage,index) => <span key={`${index}:${stage.label}`} className={`stage-dot stage-dot-${stage.state}`} title={`${stage.label}: ${stateName(stage.state)}`} aria-hidden="true" />)}
    <span id={tooltipID} role="tooltip" className="stage-tooltip">{stages.map((stage,index) => <span key={index}>{stage.label}: <b>{stateName(stage.state)}</b></span>)}</span>
  </span>;
}
