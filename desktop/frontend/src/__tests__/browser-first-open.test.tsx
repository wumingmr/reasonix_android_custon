import assert from "node:assert/strict";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import type { BrowserTabView, DesktopBrowserHost } from "../lib/browserHost";
import type { ReasonixDesktopHost } from "../lib/desktopHost";

const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
let receive: ((tabs: BrowserTabView[]) => void) | undefined;
const browser = { list: async () => [], onTabs: (callback: typeof receive) => { receive = callback; return () => { receive = undefined; }; } } as DesktopBrowserHost;
window.reasonixDesktop = { browser, invoke: async () => undefined, native: { window: {}, clipboard: {}, onServiceState: () => () => {} } } as unknown as ReasonixDesktopHost;
const [{ createRoot }, { useBrowserFirstOpen }] = await Promise.all([import("react-dom/client"), import("../app-runtime/useBrowserFirstOpen")]);
let reveals = 0;
function Fixture({ turn = "turn-1" }: { turn?: string }) { useBrowserFirstOpen("task", turn, () => reveals++, "session"); return null; }
const root = createRoot(document.getElementById("root")!);
const rows: BrowserTabView[] = [];
async function add(id: string, taskId = "task", sessionId = "session") { rows.push({ id, taskId, sessionId } as BrowserTabView); await act(async () => receive?.([...rows])); }
try {
  await act(async () => root.render(<Fixture />));
  await add("background", "other"); assert.equal(reveals, 0);
  await add("stale", "task", "old-session"); assert.equal(reveals, 0);
  await add("first"); assert.equal(reveals, 1);
  await add("second"); assert.equal(reveals, 1, "closing the panel does not allow a second reveal in the same turn");
  await act(async () => root.render(<Fixture turn="turn-2" />));
  await add("next-turn"); assert.equal(reveals, 2);
  console.log("browser first open: foreground session, one reveal per turn, and background isolation passed");
} finally { await act(async () => root.unmount()); dom.window.close(); }
