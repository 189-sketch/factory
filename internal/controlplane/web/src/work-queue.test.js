import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
import { workQueueState, queueSummary } from "./dashboard-state.js";
const job = (id, state, extra = {}) => ({ id, state, repository: "owner/one", task: { title: id }, runs: [], ...extra });

test("queues are exclusive, preserve holds and dismissed attention, and cap outcomes", () => {
  const jobs = [job("hold", "awaiting_merge", { publication_hold: "private", inbox_cleared: true }), job("review", "awaiting_merge"), job("running", "running"), job("queued", "queued"), job("blocked", "blocked"), job("failed", "failed"), job("timeout", "timed_out"), job("interrupted", "interrupted"), job("approval", "awaiting_approval"), job("dismissed", "failed", { inbox_cleared: true }), ...Array.from({length: 8}, (_, i) => job(`done${i}`, "succeeded"))];
  const state = workQueueState({jobs, loaded:true});
  assert.deepEqual(state.attention.map(j=>j.id), ["hold", "blocked", "failed", "timeout", "interrupted", "approval"]);
  assert.deepEqual(state.ready.map(j=>j.id), ["review"]);
  assert.deepEqual(state.active.map(j=>j.id), ["running", "queued"]);
  assert.equal(state.recent.length, 6);
  const ids = [...state.attention, ...state.ready, ...state.active, ...state.recent].map(j=>j.id);
  assert.equal(new Set(ids).size, ids.length);
});

test("summaries use brief recorded evidence without reports, JSON, or old workflow results", () => {
  assert.equal(queueSummary(job("a", "blocked", {runs:[{summary:"Need a decision.\n\nFULL REPORT"}]})), "Need a decision.");
  assert.equal(queueSummary(job("a", "failed", {runs:[{error:"Permission denied",summary:"Started"}]})), "Permission denied");
  for (const summary of ['{"summary":"secret"}', '[1,2]', '```json\n{}']) assert.equal(queueSummary(job("a", "succeeded", {runs:[{summary}]})), "Work completed; no summary recorded.");
  assert.equal(queueSummary(job("approval", "awaiting_approval", {runs:[{id:"old",summary:"Outdated"},{id:"bound",summary:"Please approve the plan"},{reviewed_run_id:"bound"}]})), "Please approve the plan");
  assert.ok(queueSummary(job("a", "succeeded", {runs:[{summary:"x".repeat(200)}]})).length <= 160);
  assert.equal(queueSummary(job("a", "queued", {workflow:{},runs:[{summary:"Previous success"},{state:"queued"}]})), "Queued; waiting to start.");
  assert.match(queueSummary(job("a", "succeeded", {publication_hold:"secret",runs:[{summary:"private"}]})), /Publication held private/);
});

test("project filtering applies to every queue and retains missing projects", () => {
  const jobs = ["blocked", "running", "awaiting_merge", "succeeded"].flatMap(state => [job(`one-${state}`, state), job(`two-${state}`,state,{repository:"owner/two"})]);
  const result = workQueueState({jobs, repositories:["owner/one"],project:"owner/two",loaded:true});
  for (const key of ["attention","active","ready","recent"]) assert.deepEqual(result[key].map(j=>j.repository),["owner/two"]);
  assert.equal(result.projects[1].label,"owner/two (unavailable)");
  assert.equal(workQueueState({jobs,project:"removed",loaded:true}).empty,true);
  assert.equal(workQueueState({jobs:[job("none","running",{repository:""})],project:"__no_project__",loaded:true}).active.length,1);
  assert.equal(workQueueState({jobs,loaded:true,error:"offline"}).kind,"error");
});

test("dashboard project control filters rendered queues and supports disconnected and empty states", async context => {
  const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
  const {Dashboard}=await server.ssrLoadModule("/src/analytics.jsx");
  const dom=new JSDOM('<div id="root"></div>',{url:"http://localhost"});
  const previous={window:global.window,document:global.document,IS_REACT_ACT_ENVIRONMENT:global.IS_REACT_ACT_ENVIRONMENT};
  Object.assign(global,{window:dom.window,document:dom.window.document,IS_REACT_ACT_ENVIRONMENT:true});
  const root=createRoot(document.getElementById("root"));context.after(async()=>{await act(()=>root.unmount());dom.window.close();Object.assign(global,previous);});
  let starts=0;
  const jobs=[job("hold","awaiting_merge",{publication_hold:"private"}),job("other","running",{repository:"owner/two"})];
  await act(()=>root.render(React.createElement(Dashboard,{jobs,repositories:["owner/one","empty"],connected:false,loaded:true,onNewRun:()=>starts++})));
  assert.match(document.body.textContent,/GitHub is disconnected/);
  for(const title of ["Needs you","Working now","Ready to review","Recent outcomes"]) assert.ok(document.querySelector(`section[aria-label="${title}"]`));
  assert.equal(document.querySelectorAll('a[href="#/runs/hold"]').length,2);
  assert.match(document.body.textContent,/Review hold/);
  assert.equal(document.querySelector('details, summary, svg[role="img"]'),null);
  const select=document.querySelector('select');
  await act(()=>{select.value="owner/two";select.dispatchEvent(new dom.window.Event("change",{bubbles:true}));});
  assert.equal(document.querySelector('a[href="#/runs/hold"]'),null);
  assert.ok(document.querySelector('a[href="#/runs/other"]'));
  await act(()=>{select.value="empty";select.dispatchEvent(new dom.window.Event("change",{bubbles:true}));});
  assert.match(document.body.textContent,/No work for this project/);
  await act(()=>document.querySelector('button').click());assert.equal(starts,1);
});

test("queue rendering keeps reports out of rows and exposes loading and errors", async context => {
  const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
  const {Dashboard}=await server.ssrLoadModule("/src/analytics.jsx");
  const jobs=[job("recorded","succeeded",{runs:[{summary:"Fixed the regression.\n\nFULL_REPORT",report:"FULL_REPORT"}]})];
  const html=renderToStaticMarkup(React.createElement(Dashboard,{jobs,loaded:true}));
  assert.match(html,/Fixed the regression/);
  assert.doesNotMatch(html,/FULL_REPORT|<details|<summary|Reported tokens/);
  assert.match(renderToStaticMarkup(React.createElement(Dashboard,{jobs,loaded:false})),/Loading your workspace/);
  const failed=renderToStaticMarkup(React.createElement(Dashboard,{jobs,loaded:true,error:"offline"}));
  assert.match(failed,/Dashboard unavailable/);assert.doesNotMatch(failed,/Fixed the regression/);
});

test("known runner failures have readable summaries without implying current system health", () => {
 assert.match(queueSummary(job("bad-result","failed",{runs:[{error:"sandbox agent: json: cannot unmarshal object into Go struct field FactoryResult.factory.report of type string"}]})),/result Machinist could not read/);
 assert.match(queueSummary(job("expired","failed",{runs:[{error:"Google Cloud login has expired or is unavailable; run gcloud auth login"}]})),/unavailable for this attempt/);
});
