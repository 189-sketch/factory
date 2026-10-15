import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

test("active work shows real phases and distinguishes a live runner from a delayed heartbeat", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { RunActivity } = await server.ssrLoadModule("/src/task-detail.jsx");
  const now = Date.parse("2026-10-15T20:00:00Z");
  const run = { phase: "checking_pr", worker_name: "cluster-runner", started_at: "2026-10-15T19:59:00Z", heartbeat_at: "2026-10-15T19:59:56Z" };
  const render = value => renderToStaticMarkup(React.createElement(RunActivity, { run: value, now }));
  const active = render(run);
  assert.match(active, /Checking the pull request and CI/);
  assert.match(active, /Runner connected/);
  assert.match(active, /heartbeat 4s ago/);
  assert.match(active, /Elapsed 1m/);
  assert.doesNotMatch(active, /The agent is working/);
  const delayed = render({ ...run, heartbeat_at: "2026-10-15T19:59:10Z" });
  assert.match(delayed, /Runner status delayed/);
  assert.match(delayed, /50s ago/);
  assert.doesNotMatch(delayed, /Runner connected/);
  assert.match(render({}), /Waiting for runner status/);
  assert.match(render({ ...run, phase: undefined }), /Runner active/);
  assert.match(render({ ...run, heartbeat_at: "2026-10-15T19:59:38Z" }), /Runner status delayed/);
  assert.match(render({ ...run, phase: "starting_sandbox" }), /Starting the Kubernetes sandbox/);
});
