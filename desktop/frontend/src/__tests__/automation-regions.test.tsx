import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { JSDOM } from "jsdom";
import { AppBottomRegions } from "../app-shell/AppBottomRegions";
import { register } from "node:module";
import type { Translator } from "../lib/i18n";

const noop = () => {};
register(new URL("../../scripts/svg-loader.mjs", import.meta.url));
const { SidebarRegion } = await import("../app-shell/SidebarRegion");
const t = ((key: string) => key) as Translator;
const topicbarActionsSource = readFileSync(new URL("../app-shell/TopicbarActionsStack.tsx", import.meta.url), "utf8");
assert.doesNotMatch(topicbarActionsSource, /onOpenPalette|\bSearch\b/, "the topic bar no longer owns the command-palette entry");
for (const automation of [false, true]) {
  const markup = renderToStaticMarkup(<>
    <SidebarRegion className="sidebar" collapsed={false} t={t}
      onNewSession={noop} onOpenPalette={noop} paletteShortcut="Cmd+K"
      onOpenTrash={noop} onOpenAutomation={noop} onOpenSettings={noop}
      resize={{ min: 180, max: 400, value: 240, onPointerDown: noop, onKeyDown: noop, onReset: noop }}
      projectTree={{ onOpenTopic: noop, onCreateTopic: noop, onTopicsChanged: noop }} />
    <AppBottomRegions terminal={{ surfaceVisible: !automation, open: !automation,
      contentVisible: false, remoteSurface: false, t,
      panel: { tabId: "tab", open: !automation, onClose: noop },
      resizer: { min: 100, max: 400, value: 240, onPointerDown: noop, onKeyDown: noop, onReset: noop },
    }} />
  </>);
  const dom = new JSDOM(markup);
  const doc = dom.window.document;
  assert.equal(doc.querySelectorAll(".terminal-drawer").length, 1, "terminal host survives page projection");
  assert.equal(doc.querySelector(".terminal-drawer")?.hasAttribute("inert"), automation);
  assert.equal(doc.querySelectorAll(".terminal-drawer-resizer").length, automation ? 0 : 1);
  assert.equal(doc.querySelectorAll(".sidebar-collapse-toggle").length, 0, "the workbench sidebar has no collapse toggle");
  const automationButtons = [...doc.querySelectorAll("button")].filter(button => button.querySelector(".lucide-alarm-clock"));
  assert.equal(automationButtons.length, 1, "the workbench sidebar keeps exactly one Automation entry");
  const trashButtons = [...doc.querySelectorAll("button")].filter(button => button.querySelector(".lucide-trash-2"));
  assert.equal(trashButtons.length, 1, "the workbench sidebar keeps the Trash entry");
  const searchButtons = [...doc.querySelectorAll("button")].filter(button => button.querySelector(".lucide-search"));
  assert.equal(searchButtons.length, 1, "the command-palette search entry moves from the topic bar to the sidebar footer");
  assert.ok(searchButtons[0]?.classList.contains("sidebar__utility-button"), "the search entry occupies a sidebar utility slot");
  assert.equal(doc.querySelectorAll(".sidebar__utility-button").length, 4, "the sidebar footer allocates one slot to search");
  dom.window.close();
}
console.log("automation regions: shared layout page projection passed");
