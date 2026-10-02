import assert from "node:assert/strict";
import { AutoPresentedHTMLGate } from "../lib/autoHTML";
import type { WireEvent } from "../lib/types";

const result = (id: string, files: string[], extra: Partial<WireEvent> = {}): WireEvent => ({
  kind: "tool_result",
  tool: { id, name: "present", readOnly: true, presentedFiles: files.map((path) => ({ path })) },
  ...extra,
});

const gate = new AutoPresentedHTMLGate();
gate.observeTurnStart("tab-a");
assert.equal(gate.claim(result("p1", ["notes.md", "INDEX.HTML"]), "tab-a", "tab-a", 1, "turn-1")?.file.path, "INDEX.HTML");
assert.equal(gate.claim(result("p2", ["second.html"]), "tab-a", "tab-a", 1, "turn-1"), null, "only the first HTML in a turn is automatic");
assert.equal(gate.claim(result("background", ["background.html"]), "tab-b", "tab-a", 1, "turn-1"), null, "a background task never steals focus");
assert.equal(gate.claim(result("failed", ["bad.html"], { tool: { id: "failed", name: "present", readOnly: true, err: "failed", presentedFiles: [{ path: "bad.html" }] } }), "tab-a", "tab-a", 1, "turn-1"), null);
gate.observeTurnStart("tab-a");
assert.equal(gate.claim(result("p3", ["next.htm"]), "tab-a", "tab-a", 1, "turn-2")?.file.path, "next.htm", "a new turn may auto preview again");
assert.equal(gate.claim(result("history", ["old.html"]), "tab-a", "tab-a", 2, "turn-2")?.file.path, "old.html", "a changed session generation has an independent gate");
console.log("PASS automatic presented HTML gate");
