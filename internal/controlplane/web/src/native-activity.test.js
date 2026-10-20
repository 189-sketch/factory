import test from "node:test";
import assert from "node:assert/strict";
import { reduceActivity } from "./native-activity.js";
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
