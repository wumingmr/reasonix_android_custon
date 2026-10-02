import assert from "node:assert/strict";
import React, { act } from "react";
import { renderFilesWorkspace, waitFor } from "./workspace-panel-test-harness";
import { RemotePanel } from "../components/RemotePanel";
import { LocaleProvider } from "../lib/i18n";
import { useRemoteStore } from "../store/remote";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
type Preview = { path: string; body: string; size: number; mtimeUnix: number; binary: boolean; truncated: boolean };
const pending = new Map<string, ReturnType<typeof deferred<Preview>>>();
const writes: Array<{ path: string; body: string }> = [];
const pendingWrites: Array<ReturnType<typeof deferred<{ conflict: boolean; newMtimeUnix: number }>>> = [];
const { dom, root } = await renderFilesWorkspace({
  ListRemoteDir: async () => ["a.txt", "b.txt"].map(name => ({ name, path: name, isDir: false, size: 5 })),
  ReadRemoteFile: (_host, path) => {
    const read = deferred<Preview>(); pending.set(path, read); return read.promise;
  },
  WriteRemoteFile: (_host, path, body) => {
    writes.push({ path, body });
    const write = deferred<{ conflict: boolean; newMtimeUnix: number }>();
    pendingWrites.push(write);
    return write.promise;
  },
});
await act(async () => {
  useRemoteStore.setState({ explorerHostId: "remote", explorerTab: "files", statuses: { remote: { state: "connected" } } });
  root.render(<LocaleProvider><RemotePanel onClose={() => {}} tabId="one" dockTabId="dock" /></LocaleProvider>);
});
await waitFor("remote tree", () => document.querySelectorAll(".remote-tree__row").length === 2);
const click = async (name: string) => {
  const button = [...document.querySelectorAll<HTMLButtonElement>(".remote-tree__row")].find(node => node.textContent === name)!;
  await act(async () => button.click());
  await waitFor(`read ${name}`, () => pending.has(name));
};
await click("a.txt");
await click("b.txt");
const result = (path: string): Preview => ({ path, body: `CONTENT ${path}`, size: 10, mtimeUnix: 1, binary: false, truncated: false });
await act(async () => pending.get("b.txt")!.resolve(result("b.txt")));
await waitFor("new body", () => document.body.textContent?.includes("CONTENT b.txt") === true);
await act(async () => pending.get("a.txt")!.resolve(result("a.txt")));
assert(!document.body.textContent?.includes("CONTENT a.txt"));
assert(document.body.textContent?.includes("CONTENT b.txt"));

// A write issued for one file must not touch the file the panel moved on to.
await click("a.txt");
await act(async () => pending.get("a.txt")!.resolve(result("a.txt")));
await waitFor("a reloaded", () => document.body.textContent?.includes("CONTENT a.txt") === true);
const edit = [...document.querySelectorAll<HTMLButtonElement>(".remote-file-view__toolbar button")]
  .find(button => /edit/i.test(button.textContent ?? ""))!;
await act(async () => { edit.click(); });
// jsdom does not deliver a text input event to React's handler in this harness;
// the mounted element's own props are the same contract the browser drives.
const textarea = document.querySelector<HTMLTextAreaElement>(".remote-file-view__editor")!;
const propsKey = Object.keys(textarea).find((key) => key.startsWith("__reactProps"));
const onChange = propsKey
  ? (textarea as unknown as Record<string, { onChange?: (event: { target: { value: string } }) => void }>)[propsKey]?.onChange
  : undefined;
assert(onChange, "the editor exposes its change handler");
await act(async () => { onChange!({ target: { value: "DRAFT A" } }); });
const save = [...document.querySelectorAll<HTMLButtonElement>(".remote-file-view__toolbar button")]
  .find(button => /save/i.test(button.textContent ?? ""))!;
assert.equal(save.disabled, false, "an edited file can be saved");
await act(async () => save.click());
await waitFor("write issued", () => writes.length === 1);
assert.deepEqual(writes[0], { path: "a.txt", body: "DRAFT A" }, "the write targets the file it was issued for");
await click("b.txt");
await act(async () => pending.get("b.txt")!.resolve(result("b.txt")));
await waitFor("replaced file", () => document.body.textContent?.includes("CONTENT b.txt") === true);
await act(async () => pendingWrites[0]!.resolve({ conflict: false, newMtimeUnix: 2 }));
assert(document.body.textContent?.includes("CONTENT b.txt"), "a receipt for another file cannot overwrite the one on screen");
assert(!document.body.textContent?.includes("DRAFT A"), "a receipt for another file cannot leak its draft");
assert.equal(document.querySelector(".remote-file-view__conflict"), null, "a receipt for another file cannot raise its conflict");

// A disconnected host reports the failure in the panel rather than a stale view.
await act(async () => useRemoteStore.setState({ statuses: { remote: { state: "stopped" } } }));
await waitFor("disconnected hint", () => document.querySelector(".remote-panel__hint") !== null);
assert.equal(document.querySelector(".remote-file-view"), null, "a disconnected host shows no file view");
assert(!document.body.textContent?.includes("CONTENT b.txt"), "a disconnected host shows no stale preview");

await act(async () => root.unmount());
await act(async () => pending.get("a.txt")!.resolve(result("a.txt")));
assert.equal(document.querySelector(".remote-file-view"), null);
dom.window.close();
console.log("PASS actual remote panel: out-of-order reads, save isolation, late completion after disposal");
