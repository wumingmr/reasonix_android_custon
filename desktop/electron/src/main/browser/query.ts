import type { Params } from "../params.js";
import { str } from "../params.js";
import { browserFailure, staleReference } from "./errors.js";
import type { DocumentRegistry } from "./documents.js";
import type { BrowserTab } from "./surfaceManager.js";
import { frameForRef } from "./refResolver.js";
import { scriptCall } from "./pageScripts.js";
import { REGISTRY_KEY, runInFrame } from "./snapshot.js";

export async function queryElements(tab: BrowserTab, params: Params, documents: DocumentRegistry) {
  const documentToken = str(params, "documentToken");
  const binding = documents.lookup(documentToken);
  if (!binding || binding.tabId !== tab.id || binding.epoch !== tab.epoch) throw staleReference("query needs the current documentToken");
  const containerRef = str(params, "containerRef");
  const frameRef = str(params, "frameRef");
  const target = containerRef || frameRef ? frameForRef(tab.view.page, binding, containerRef || frameRef) : { frame: tab.view.page.mainFrame, binding: binding.frames[0] };
  if (containerRef && frameRef && frameForRef(tab.view.page, binding, frameRef).binding !== target.binding) throw staleReference("container and frame reference belong to different frames");
  if (!target.binding) throw staleReference("snapshot has no frame");
  const criteria = Object.fromEntries(["role", "name", "text", "testId", "state"].filter(key => typeof params[key] === "string").map(key => [key, params[key]]));
  if (criteria.state && !["visible", "hidden", "enabled", "disabled", "checked", "editable"].includes(String(criteria.state))) throw new Error("invalid element state");
  const raw = await runInFrame(tab.view.page, target.frame, scriptCall("pageQuery", { ...criteria, key: REGISTRY_KEY, snapshotId: binding.snapshotId, docId: target.binding.docId, prefix: target.binding.prefix, containerRef }));
  const result = raw as { count?: number; refs?: string[]; ambiguous?: boolean; state?: boolean; error?: string };
  if (result?.error === "stale_document") throw staleReference("query document changed");
  if (!Number.isInteger(result?.count) || !Array.isArray(result?.refs)) throw browserFailure("script_runtime_error", "query returned an invalid result");
  return { ...result, documentToken, url: tab.view.page.getURL() };
}
