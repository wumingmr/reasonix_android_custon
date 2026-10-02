import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { LocaleProvider } from "../lib/i18n";
import { ModifiedFiles, PresentedFiles } from "../components/PresentedFiles";
import type { PresentedFileView } from "../lib/chatViewSource";
import type { WireCompletionSummary } from "../lib/types";

const files: PresentedFileView[] = Array.from({ length: 5 }, (_, index) => ({
  path: `output/file-${index + 1}.html`,
  description: `Preview ${index + 1}`,
  toolCallId: `present-${index + 1}`,
}));

const markup = renderToStaticMarkup(
  <LocaleProvider><PresentedFiles files={files} tabId="tab-1" /></LocaleProvider>,
);

assert.equal((markup.match(/class="presented-file"/g) ?? []).length, 4);
assert.match(markup, /file-1\.html/);
assert.doesNotMatch(markup, /file-5\.html/);
assert.match(markup, /5/);
assert.equal((markup.match(/presented-file__split/g) ?? []).length, 4, "every presented card uses one split open control");

const changes = renderToStaticMarkup(<LocaleProvider><ModifiedFiles tabId="tab-1" files={[
  { path: "src/app.ts", toolCallId: "write-1", operation: "modified" },
]} summary={{
  preset: "balanced", verdict: "complete", mutations: 2, changed_files: 2, checks_passed: 0, checks_failed: 0,
  checks_suppressed: 0, review: "passed", constraint_degraded: false,
  receipt: { verdict: "complete", diff: { id: "result", turn: 1, coverage: "complete", added: 7, removed: 3, reasons: [], files: [
    { path: "src/app.ts", kind: "modify", added: 5, removed: 2 },
    { path: "README.md", kind: "modify", added: 2, removed: 1 },
  ] } },
}} /></LocaleProvider>);
assert.match(changes, /Edited 2 files/);
assert.match(changes, /\+7/);
assert.match(changes, /−3/);
assert.match(changes, /src\/app\.ts/);
assert.match(changes, /README\.md/);
console.log("presented files UI: changed summary and four-card split controls passed");

const mutationFiles = [{ path: "src/app.ts", toolCallId: "edit-1", operation: "modified" as const }];
const renderChanges = (diff?: NonNullable<WireCompletionSummary["receipt"]>["diff"]) => renderToStaticMarkup(
  <LocaleProvider><ModifiedFiles files={mutationFiles} summary={diff ? {
    preset: "balanced", verdict: "complete", mutations: 2, changed_files: diff.files.length,
    checks_passed: 0, checks_failed: 0, checks_suppressed: 0, review: "passed", constraint_degraded: false,
    receipt: { verdict: "complete", diff },
  } : undefined} /></LocaleProvider>,
);
assert.equal(renderChanges({ id: "reverted", turn: 1, coverage: "complete", added: 0, removed: 0, reasons: [], files: [] }), "",
  "a complete empty receipt must not resurrect reverted mutations");
const partial = renderChanges({ id: "partial", turn: 1, coverage: "partial", added: 4, removed: 2, reasons: ["limited"], files: [
  { path: "src/app.ts", kind: "modify", added: 4, removed: 2 },
] });
assert.match(partial, /Partial statistics/);
assert.match(partial, /\+4/);
const partialEmpty = renderChanges({ id: "partial-empty", turn: 1, coverage: "partial", added: 0, removed: 0, reasons: [], files: [] });
assert.match(partialEmpty, /src\/app.ts/, "an incomplete empty inventory must preserve known mutation paths");
assert.match(partialEmpty, /Partial statistics/);
assert.doesNotMatch(partialEmpty, /\+0|−0/);
const unknown = renderChanges();
assert.match(unknown, /src\/app.ts/);
assert.doesNotMatch(unknown, /\+0|−0/, "mutation calls alone cannot establish net line counts");
const uncounted = renderChanges({ id: "uncounted", turn: 1, coverage: "partial", added: 0, removed: 0, reasons: [], files: [
  { path: "src/app.ts", kind: "modify", uncounted: true },
] });
assert.doesNotMatch(uncounted, /\+0|−0/);
const modes = renderChanges({ id: "mode", turn: 1, coverage: "complete", added: 0, removed: 0, reasons: [], files: [
  { path: "src/app.ts", kind: "modify", modeOnly: true },
] });
assert.match(modes, /Permissions only/);
console.log("presented files UI: reverted, partial, unknown and permissions-only receipts passed");
