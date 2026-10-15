import assert from "node:assert/strict";
import test from "node:test";
import { taskPresentation } from "./task-presentation.js";

test("review shows the bound revision, with earlier attempts in history", () => {
  const runs = [{ id: "original" }, { id: "old-gate" }, { id: "revised" }, { id: "gate", reviewed_run_id: "revised" }];
  const view = taskPresentation({ state: "awaiting_approval", runs, workflow: { steps: ["plan", "build"], current_step: 1 } });
  assert.equal(view.result.id, "revised");
  assert.deepEqual(view.history.map(r => r.id), ["original", "old-gate"]);
  assert.deepEqual(view.stages.map(s => [s.complete, s.current]), [[true, false], [false, true]]);
});

test("initial approval has no fabricated result; running revision shows itself", () => {
  assert.equal(taskPresentation({ state: "awaiting_approval", runs: [{ id: "gate" }] }).result, undefined);
  const view = taskPresentation({ state: "running", runs: [{ id: "original" }, { id: "revision" }] });
  assert.equal(view.result.id, "revision");
  assert.equal(view.history.length, 1);
});

test("successful task marks all stages complete", () => {
  const view = taskPresentation({ state: "succeeded", runs: [{ id: "done" }], workflow: { steps: ["plan", "build"], current_step: 1 } });
  assert.ok(view.stages.every(s => s.complete && !s.current));
});

test("factory skips absent stages without claiming they completed", () => {
  const workflow = { steps: ["triage", "spec", "implement", "review"], current_step: 2, factory: {} };
  const view = taskPresentation({ state: "running", workflow, runs: [{ command: "triage", outcome: "complete" }, { command: "implement", state: "running" }] });
  assert.deepEqual(view.stages.map(stage => [stage.complete, stage.current, stage.skipped]), [[true, false, false], [false, false, true], [false, true, false], [false, false, false]]);
});

test("factory review completion binds accepted handoff and remains incomplete during repair", () => {
  const workflow = { steps: ["triage", "spec", "implement", "review"], current_step: 2, factory: { verdict: "revision_needed" } };
  const runs = [{ command: "triage", outcome: "complete" }, { command: "implement", outcome: "complete" }, { command: "review", outcome: "complete" }, { command: "implement", state: "running" }];
  const repair = taskPresentation({ state: "running", workflow, runs });
  assert.equal(repair.stages[2].complete, false);
  assert.equal(repair.stages[3].complete, false);
  const handoff = taskPresentation({ state: "awaiting_merge", workflow: { ...workflow, current_step: 3, factory: { verdict: "accepted" } }, runs });
  assert.equal(handoff.stages[3].complete, true);
  assert.equal(handoff.stages[3].current, false);
  assert.equal(handoff.stages[1].skipped, true);
});


test("an informational answer completes only triage and skips unused factory stages", () => {
 const result=taskPresentation({state:"succeeded",workflow:{steps:["triage","spec","implement","review"],current_step:0,factory:{}},runs:[{id:"reply",command:"triage",outcome:"complete"}]});
 assert.equal(result.stages[0].complete,true);
 assert.ok(result.stages.slice(1).every(stage=>stage.skipped && !stage.complete));
});
