import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";

test("stage dots distinguish skipped planning, active build, and independent review", async context => {
  const server = await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});
  context.after(()=>server.close());
  const {stageProgress,stageLabel} = await server.ssrLoadModule("/src/stage-progress.jsx");
  assert.equal(stageLabel("implement"),"Build");
  assert.equal(stageLabel("spec"),"Spec");
  const job={state:"running",workflow:{steps:["triage","spec","implement","review"],current_step:2,factory:{}},runs:[{command:"triage",outcome:"complete"},{command:"implement",state:"running"}]};
  assert.deepEqual(stageProgress(job),[{label:"Triage",state:"complete"},{label:"Spec",state:"skipped"},{label:"Build",state:"running"},{label:"Review",state:"waiting"}]);
  job.state="awaiting_merge";job.workflow.current_step=3;job.workflow.factory.verdict="accepted";job.runs.push({command:"review",outcome:"complete"});job.runs[1].outcome="complete";
  assert.equal(stageProgress(job).at(-1).state,"complete");
  assert.deepEqual(stageProgress({recipe:"answer",state:"succeeded",workflow:{steps:["triage"],current_step:0,factory:{}},runs:[{command:"triage",outcome:"complete"}]}),[{label:"Answer",state:"complete"}]);
});
