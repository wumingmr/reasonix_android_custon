import assert from "node:assert/strict";
import { test } from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { mkdtemp, copyFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { JSDOM } from "jsdom";

test("production minification cannot leak main-world bundler helpers into page scripts", async () => {
  const dir = await mkdtemp(join(tmpdir(), "reasonix-page-bundle-"));
  const dom = new JSDOM('<body><button aria-label="Save">Save</button></body>', { runScripts: "outside-only", pretendToBeVisual: true });
  try {
    // Same failure mechanism as the former nested snapshot walker: keepNames
    // introduces a module helper which Function#toString cannot carry along.
    const legacy = await build({ stdin: { contents: 'export function pageSnapshot() { const role = element => element.getAttribute("aria-label"); return role(document.querySelector("button")); }', loader: "js" }, bundle: true, platform: "node", format: "cjs", minify: true, keepNames: true, write: false });
    const oldModule = { exports: {} as { pageSnapshot?: () => string } };
    new Function("module", "exports", legacy.outputFiles[0].text)(oldModule, oldModule.exports);
    assert.throws(() => dom.window.eval(`(${oldModule.exports.pageSnapshot!.toString()})()`), /not defined/, "the old serialization mechanism must fail in the same isolated page environment");
    for (const name of ["browser-page.js", "browser-page.json"]) await copyFile(resolve("dist", name), join(dir, name));
    await build({ entryPoints: [resolve("src/main/browser/pageScripts.ts")], bundle: true, platform: "node", target: "node22", format: "cjs", minify: true, keepNames: true, outfile: join(dir, "main.cjs"), logLevel: "silent" });
    const runtime = createRequire(import.meta.url)(join(dir, "main.cjs")) as { scriptCall(method: string, input: unknown): string };
    dom.window.Element.prototype.getBoundingClientRect = () => ({ width: 50, height: 20, x: 0, y: 0, left: 0, top: 0, right: 50, bottom: 20, toJSON() {} });
    const out = dom.window.eval(runtime.scriptCall("pageSnapshot", { key: "test", snapshotId: "s1", prefix: "", selector: "", budget: 4000 })) as { tree: string; docId: string };
    assert.match(out.tree, /button "Save" ref=e1/);
    assert.equal(dom.window.eval("typeof __name"), "undefined");
    assert.equal(dom.window.eval(runtime.scriptCall("pageIdentity", { key: "test", docId: out.docId })), true);
    const picking = dom.window.eval(runtime.scriptCall("pagePickElement", { key: "test" })) as Promise<unknown>;
    assert.equal(dom.window.document.querySelectorAll("div").length, 1);
    dom.window.eval(runtime.scriptCall("pageCancelPicker", {}));
    assert.equal(await picking, null);
    assert.equal(dom.window.document.querySelectorAll("div").length, 0, "closing the panel cancels and removes the picker overlay");
  } finally {
    dom.window.close();
    await rm(dir, { recursive: true, force: true });
  }
});
