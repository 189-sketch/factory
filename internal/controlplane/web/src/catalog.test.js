import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("a shared prompt draft keeps its original version for conflict detection", async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/agents" });
  const globals = new Map();
  for (const name of ["window", "document", "navigator", "Event", "MouseEvent", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    globals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const firstHash = "original-a";
  let saved;
  globalThis.fetch = async (url, options) => {
    if (options?.method === "PUT") {
      saved = JSON.parse(options.body);
      return { ok: false, status: 409, json: async () => ({}) };
    }
    assert.equal(url, "/api/v1/definitions");
    return { ok: true, json: async () => ({ commands: [{ name: "triage", prompt: "# Triage", hash: firstHash }, { name: "spec", prompt: "# Spec", hash: "original-b" }] }) };
  };
  const server = await createServer({ server: { middlewareMode: true }, appType: "custom" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of globals) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  const { LegacyAgentsPage: AgentsPage } = await server.ssrLoadModule("/src/catalog.jsx");
  await act(async () => root.render(React.createElement(AgentsPage, { csrfToken: "test", repositories: ["owner/a", "owner/b"], stateBackend: "local" })));
  const click = async text => {
    const element = [...document.querySelectorAll("button")].find(element => element.textContent === text);
    assert.ok(element, `button ${text} exists`);
    await act(async () => element.click());
  };
  await click("Edit prompt");
  const edit = document.querySelector("textarea");
  await act(async () => {
    Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, "value").set.call(edit, "# My unsaved draft");
    edit.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  });
  assert.equal(document.querySelector("select"), null, "shared agents have no repository selector");
  const chooseRole = async name => {
    const role = [...document.querySelectorAll(".agent-row")].find(element => element.textContent.startsWith(name));
    await act(async () => role.click());
  };
  await chooseRole("Spec");
  await chooseRole("Triage");
  await click("Edit prompt");
  assert.equal(document.querySelector("textarea").value, "# My unsaved draft");
  await click("Save prompt");
  assert.equal(Object.hasOwn(saved, "repository"), false);
  assert.equal(saved.prompt, "# My unsaved draft");
  assert.equal(saved.expected_hash, "original-a");
  assert.match(document.body.textContent, /This prompt changed elsewhere/);
  assert.equal(document.querySelector("textarea").value, "# My unsaved draft");

  let finishSave;
  globalThis.fetch = async () => new Promise(resolve => {
    finishSave = () => resolve({ ok: true, json: async () => ({ hash: "saved-a" }) });
  });
  await click("Save prompt");
  assert.ok(document.querySelector("textarea").disabled, "cannot edit while the submitted version is being saved");
  for (const role of document.querySelectorAll(".agent-row")) {
    assert.ok(role.disabled, "cannot reset the pending save by changing role");
  }
  await act(async () => finishSave());
  assert.equal(document.querySelector("textarea").disabled, false);
  assert.equal(document.querySelector("textarea").value, "# My unsaved draft");
  globalThis.fetch = async () => ({ ok:true,json:async()=>({commands:[{name:"triage",prompt:"Legacy prompt"}]}) });
  await act(async()=>root.render(React.createElement(AgentsPage,{csrfToken:"test",repositories:["repo"],stateBackend:"sqlite"})));
  await click("Edit prompt");
  assert.match(document.body.textContent,/legacy backend uses configuration files/);
  assert.equal(document.querySelector("textarea"),null);
});
