// Run: tsx src/__tests__/mock-fork-fixture.test.ts
// The browser-dev fixture must resolve through the same identity the transcript
// renders: a dev source that answers a fork read has to name the answer message
// the user is looking at, or the entry can never be enabled in `pnpm dev`.
import { JSDOM } from "jsdom";

let passed = 0;
let failed = 0;
function ok(value: unknown, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nmock fork fixture");

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/?mock=demo" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;

const { app } = await import("../lib/bridge");
const { historyMessagesToItems } = await import("../lib/historyItems");

const tabs = await app.ListTabs();
const tab = tabs.find((candidate) => candidate.active) ?? tabs[0];
ok(Boolean(tab?.id), "the dev mock lists an active tab");

const history = await app.HistoryForTab(tab!.id);
const { items } = historyMessagesToItems(history, "h");
const answers = items.filter((item) => item.kind === "assistant" && item.text.trim());
ok(answers.length > 0, "the dev source renders assistant answers");

const set = await app.ForkTargetsForTab(tab!.id);
ok(Array.isArray(set.targets) && set.targets.length > 0, "the dev source answers with fork targets");
ok(set.verifiable === true, "a dev source with message identity proves its boundaries");

const keys = new Set(items.map((item) => item.id));
const matched = set.targets.filter((target) => target.messageId && keys.has(`m:${target.messageId}`));
ok(matched.length > 0, "a target names a message the transcript actually renders");
ok(matched.every((target) => target.available), "every matched dev target is forkable");
ok(matched.every((target) => target.messageId === `m:${target.messageId}`.slice(2)), "the target keeps the raw message id the transcript keys on");
ok(set.targets.some((target) => !target.available && target.reason === "turn_open") === false,
  "an idle dev source offers no open turn");

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
