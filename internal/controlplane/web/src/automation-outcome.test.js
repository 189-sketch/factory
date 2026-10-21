import assert from "node:assert/strict";
import test from "node:test";
import { automationNextRun, automationOutcome, outcomeText } from "./automation-outcome.js";

const automation = { last_job: "latest" };
const job = { id: "latest", state: "succeeded", runs: [{ summary: "No changes needed after reviewing dependencies.", report: "All dependencies were checked." }] };

test("outcomes belong to the selected latest job and latest attempt, never an older success", () => {
  assert.equal(automationOutcome({}, job).text, "Never run");
  assert.match(automationOutcome(automation).text, /unavailable/);
  assert.match(automationOutcome(automation, { ...job, id: "older" }).text, /unavailable/);
  const runs = [{ summary: "Delivered old code" }, { state: "failed", error: "Runner credentials expired. Sign in again." }];
  assert.equal(automationOutcome(automation, { ...job, state: "failed", runs }).text, "Failed: Runner credentials expired. Sign in again.");
  assert.equal(automationOutcome(automation, { ...job, state: "running", runs: [...runs, { state: "running" }] }).text, "Run in progress.");
  assert.equal(automationOutcome(automation, { ...job, runs: [...runs, { summary: "Retry checked dependencies; nothing to update." }] }).text, "Retry checked dependencies; nothing to update.");
});

test("no-change reports and connection checks do not claim delivered code", () => {
  assert.equal(automationOutcome(automation, job).text, job.runs[0].summary);
  assert.equal(automationOutcome(automation, { ...job, workflow: { factory: { pr_url: "https://github.com/owner/repo/pull/2" } } }).link.label, "View report");
  assert.match(automationOutcome(automation, { ...job, runs: [{ summary: "PONG" }] }).text, /Connection check.*no recurring work delivered/);
  assert.match(automationOutcome(automation, { ...job, runs: [] }).text, /no outcome summary/);
});

test("holds suppress summaries, reports and external delivery links", () => {
  const result = automationOutcome(automation, { ...job, publication_hold: "private reason", workflow: { factory: { pr_url: "https://github.com/owner/repo/pull/2" } } });
  assert.equal(result.held, true);
  assert.match(result.text, /Publication held private/);
  assert.equal(result.link.href, "#/runs/latest");
  assert.doesNotMatch(JSON.stringify(result), /dependencies|private reason|github.com/);
});

test("PR links require a valid GitHub pull request and a completed or merge-ready job", () => {
  for (const url of ["https://github.com/owner/repo/pull/2", "javascript:alert(1)", "https://github.com/owner/repo/issues/2", "https://github.com.evil.test/owner/repo/pull/2", "https://user@github.com/owner/repo/pull/2"]) {
    const result = automationOutcome(automation, { ...job, runs: [{ summary: "Fixed dependency versions.", report: "Updated the lockfile." }], workflow: { factory: { pr_url: url } } });
    assert.equal(result.link.label, url === "https://github.com/owner/repo/pull/2" ? "View PR" : "View report");
  }
  assert.equal(automationOutcome(automation, { ...job, state: "failed", workflow: { factory: { pr_url: "https://github.com/owner/repo/pull/2" } } }).link.label, "View run");
});

test("errors are readable evidence with bounded text, not raw JSON", () => {
  assert.equal(outcomeText('{"error":"Authenticate the runner again."}'), "Authenticate the runner again.");
  assert.equal(outcomeText('{"events":[1,2]}'), "");
  assert.equal(outcomeText('{broken JSON'), "");
  assert.equal(outcomeText("x".repeat(300)).length, 240);
  assert.match(automationOutcome(automation, { ...job, state: "failed", runs: [{ error: '{"events":[]}' }] }).text, /inspect its logs/);
});

test("next run only uses valid server data for an enabled schedule", () => {
  const a = { cron: "0 9 * * *", next_due: "2026-10-22T09:00:00Z" };
  assert.equal(automationNextRun(a), a.next_due);
  for (const patch of [{ cron: "" }, { paused: true }, { next_due: undefined }, { next_due: "invalid" }]) assert.equal(automationNextRun({ ...a, ...patch }), null);
});
