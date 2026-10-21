import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("project start scopes composer and explicit job choices override defaults", async context => {
 const dom = new JSDOM('<div id="root"></div>', {url:"http://localhost/#/repositories"});
 const previous = new Map();
 for (const name of ["window","document","navigator","localStorage","Event","MouseEvent","fetch","IS_REACT_ACT_ENVIRONMENT"]) {
  previous.set(name,Object.getOwnPropertyDescriptor(globalThis,name));
  Object.defineProperty(globalThis,name,{configurable:true,writable:true,value:dom.window[name]});
 }
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 const status = {state_backend:"local",csrf_token:"csrf",jobs:[],workers:[],commands:[],workflows:["factory"],repositories:["owner/first","owner/second"],onboarding:{required:false,selection:{host:"local"},project_defaults:{"owner/second":{recipe:"single",instructions:"Test this project"}}}};
 let submitted;
 globalThis.fetch = async (url, options) => {
  if (url === "/api/v1/jobs") { submitted = JSON.parse(options.body); return {ok:true,json:async()=>({id:"new-job"})}; }
  return {ok:true,json:async()=>status};
 };
 const server = await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});
 let root;
 context.after(async()=>{await act(()=>root?.unmount());await server.close();dom.window.close();for(const [name,descriptor] of previous){if(descriptor)Object.defineProperty(globalThis,name,descriptor);else delete globalThis[name];}});
 await act(async()=>{root=(await server.ssrLoadModule("/src/main.jsx")).appRoot;});
 await act(()=>[...document.querySelectorAll(".project-card")][1].querySelector(".project-work button").click());
 await act(async()=>{await new Promise(resolve=>setTimeout(resolve,20));});
 const composer = document.querySelector(".run-composer");
 const selects = composer.querySelectorAll("select");
 assert.equal(selects[0].value,"owner/second");
 assert.match(selects[1].selectedOptions[0].textContent,/Single agent/);
 const areas = composer.querySelectorAll("textarea");
 assert.equal(areas[1].value,"Test this project");
 function fill(element,value) {
  const setter=Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype,"value").set;
  setter.call(element,value); element.dispatchEvent(new dom.window.Event("input",{bubbles:true}));
 }
 await act(()=>{selects[1].value="factory";selects[1].dispatchEvent(new dom.window.Event("change",{bubbles:true}));fill(areas[0],"Implement the change");fill(areas[1],"");});
 await act(()=>composer.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
 assert.equal(submitted.repository,"owner/second");
 assert.equal(submitted.recipe,"factory");
 assert.equal(submitted.project_instructions,"");
 assert.equal(submitted.spec,"Implement the change");
});
