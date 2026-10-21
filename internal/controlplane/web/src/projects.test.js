import assert from "node:assert/strict";
import test from "node:test";
import React, {act} from "react";
import {createRoot} from "react-dom/client";
import {renderToStaticMarkup} from "react-dom/server";
import {JSDOM} from "jsdom";
import {createServer} from "vite";

const status={state_backend:"local",csrf_token:"csrf",repositories:["owner/first"],repository_mappings:{"owner/first":"owner/first"},jobs:[],workers:[{connected:true,repositories:["owner/first"]}],onboarding:{github:{connected:true,install_url:"https://github.com/apps/example/installations/new"},selection:{host:"local"}}};

test("Projects adds a second repository, preserves failures and refreshes the shared selection list",async context=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
 const {ProjectsPage}=await server.ssrLoadModule("/src/projects.jsx");
 const dom=new JSDOM('<div id="root"></div>',{url:"http://localhost"});
 const previous={window:global.window,document:global.document,fetch:global.fetch,IS_REACT_ACT_ENVIRONMENT:global.IS_REACT_ACT_ENVIRONMENT};Object.assign(global,{window:dom.window,document:dom.window.document,IS_REACT_ACT_ENVIRONMENT:true});
 let fail=true,refreshes=0;const requests=[];
 global.fetch=async(url,options={})=>{requests.push({url,...options});
  if(options.method==="POST")return{ok:!fail,status:fail?409:201,json:async()=>fail?{error:"Wait for running jobs to finish"}:{repository:"owner/second"}};
  return{ok:true,json:async()=>[{full_name:"owner/first",can_write:true},{full_name:"owner/second",can_write:true}]};
 };
 const root=createRoot(document.getElementById("root"));context.after(async()=>{await act(()=>root.unmount());dom.window.close();Object.assign(global,previous);});
 const button=text=>[...document.querySelectorAll("button")].find(b=>b.textContent.trim()===text);
 await act(()=>root.render(React.createElement(ProjectsPage,{status,onRefresh:async()=>refreshes++})));
 assert.match(document.body.textContent,/owner\/first/);assert.equal(requests.length,0);
 await act(()=>button("Add project").click());
 assert.deepEqual([...document.querySelectorAll(".project-editor select option")].map(o=>o.value),["owner/second"]);
 await act(()=>document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
 assert.match(document.querySelector('[role="alert"]').textContent,/Wait for running jobs/);
 assert.equal(document.querySelector("select").value,"owner/second");assert.ok(document.querySelector("form"));
 fail=false;
 await act(()=>document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
 assert.equal(refreshes,1);assert.equal(document.querySelector(".project-editor form"),null);
 assert.match(document.querySelector('[role="status"]').textContent,/owner\/second added/);
 const post=requests.find(r=>r.method==="POST");assert.equal(post.headers['X-Machinist-CSRF'],"csrf");assert.deepEqual(JSON.parse(post.body),{repository:"owner/second",path:""});
});

test("project list supports multiple roots and exposes VM update without leaking credentials",async context=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
 const {ProjectsPage}=await server.ssrLoadModule("/src/projects.jsx");
 const html=renderToStaticMarkup(React.createElement(ProjectsPage,{status:{...status,repositories:["owner/first","owner/second"],repository_mappings:{"owner/first":"owner/first","owner/second":"owner/second"},onboarding:{...status.onboarding,selection:{host:"remote"}}}}));
 assert.match(html,/owner\/first/);assert.match(html,/owner\/second/);assert.match(html,/Download VM setup for all projects/);assert.match(html,/Waiting for runner/);
 assert.doesNotMatch(html,/<details|type="checkbox"|role="switch"|private-test-token/);
});

test("project defaults stay inline, save with CSRF and start a scoped job", async context => {
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
 const {ProjectsPage}=await server.ssrLoadModule("/src/projects.jsx");
 const dom=new JSDOM('<div id="root"></div>',{url:"http://localhost"});
 const previous={window:global.window,document:global.document,fetch:global.fetch,IS_REACT_ACT_ENVIRONMENT:global.IS_REACT_ACT_ENVIRONMENT};
 Object.assign(global,{window:dom.window,document:dom.window.document,IS_REACT_ACT_ENVIRONMENT:true});
 const requests=[];let started="",refreshes=0;
 global.fetch=async(url,options)=>{requests.push({url,...options});return {ok:true,json:async()=>({})};};
 const root=createRoot(document.getElementById("root"));
 context.after(async()=>{await act(()=>root.unmount());dom.window.close();Object.assign(global,previous);});
 await act(()=>root.render(React.createElement(ProjectsPage,{status:{...status,jobs:[{id:"job-1",repository:"owner/first",task:{title:"Fix tests"},state:"running"}],onboarding:{...status.onboarding,selection:{host:"kubernetes"},project_defaults:{"owner/first":{recipe:"single",instructions:"Run tests"}}}},onStart:repo=>started=repo,onRefresh:()=>refreshes++})));
 assert.equal(document.querySelector("textarea"),null);
 await act(()=>[...document.querySelectorAll("button")].find(b=>b.textContent==="Edit defaults").click());
 assert.equal(document.querySelectorAll(".project-default-panel").length,1);
 assert.equal(document.querySelector("select").value,"single");
 assert.equal(document.querySelector('option[value="native-v1"]').disabled,true);
 assert.equal(document.querySelector("textarea").value,"Run tests");
 assert.match(document.body.textContent,/Fix tests/);assert.match(document.body.textContent,/Running/);
 await act(()=>[...document.querySelectorAll("button")].find(b=>b.textContent==="Start job").click());
 assert.equal(started,"owner/first");
 await act(()=>document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
 assert.equal(requests[0].headers["X-Machinist-CSRF"],"csrf");
 assert.deepEqual(JSON.parse(requests[0].body),{repository:"owner/first",recipe:"single",instructions:"Run tests"});
 assert.equal(refreshes,1);assert.match(document.body.textContent,/Existing jobs are unchanged/);
});
