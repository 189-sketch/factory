import test from "node:test";
import assert from "node:assert/strict";
import { reduceActivity, pollRunActivity } from "./native-activity.js";
const activity = (id, status, time) => ({ type: "agent.activity", time, message: JSON.stringify({ native_id: id, name: "Reader", status }) });
test("parallel identities, primary log and honest interruption", () => {
 const events = [activity("a", "running", "start"), activity("b", "running", "later"), activity("a", "failed", "end"), { type: "agent.message", message: JSON.stringify({ primary: false, text: "CHILD" }) }, { type: "agent.message", message: JSON.stringify({ primary: true, text: "PARENT" }) }];
 const result = reduceActivity(events, "running");
 assert.equal(result.children.length, 2);
 assert.equal(result.children[0].status, "failed");
 assert.equal(result.children[0].started_at, "start");
 assert.deepEqual(result.logs, ["PARENT"]);
 assert.equal(reduceActivity(events, "interrupted").children[1].status, "interrupted");
 assert.equal(reduceActivity(events, "running").children[1].status, "running");
});

test("reports and delegation map are readable, accessible, and free of disclosures", async context => {
  const { createServer } = await import("vite");
  const React = await import("react");
  const { renderToStaticMarkup } = await import("react-dom/server");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { ActivityTimeline, PrimaryLog, ExecutionGraph } = await server.ssrLoadModule("/src/native-activity.jsx");
  const children = [{ native_id: "reader", name: "Read go.mod", status: "completed", started_at: "2026-10-20T10:00:00Z", updated_at: "2026-10-20T10:00:04Z", summary: "- **Module:** `example/project`\n- **Go:** 1.26\n\n<script>unsafe()</script>" }];
  const html = renderToStaticMarkup(React.createElement(ActivityTimeline, { children }));
  assert.match(html, /1 of 1 completed/);
  assert.match(html, /<strong>Module:<\/strong>/);
  assert.match(html, /<code>example\/project<\/code>/);
  assert.match(html, /Completed/);
  assert.doesNotMatch(html, /<details|<summary|<script/);
  const log = renderToStaticMarkup(React.createElement(PrimaryLog, { logs: [JSON.stringify({ outcome: "complete", summary: "Both agents finished." })] }));
  assert.match(log, /Both agents finished\./);
  assert.doesNotMatch(log, /&quot;outcome&quot;|<details/);
  const empty = renderToStaticMarkup(React.createElement(ActivityTimeline, { children: [], loaded: true, runState: "running" }));
  assert.match(empty, /The primary agent is working/);
  const graph = renderToStaticMarkup(React.createElement(ExecutionGraph, { activities: children, runState: "succeeded", idPrefix: "overview" }));
  assert.match(graph, /Execution map/);
  assert.match(graph, /Primary agent/);
  assert.match(graph, /href="#overview-agent-reader"/);
  assert.match(graph, /Connections show delegation/);
  assert.equal(renderToStaticMarkup(React.createElement(ExecutionGraph, { activities: [], runState: "running" })), "");
});

test("native run separates outcome, logs, history and diagnostics", async context => {
  const { createServer } = await import("vite");
  const React = await import("react");
  const { renderToStaticMarkup } = await import("react-dom/server");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { TaskDetail } = await server.ssrLoadModule("/src/task-detail.jsx");
  const job = { id: "native", recipe: "native-v1", state_backend: "local", state: "succeeded", repository: "example/repo", task: { title: "Read the project", spec: "Read only" }, workflow: { factory: {} }, runs: [{ id: "first", state: "failed", summary: "invalid character 'B' looking for beginning of value" }, { id: "second", state: "succeeded", executor: "claude-native", summary: "Two readers finished." }] };
  const html = renderToStaticMarkup(React.createElement(TaskDetail, { job, loaded: true }));
  assert.match(html, /Run outcome/);
  assert.match(html, /Overview/);
  assert.match(html, /Attempts \(2\)/);
  assert.match(html, /required result format/);
  assert.doesNotMatch(html, /<details|<summary|invalid character/);
});

const rawEvent = object => ({type:"process.output",stream:"stdout",encoding:"base64",data:Buffer.from(JSON.stringify(object)+"\n").toString("base64")});
test("historical Claude graph excludes shell tasks and restores delegation names",()=>{
 const events=[rawEvent({type:"system",subtype:"task_started",task_type:"local_agent",task_id:"reader",description:"Review code"}),rawEvent({type:"system",subtype:"task_started",task_type:"local_bash",task_id:"shell",description:"npm test"}),activity("reader","running","start"),activity("shell","completed","later"),{...activity("reader","completed","end"),message:JSON.stringify({native_id:"reader",name:"Running git diff",status:"completed",summary:"Accepted"})}];
 const result=reduceActivity(events,"succeeded");assert.equal(result.children.length,1);assert.equal(result.children[0].name,"Review code");assert.equal(result.children[0].summary,"Accepted");
});
test("historical Codex excludes the primary and interactions preserve child state",()=>{
 const events=[rawEvent({id:4,result:{thread:{id:"root"}}}),rawEvent({method:"item/started",params:{item:{type:"subAgentActivity",kind:"started",agentThreadId:"child",agentPath:"/root/reviewer"}}}),activity("child","running","start"),activity("child","unknown","interacted"),activity("root","unknown","interacted")];
 const result=reduceActivity(events,"running");assert.equal(result.children.length,1);assert.equal(result.children[0].status,"running");assert.equal(result.children[0].name,"/root/reviewer");
});

test("cancelled activity continues polling until cleanup acknowledgment",()=>{
 assert.equal(pollRunActivity({state:"cancelled",phase:"stopping"}),true);
 assert.equal(pollRunActivity({state:"cancelled",completed_at:"2026-10-21T10:00:00Z"}),false);
 assert.equal(pollRunActivity({state:"cancelled"}),false);
});
