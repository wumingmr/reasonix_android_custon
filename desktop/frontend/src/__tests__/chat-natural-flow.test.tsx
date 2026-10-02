import assert from "node:assert/strict";
import { act, createElement, useState, type Dispatch, type SetStateAction } from "react";
import { createRoot } from "react-dom/client";
import { createTranscriptHarness } from "./transcript-dom-harness";
import { reconcileMountedOrder, revealEarlierMountedOrder } from "../lib/chatMountedOrder";
import type { Item } from "../lib/useController";
import type { ChatActions } from "../components/ChatNodes";

for (const count of [1, 5, 23, 24, 25, 48, 49]) {
  const resident = Array.from({ length: 60 }, (_, index) => `resident-${index}`);
  const prefix = Array.from({ length: count }, (_, index) => `history-${index}`);
  const full = [...prefix, ...resident];
  let mounted = reconcileMountedOrder(resident, full);
  assert.equal(mounted, resident, `${count}-node prepend keeps the resident order object before the first frame`);
  let frames = 0;
  while (mounted.length < full.length) {
    const previous = mounted;
    mounted = revealEarlierMountedOrder(mounted, full);
    frames += 1;
    assert.deepEqual(mounted.slice(-resident.length), resident, `${count}-node prepend keeps the resident suffix after frame ${frames}`);
    assert.ok(mounted.length > previous.length && mounted.length - previous.length <= 24, `${count}-node prepend reveals a bounded frame`);
  }
  assert.deepEqual(mounted, full, `${count}-node prepend converges to the source order`);
  assert.equal(frames, Math.ceil(count / 24), `${count}-node prepend uses the expected frame count`);
}

