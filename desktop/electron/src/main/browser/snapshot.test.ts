import assert from "node:assert/strict";
import { test } from "node:test";
import { runInContext } from "node:vm";
import { JSDOM } from "jsdom";
import { DocumentRegistry } from "./documents.js";
import { BROWSER_ERR_STALE_REFERENCE } from "./errors.js";
import { FakeFrame, FakePage } from "./fakeGuestViews.js";
import { LOCATE_SCRIPT_SOURCE, RESOLVE_SCRIPT_SOURCE, scriptCall, SELECT_SCRIPT_SOURCE, type LocateOutput, type ResolveOutput, type SelectOutput } from "./pageScripts.js";
import { resolveRef } from "./refResolver.js";
import { RpcError } from "../rpc.js";
import { REGISTRY_KEY, takeSnapshot } from "./snapshot.js";
import type { SnapshotOutput } from "./snapshotScript.js";
import { SNAPSHOT_SCRIPT_SOURCE } from "./pageScripts.js";

// Script outputs are plain data but live in the page realm; deepStrictEqual
// compares prototypes, so JSON round-trip them before asserting.
const plain = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

// jsdom has no layout: rects come from data-rect="x,y,w,h" (default 10x10)
// and elementFromPoint answers with whatever the test pinned.
function page(html: string) {
  const dom = new JSDOM(`<!doctype html><html><body>${html}</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://site.test/page" });
  const win = dom.window;
  const computed = win.getComputedStyle.bind(win);
  win.getComputedStyle = (element) => computed(element);
  const hit: { element: Element | null } = { element: null };
  win.Element.prototype.getBoundingClientRect = function (this: Element) {
    const spec = this.getAttribute("data-rect");
    const [x, y, width, height] = spec ? spec.split(",").map(Number) : [0, 0, 10, 10];
    return { x, y, width, height, left: x, top: y, right: x + width, bottom: y + height, toJSON: () => ({}) } as DOMRect;
  };
  win.document.elementFromPoint = () => hit.element;
  const context = dom.getInternalVMContext();
  const run = (code: string) => runInContext(code, context) as unknown;
  const snapshot = (input: Partial<{ snapshotId: string; prefix: string; selector: string; budget: number }> = {}) =>
    run(scriptCall(SNAPSHOT_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId: "snap-1", prefix: "", selector: "", budget: 4000, ...input })) as SnapshotOutput;
  const refFor = (selector: string) => {
    const registry = run(`window[${JSON.stringify(REGISTRY_KEY)}]`) as { refs: Map<string, Element> };
    const element = win.document.querySelector(selector);
    const ref = [...registry.refs].find(([, value]) => value === element)?.[0];
    assert.ok(ref, `missing ref for ${selector}`);
    return ref;
  };
  return { dom, win, hit, run, snapshot, refFor };
}

test("the snapshot walker emits roles, names, states and refs one node per line", () => {
  const { snapshot } = page(`
    <nav aria-label="Main"><a href="/home">Home</a><a>no href</a></nav>
    <h2>Sign in</h2>
    <form>
      <label for="u">Username</label><input id="u" value="ann" placeholder="user">
      <input type="password" aria-label="Password" value="secret">
      <input type="checkbox" checked aria-label="Remember"> <button disabled>Go</button>
      <select aria-label="Role"><option value="a">Admin</option><option value="b" selected>Basic</option></select>
      <div hidden>hidden text</div><span aria-hidden="true">assistive only</span>
      <p>Some <b>bold</b> text</p>
      <div tabindex="0">Clickable card</div>
      <textarea aria-label="Bio">hello</textarea>
      <img alt="Logo"><img alt="">
      <input type="file" aria-label="Attach">
      <table><tr><th>Name</th><td>Ann</td></tr></table>
      <ul><li>one</li><li data-rect="0,0,0,0">zero size</li></ul>
      <details open><summary>More</summary>body</details>
    </form>`);
  const out = snapshot();
  for (const pattern of [/navigation "Main" ref=e\d+/, /link "Home" ref=e\d+/, /heading "Sign in" \[level=2\]/, /textbox "Username"/, /checkbox "Remember" \[checked\]/, /button "Go" \[disabled\]/, /option "Basic" \[selected\]/, /textbox "Bio"/, /img "Logo"/, /cell "Ann"/]) assert.match(out.tree, pattern);
  assert.ok(out.refs > 10);
  assert.match(out.docId, /^\d+(\.\d+)?:[a-z0-9]+$/);
  assert.equal(out.tree.includes("secret"), false, "password values never appear");
});

test("semantic queries expose ambiguity and pierce shadow DOM without exposing password values", () => {
  const p = page('<button>Save</button><button>Save</button><input type="password" role="textbox" aria-label="Credential" value="do-not-expose"><div id="host"></div>');
  p.win.document.querySelector("#host")!.attachShadow({ mode: "open" }).innerHTML = '<button data-testid="shadow-save">Shadow save</button>';
  const observed = p.snapshot();
  assert.equal(observed.tree.includes("do-not-expose"), false);
  const query = (criteria: object) => plain(p.run(scriptCall("pageQuery", { key: REGISTRY_KEY, snapshotId: "snap-1", docId: observed.docId, prefix: "", ...criteria }))) as { count: number; refs: string[]; ambiguous: boolean; error?: string };
  assert.equal(query({ role: "button", name: "Save" }).count, 2);
  assert.equal(query({ role: "button", name: "Save" }).ambiguous, true);
  assert.equal(query({ testId: "shadow-save" }).count, 1);
  p.snapshot({ snapshotId: "new" });
  assert.equal(query({ text: "Save" }).error, "stale_document");
  p.dom.window.close();
});

test("the walker honours selector scoping, the node budget and a stable document identity", () => {
  const { snapshot, run } = page(`<main><button>A</button><button>B</button><button>C</button></main><aside><a href="#">x</a></aside>`);
  const first = snapshot({ budget: 2 });
  assert.equal(first.tree.split("\n").length, 3);
  assert.ok(first.truncated > 0);
  assert.equal(first.nodes, 2);
  const scoped = snapshot({ selector: "aside", prefix: "f2", snapshotId: "snap-2" });
  assert.match(scoped.tree, /link "x" ref=f2e\d+/);
  assert.equal(snapshot({ selector: "#nope" }).tree, '(no element matches selector "#nope")');
  assert.equal(scoped.docId, first.docId, "the identity survives re-snapshots of the same document");
  const registry = run(`window[${JSON.stringify(REGISTRY_KEY)}]`) as { snapshotId: string; refs: Map<string, Element> };
  assert.equal(registry.snapshotId, "snap-1", "the latest snapshot owns the registry");
  assert.equal(run(`Object.keys(window).includes(${JSON.stringify(REGISTRY_KEY)})`), false, "the registry is not enumerable");
});

test("resolve, locate and select validate the snapshot token and the document identity", () => {
  const { snapshot, run, hit, win, refFor } = page(`<button data-rect="100,50,80,30">Go</button><select aria-label="S"><option value="1">One</option><option value="2">Two</option></select><input type="file" aria-label="F">`);
  const out = snapshot();
  const buttonRef = refFor("button"), selectRef = refFor("select"), fileRef = refFor("input");
  const resolve = (ref: string, extra: Record<string, unknown> = {}) =>
    plain(run(scriptCall(RESOLVE_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId: "snap-1", docId: out.docId, ref, scroll: true, ...extra })) as ResolveOutput);
  const resolved = resolve(buttonRef);
  assert.deepEqual(resolved, { ok: true, x: 100, y: 50, width: 80, height: 30, tag: "button", type: "", disabled: false, editable: false, frameOffsetKnown: true });
  assert.deepEqual(resolve(buttonRef, { snapshotId: "old" }), { ok: false, reason: "stale" });
  assert.deepEqual(resolve(buttonRef, { docId: "other" }), { ok: false, reason: "stale" });
  assert.deepEqual(resolve("e99"), { ok: false, reason: "stale" });
  hit.element = win.document.querySelector("select");
  assert.deepEqual(resolve(buttonRef), { ok: false, reason: "element is covered by another element" });
  hit.element = null;
  win.document.querySelector("button")?.remove();
  assert.deepEqual(resolve(buttonRef), { ok: false, reason: "element is no longer in the document" });

  const located = plain(run(scriptCall(LOCATE_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId: "snap-1", docId: out.docId, ref: fileRef })) as LocateOutput);
  assert.deepEqual(located, { ok: true, tag: "input", type: "file", path: "html > body:nth-child(2) > input:nth-child(2)" });

  const changes: string[] = [];
  win.document.querySelector("select")?.addEventListener("change", () => changes.push("change"));
  const selected = plain(run(scriptCall(SELECT_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId: "snap-1", docId: out.docId, ref: selectRef, options: ["Two"] })) as SelectOutput);
  assert.deepEqual(selected, { ok: true, selected: ["2"] });
  assert.equal((win.document.querySelector("select") as HTMLSelectElement).value, "2");
  assert.deepEqual(changes, ["change"]);
  assert.deepEqual(plain(run(scriptCall(SELECT_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId: "snap-1", docId: out.docId, ref: selectRef, options: ["Nine"] }))), { ok: false, reason: "no option matches the requested values" });
});

test("takeSnapshot assembles the main frame and reachable child frames under one token", async () => {
  const main = page(`<h1>Top</h1><iframe title="Login frame"></iframe>`);
  const child = page(`<button>Inside</button>`);
  const broken = page(`<p>never seen</p>`);
  const fake = new FakePage(7);
  fake.url = "https://site.test/page";
  fake.title = "Site";
  const childFrame = new FakeFrame(701, "https://login.test/", (code) => child.run(code));
  const brokenFrame = new FakeFrame(702, "https://cross.test/", () => {
    throw new Error("cross-origin");
  });
  fake.mainFrame.children.push(childFrame, brokenFrame);
  childFrame.parent = fake.mainFrame;
  brokenFrame.parent = fake.mainFrame;
  fake.debugger.respond = (method, raw) => {
    const params = raw as Record<string, unknown>;
    if (method === "Page.getFrameTree") return { frameTree: { frame: { id: "main", url: fake.url }, childFrames: [{ frame: { id: "child", url: childFrame.url } }, { frame: { id: "broken", url: brokenFrame.url } }] } };
    if (method === "Page.createIsolatedWorld") return { executionContextId: params.frameId === "main" ? 0 : params.frameId === "child" ? 1 : 2 };
    if (method === "DOM.describeNode") return { node: { frameId: params.objectId } };
    if (method === "Target.getTargets") return { targetInfos: [] };
    if (method === "Runtime.evaluate") {
      if (params.contextId === 0) return { result: { objectId: String(params.expression).includes("window.frames[0]") ? "child" : "broken" } };
      if (params.contextId === 2) throw new Error("frame crashed");
      return { result: { value: child.run(String(params.expression)) } };
    }
    return {};
  };
  fake.run = (code, frame) => code.includes("reasonix:pageFrameGeometry") ? { x: 5, y: 5, scaleX: 1, scaleY: 1 } : (frame === fake.mainFrame ? main.run(code) : broken.run(code));
  const documents = new DocumentRegistry(() => "tok-1");
  const result = await takeSnapshot(fake, "tab-1", 3, "", documents);
  assert.equal(result.documentToken, "tok-1");
  assert.equal(result.url, "https://site.test/page");
  assert.equal(result.title, "Site");
  assert.ok(result.refs >= 2);
  assert.match(result.tree, /heading "Top" \[level=1\]/);
  assert.match(result.tree, /frame f1 "https:\/\/login.test\/"/);
  assert.match(result.tree, /button "Inside" ref=f1e\d+/);
  const binding = documents.lookup("tok-1");
  assert.ok(binding);
  assert.equal(binding.epoch, 3);
  assert.deepEqual(binding.frames.map((frame) => [frame.prefix, frame.frameTreeNodeId]), [["", 700], ["f1", 701]]);

  const inside = await resolveRef(fake, binding, child.refFor("button"), false);
  assert.ok(inside.ok);
  assert.equal(inside.value.frame, childFrame);
  assert.equal(inside.value.element.tag, "button");
  await assert.rejects(resolveRef(fake, binding, "f3e1", false), (error: unknown) => error instanceof RpcError && error.code === BROWSER_ERR_STALE_REFERENCE);
  await assert.rejects(resolveRef(fake, binding, "bogus", false), (error: unknown) => error instanceof RpcError && error.code === BROWSER_ERR_STALE_REFERENCE);

  const documents2 = new DocumentRegistry(() => "tok-2");
  await takeSnapshot(fake, "tab-1", 4, "", documents2);
  await assert.rejects(resolveRef(fake, binding, "e1", false), (error: unknown) => error instanceof RpcError && error.code === BROWSER_ERR_STALE_REFERENCE, "the older snapshot's refs are stale once a newer one exists");
});
