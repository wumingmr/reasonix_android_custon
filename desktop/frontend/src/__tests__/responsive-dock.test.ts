import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { resolveWorkspacePanelPlacement } from "../lib/workspaceLayout";

const dom = new JSDOM("", { url: "http://localhost" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
const { useLayoutStore, saveRightDockTreeWidth } = await import("../store/layout");
const { setViewportSize, useWindowChromeStore } = await import("../store/windowChrome");
const { loadOptionalLayoutSize } = await import("../lib/layoutPreferences");
const layout = useLayoutStore.getState;
setViewportSize(1600, 900);
layout().setWorkspacePanelOpen(true);
assert.equal(layout().rightDockTreeWidth, 720, "first opening uses the current viewport, not startup width");
assert.equal(loadOptionalLayoutSize("rightDockTreeWidth"), null, "automatic sizing does not persist a manual preference");
layout().setRightDockTreeWidth(810);
saveRightDockTreeWidth(810);
layout().setWorkspacePanelOpen(false);
setViewportSize(800, 900);
layout().setWorkspacePanelOpen(true);
assert.equal(layout().rightDockTreeWidth, 810, "reopening in a narrow window retains the preferred width");

const placement = (viewportWidth: number, preferredWidth = layout().rightDockTreeWidth) => resolveWorkspacePanelPlacement({
  viewportWidth, sidebarCollapsed: viewportWidth < 1024, sidebarWidth: 264,
  chatMinWidth: 400, resizerWidth: 8, open: true, maximized: false,
  preferredWidth, minWidth: 300, minRenderWidth: 300,
});
assert.equal(placement(800).renderWidth, 392);
assert.equal(placement(800).gridOpen, true);
assert.equal(placement(767).renderWidth, 767);
assert.equal(placement(767).overlay, true);
assert.equal(placement(767).gridOpen, false);
assert.equal(placement(360).renderWidth, 360, "fullscreen does not impose a desktop minimum");
assert.equal(placement(768).gridOpen, true, "the breakpoint is exclusive");
assert.equal(placement(1600).renderWidth, 810, "rewidening restores the preference");
assert.equal(placement(2400, 2000).renderWidth, 1680, "right dock is capped at 70 percent");
assert.equal(loadOptionalLayoutSize("rightDockTreeWidth"), 810, "responsive concessions never persist");
useWindowChromeStore.setState({ narrowSidebarExpanded: true });
setViewportSize(900, 900);
assert.equal(useWindowChromeStore.getState().narrowSidebarExpanded, true);
setViewportSize(1100, 900);
setViewportSize(900, 900);
assert.equal(useWindowChromeStore.getState().narrowSidebarExpanded, false, "crossing a breakpoint resets the narrow override");
useWindowChromeStore.setState({ narrowSidebarExpanded: true });
layout().setWorkspacePanelOpen(false);
layout().setWorkspacePanelOpen(true);
assert.equal(useWindowChromeStore.getState().narrowSidebarExpanded, false, "opening the dock frees narrow sidebar space");
dom.window.close();
console.log("responsive dock: first open, retained preferences, breakpoints and width bounds passed");