const harness = await createTranscriptHarness({ deterministic: true });
const items: Item[] = [
  { kind: "user", id: "u1", text: "hello", checkpointTurn: 1 },
  { kind: "tool", id: "t1", name: "read_file", args: "{}", output: "result", readOnly: true, status: "done" },
  { kind: "assistant", id: "a1", text: "answer", reasoning: "thought", streaming: false },
];
try {
  await harness.loadModule("/src/components/ChatToolBody.tsx");
  await harness.render(items);
  await harness.settle();
  assert.ok(harness.container.querySelector(".chat-column"));
  assert.ok(harness.container.querySelector('[id^="reasonix-chat-transcript-"] .chat-column'), "native upgrade evidence scopes history to the transcript");
  assert.equal(harness.container.querySelectorAll(".transcript__window-item").length, 0);
  assert.equal(harness.container.querySelectorAll(".chat-tool").length, 0, "completed process unmounts heavy rows");
  const disclosure = harness.container.querySelector<HTMLButtonElement>(".chat-process");
  await act(async () => disclosure!.click());
  const tool = harness.container.querySelector<HTMLElement>(".chat-tool [data-disclosure-row]");
  assert.ok(tool);
  await act(async () => tool.click());
  await harness.settle();
  assert.ok(harness.container.querySelector(".dsh-ToolRow-ioCard"), "tool opens an inline preview");
  await act(async () => harness.container.querySelector<HTMLButtonElement>(".dsh-ToolRow-inspectButton")!.click());
  assert.ok(harness.container.querySelector('[role="dialog"]'));
  for (let i = 0; i < 60; i++) {
    await harness.render(items.map(item => item.kind === "assistant" ? { ...item, text: `answer ${i}`, streaming: true } : item), { running: true });
    await act(async () => { harness.resizeNotifications.forEach(notify => notify()); harness.clock.advance(16); });
  }
  assert.ok(harness.container.querySelector(".chat-column"), "60 geometry changes do not trip React nested update fuse");
  await harness.render(items, { geometrySessionKey: "other" });
  assert.equal(harness.container.querySelector('[role="dialog"]'), null, "session replacement closes details");

  const restored: Item[] = [];
  for (let turn = 1; turn <= 12; turn += 1) {
    restored.push(
      { kind: "user", id: `u${turn}`, text: `prompt ${turn}`, checkpointTurn: turn },
      { kind: "assistant", id: `a${turn}`, text: `answer ${turn}`, reasoning: "", streaming: false },
    );
  }
  await harness.render(restored, { geometrySessionKey: "history-identity" });
  await harness.settle();
  const existing = new Map(Array.from(harness.container.querySelectorAll<HTMLElement>("[data-chat-anchor-key]"))
    .map(node => [node.dataset.chatAnchorKey!, node]));
  assert.equal(existing.size, 60, "fixture spans multiple legacy 24-node chunks");
  const selectedParagraph = harness.container.querySelector<HTMLElement>('[data-chat-anchor-key="a6"] .md p')!;
  const selectedText = selectedParagraph.firstChild!;
  const range = harness.dom.window.document.createRange();
  range.selectNodeContents(selectedText);
  const selection = harness.dom.window.getSelection()!;
  selection.removeAllRanges();
  selection.addRange(range);
  const selectedBefore = selection.toString();
  await harness.render([
    { kind: "user", id: "u0", text: "older prompt", checkpointTurn: 0 },
    { kind: "assistant", id: "a0", text: "older answer", reasoning: "", streaming: false },
    ...restored,
  ], { geometrySessionKey: "history-identity" });
  await harness.settle();
  for (const [key, node] of existing) {
    assert.equal(harness.container.querySelector(`[data-chat-anchor-key="${key}"]`), node, `history prepend preserves ${key} DOM identity`);
  }
  assert.equal(selection.toString(), selectedBefore, "history prepend preserves the native text selection");

  const deepHistory: Item[] = [];
  for (let turn = 1; turn <= 15; turn += 1) {
    deepHistory.push(
      { kind: "user", id: `old-u${turn}`, text: `old prompt ${turn}`, checkpointTurn: -turn },
      { kind: "assistant", id: `old-a${turn}`, text: `old answer ${turn}`, reasoning: "", streaming: false },
    );
  }
  await harness.render([
    ...deepHistory,
    { kind: "user", id: "u0", text: "older prompt", checkpointTurn: 0 },
    { kind: "assistant", id: "a0", text: "older answer", reasoning: "", streaming: false },
    ...restored,
  ], { geometrySessionKey: "history-identity" });
  assert.equal(harness.container.querySelector('[data-chat-anchor-key="old-u1"]'), null, "deep history is still mounting after two bounded frames");
  assert.equal(harness.container.querySelector('[data-nav-turn="old-u1"]'), null, "navigation omits history without a mounted target");
  await harness.settle();
  assert.ok(harness.container.querySelector('[data-chat-anchor-key="old-u1"]'), "deep history eventually mounts");
  assert.ok(harness.container.querySelector('[data-nav-turn="old-u1"]'), "navigation publishes the target after its DOM commit");
  let newerLoads = 0;
  await harness.render(restored, {
    geometrySessionKey: "history-identity",
    hasNewerHistory: true,
    historyStartTurn: 4,
    historyEndTurn: 12,
    totalTurns: 20,
    onLoadNewerHistory: async () => { newerLoads += 1; return "loaded"; },
  });
  const newer = harness.container.querySelector<HTMLButtonElement>(".chat-history-newer .btn")!;
  await act(async () => newer.click());
  assert.equal(newerLoads, 0, "a native transcript selection protects its resident page from reclaim");
  assert.ok(harness.container.querySelector(".chat-history-selection"), "selection protection explains why paging paused");
  await act(async () => {
    selection.removeAllRanges();
    harness.dom.window.document.dispatchEvent(new harness.dom.window.Event("selectionchange"));
  });
  await act(async () => newer.click());
  assert.equal(newerLoads, 1, "newer paging resumes after the selection is cleared");
  assert.match(harness.container.querySelector(".chat-history-window")?.textContent ?? "", /5.*12.*20/, "the bounded window reports its visible turn range");

  // Reading summary is presentation work, so this measures actual tool renders
  // without a wall-clock threshold or counting the source's projection work.
  // Mount the list once, then publish through its source: rerendering the test
  // harness's LocaleProvider would also invalidate every locale consumer.
  await harness.unmount();
  const { ChatSource } = await harness.loadModule<typeof import("../lib/chatViewSource")>("/src/lib/chatViewSource.ts");
  const { ChatNodeList } = await harness.loadModule<typeof import("../components/ChatNodes")>("/src/components/ChatNodes.tsx");
  const { ChatMountedOrder } = await harness.loadModule<typeof import("../lib/chatMountedOrder")>("/src/lib/chatMountedOrder.ts");
  const { ChatContentLoader } = await harness.loadModule<typeof import("../lib/chatContentLoader")>("/src/lib/chatContentLoader.ts");
  const { ChatScrollController } = await harness.loadModule<typeof import("../lib/chatScrollController")>("/src/lib/chatScrollController.ts");
  const { LocaleProvider } = await harness.loadModule<typeof import("../lib/i18n")>("/src/lib/i18n.tsx");
  const source = new ChatSource("render-isolation");
  const root = createRoot(harness.container);
  const loader = new ChatContentLoader();
  const scroll = new ChatScrollController("render-isolation");
  const mounts = new ChatMountedOrder();
  let changeActions!: Dispatch<SetStateAction<ChatActions>>;
  function Surface() {
    const [actions, setActions] = useState<ChatActions>({ openDetails: () => {}, recover: () => {},
      fork: { targetFor: () => undefined, loaded: true, verifiable: true, blocked: null, create: () => {} } });
    changeActions = setActions;
    return createElement(ChatNodeList, { source, mounts, loader, scroll, actions });
  }
  const publish = async (items: Item[], running = true) => {
    await act(async () => source.update({ items, running, hydrating: false, hasOlder: false, loadingOlder: false }));
    await harness.settle();
  };
  try {
    const renders = new Map<string, number>();
    const toolItem = (id: string, status: "done" | "running" | "error" = "done"): Item => ({
      kind: "tool", id, name: "custom_tool", args: "{}", status,
      get summary() { renders.set(id, (renders.get(id) ?? 0) + 1); return `summary ${id}`; },
    });
    const siblings = Array.from({ length: 32 }, (_, index) => toolItem(`isolated-${index}`));
    const question: Item = { kind: "user", id: "isolated-user", text: "run tools" };
    await publish([question, ...siblings]);
    await act(async () => root.render(createElement(LocaleProvider, null, createElement(Surface))));
    await harness.settle();
    assert.equal(renders.size, siblings.length, "the fixture renders every existing tool");
    renders.clear();
    const appended = toolItem("isolated-new", "running");
    await publish([question, ...siblings, appended]);
    assert.deepEqual([...renders.keys()], [appended.id], "adding a tool renders no unchanged sibling despite process membership/count changes");
    renders.clear();
    const failed = toolItem(appended.id, "error");
    await publish([question, ...siblings, failed]);
    assert.deepEqual([...renders.keys()], [failed.id], "a failure-count update renders only the changed tool");
    assert.equal(harness.container.querySelector(`[data-chat-anchor-key="${failed.id}"] .chat-tool`)?.getAttribute("data-state"), "error");
    const finalAnswer: Item = { kind: "assistant", id: "isolated-answer", text: "finished", reasoning: "", streaming: false };
    await publish([question, ...siblings, failed, finalAnswer], false);
    assert.equal(harness.container.querySelectorAll(".chat-tool").length, 0, "settlement still hides every process member");
    assert.ok(harness.container.querySelector('[data-chat-anchor-key="isolated-answer"][data-turn-process-answer]'), "the final answer still gets collapsed-process spacing");
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".chat-process")!.click());
    assert.equal(harness.container.querySelectorAll(".chat-tool").length, siblings.length + 1, "expansion still reveals every process member");
    assert.equal(harness.container.querySelector('[data-turn-process-answer]'), null, "expansion clears collapsed-answer spacing");
    renders.clear();
    await act(async () => changeActions(old => ({ ...old, fork: { ...old.fork!, blocked: "creating" } })));
    assert.equal(renders.size, 0, "fork state changes do not rerender unchanged tools");
    assert.ok(harness.container.querySelector('[aria-label*="Creating the branch"]'), "fork controls still receive current state");

    await harness.loadModule("/src/components/PresentedFiles.tsx");
    const writes: Item[] = [
      { kind: "tool", id: "write-a", name: "write_file", args: '{"path":"a.ts","content":"a"}', status: "done" },
      { kind: "tool", id: "present-a", name: "present", args: "{}", status: "done", presentedFiles: [{ path: "a.ts", description: "A" }] },
    ];
    const liveAnswer: Item = { ...finalAnswer, streaming: true, reasoning: "unchanged", reasoningComplete: true };
    await publish([question, ...siblings, ...writes, liveAnswer]);
    await harness.waitFor(() => Boolean(harness.container.querySelector(".turn-files") && harness.container.querySelector(".presented-files")), "file cards");
    const tail = source.getNodeSnapshot(`${question.id}:tail`);
    assert.ok(tail?.kind === "tail");
    let modifiedRenders = 0, presentedRenders = 0;
    Object.defineProperty(tail.modifiedFiles, "map", { configurable: true, value: function (...args: unknown[]) {
      modifiedRenders++; return Array.prototype.map.apply(this, args as never);
    } });
    Object.defineProperty(tail.presentedFiles, "slice", { configurable: true, value: function (...args: unknown[]) {
      presentedRenders++; return Array.prototype.slice.apply(this, args as never);
    } });
    for (let index = 0; index < 20; index++) await act(async () => source.updateLive({
      id: liveAnswer.id, text: `answer ${index}`, reasoning: "unchanged", reasoningComplete: true,
    }));
    assert.equal(modifiedRenders, 0, "text-only stream updates do not recompute modified-file cards");
    assert.equal(presentedRenders, 0, "text-only stream updates do not rerender presented-file cards");
    await publish([question, ...siblings, ...writes,
      { kind: "tool", id: "write-b", name: "write_file", args: '{"path":"b.ts","content":"b"}', status: "done" },
      { kind: "tool", id: "present-b", name: "present", args: "{}", status: "done", presentedFiles: [{ path: "b.ts", description: "B" }] }, liveAnswer]);
    assert.match(harness.container.querySelector(".turn-files")?.textContent ?? "", /b\.ts/, "new modified files remain visible");
    assert.match(harness.container.querySelector(".presented-files")?.textContent ?? "", /b\.ts/, "new presented files remain visible");

    // Memoization must preserve positive updates as well as skip stale work.
    const declarations: Item = { kind: "tool", id: "present-many", name: "present", args: "{}", status: "done",
      presentedFiles: Array.from({ length: 5 }, (_, index) => ({ path: `file-${index}.ts`, description: `description-${index}` })) };
    await publish([question, declarations, liveAnswer]);
    const cards = () => harness.container.querySelectorAll(".presented-file");
    assert.equal(cards().length, 4);
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".presented-files .presented-files__toggle")!.click());
    assert.equal(cards().length, 5, "memoized file cards still expand from their own state");
    await publish([question, { ...declarations, presentedFiles: declarations.presentedFiles!.map((file, index) => index ? file : { ...file, description: "updated description" }) }, liveAnswer]);
    assert.equal(cards().length, 5, "file metadata updates preserve disclosure state");
    assert.match(harness.container.querySelector(".presented-files")?.textContent ?? "", /updated description/, "same-path description updates are not cached away");
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".presented-files .presented-files__toggle")!.click());
    assert.equal(cards().length, 4);

    const boundary = { sourceSessionId: "source", sessionGeneration: 1, turnId: "turn", boundarySequence: 10,
      turnNumber: 1, status: "committed", messageId: liveAnswer.id, available: true };
    const forkCalls: string[] = [];
    await act(async () => changeActions(old => ({ ...old, fork: { targetFor: () => boundary, loaded: true,
      verifiable: true, blocked: null, create: () => { forkCalls.push("old"); } } })));
    await act(async () => changeActions(old => ({ ...old, fork: { ...old.fork!, create: () => { forkCalls.push("latest"); } } })));
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".chat-actions button.chat-action-icon:not(.copybtn)")!.click());
    assert.deepEqual(forkCalls, ["latest"], "fork context replacement uses the latest callback");
    await act(async () => changeActions(old => ({ ...old, fork: undefined })));
    assert.equal(harness.container.querySelector(".chat-actions button.chat-action-icon:not(.copybtn)"), null, "removing fork support removes the old control");

    const copied: string[] = [];
    Object.defineProperty(harness.dom.window.navigator, "clipboard", { configurable: true,
      value: { writeText: async (text: string) => { copied.push(text); } } });
    const longReasoning = "complete reasoning ".repeat(600);
    await publish([question, { ...liveAnswer, reasoning: longReasoning }]);
    await act(async () => harness.container.querySelector<HTMLElement>(".chat-reasoning [data-disclosure-row]")!.click());
    await act(async () => source.updateLive({ id: liveAnswer.id, text: "new answer while reading reasoning", reasoning: longReasoning, reasoningComplete: true }));
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".chat-reasoning .copybtn")!.click());
    assert.equal(copied.at(-1), longReasoning, "isolated reasoning still copies full content beyond its preview");
    await act(async () => source.updateLive({ id: liveAnswer.id, text: "latest answer", reasoning: "replacement reasoning", reasoningComplete: true }));
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".chat-reasoning .copybtn")!.click());
    assert.equal(copied.at(-1), "replacement reasoning", "changed reasoning replaces the copy callback content");
    await act(async () => harness.container.querySelector<HTMLButtonElement>(".chat-actions .copybtn")!.click());
    assert.equal(copied.at(-1), "latest answer", "reasoning projection never replaces the canonical answer used for copying");
  } finally {
    await act(async () => root.unmount());
    source.dispose(); mounts.dispose(); loader.dispose(); scroll.dispose();
  }
  console.log("chat natural flow: process disclosure, details, stable history identity, mounted navigation and session isolation passed");
} finally { await harness.unmount(); await harness.close(); }
