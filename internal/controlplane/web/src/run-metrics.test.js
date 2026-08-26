import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { completedRuns, formatDurationMillis, formatTokenUsage, runDetails } from "./run-metrics.js";

test("completedRuns keeps measured duration and optional reported token usage", () => {
  const jobs = [{ runs: [
    { id: "run_reported", agent: "build", completed_at: "2026-08-25T12:00:00Z", duration_millis: 1250, token_usage: "4321" },
    { id: "run_missing", agent: "review", completed_at: "2026-08-25T11:00:00Z", duration_millis: 500 },
    { id: "run_unmeasured", agent: "plan", completed_at: "2026-08-25T10:00:00Z" },
  ] }];
  const runs = completedRuns(jobs, "7", new Date("2026-08-25T15:00:00Z"));
  assert.deepEqual(runs.map((run) => run.id), ["run_reported", "run_missing"]);
  assert.equal(runs[0].token_usage, "4321");
  assert.equal(runs[1].token_usage, undefined);
});

test("formatters distinguish explicitly reported zero from unavailable usage", () => {
  assert.equal(formatDurationMillis(1250), "1.25s");
  assert.equal(formatTokenUsage("0"), "0");
  assert.equal(formatTokenUsage("9007199254740993"), "9,007,199,254,740,993");
  assert.equal(formatTokenUsage(9007199254740992), "Unavailable");
  assert.equal(formatTokenUsage(undefined), "Unavailable");
});

test("runDetails always surfaces the executor, even when a worker has claimed the run", () => {
  const claimed = { executor: "codex", worker_name: "my-macbook", model: "sonnet", duration_millis: 1250, completed_at: "2026-08-25T12:00:00Z", token_usage: "4321" };
  assert.equal(runDetails(claimed), "codex · my-macbook · sonnet · 1.25s · 4,321 tokens");

  const unassigned = { executor: "claude", model: "opus" };
  assert.equal(runDetails(unassigned), "claude · opus");
});

test("analytics presents only duration and reported token usage metrics", async () => {
  const source = await readFile(new URL("./analytics.jsx", import.meta.url), "utf8");
  assert.match(source, /Duration/);
  assert.match(source, /Reported token usage/);
  for (const label of ["Executions", "Success rate", "Active", "Outcomes", "Executions over time", "Median duration"]) {
    assert.doesNotMatch(source, new RegExp(label));
  }
});

test("main.jsx no longer drops the executor in favor of the worker name", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /run\.worker_name\s*\|\|\s*run\.executor/);
  assert.match(source, /runDetails/);
});
