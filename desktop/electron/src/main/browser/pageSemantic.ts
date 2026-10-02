/// <reference lib="dom" />
import { InjectedScript } from "reasonix-playwright-injected";
import type { PageRegistry, SnapshotInput, SnapshotOutput } from "./snapshotScript.js";

type Registry = PageRegistry & { kernel: InjectedScript };
export function pageRegistry(key: string): Registry {
  const host = window as unknown as Record<string, unknown>;
  let registry = host[key] as Registry | undefined;
  if (!registry?.kernel) {
    registry = { docId: `${performance.timeOrigin}:${Math.random().toString(36).slice(2)}`, snapshotId: "", refs: new Map(), kernel: new InjectedScript(window, { isUnderTest: false, sdkLanguage: "javascript", testIdAttributeName: "data-testid", stableRafCount: 1, browserName: "chromium", customEngines: [], isUtilityWorld: true }) };
    Object.defineProperty(host, key, { value: registry, configurable: true, enumerable: false });
  }
  return registry;
}

export function pageSemanticSnapshot(input: SnapshotInput): SnapshotOutput {
  const registry = pageRegistry(input.key);
  registry.snapshotId = input.snapshotId;
  registry.refs = new Map();
  const root = input.selector ? registry.kernel.querySelectorAll(registry.kernel.parseSelector(input.selector), document) : [document.body ?? document.documentElement];
  if (root.length > 1) throw new Error("ambiguous selector: snapshot scope matches multiple elements");
  if (!root.length) return { docId: registry.docId, tree: `(no element matches selector ${JSON.stringify(input.selector)})`, refs: 0, nodes: 0, truncated: 0 };
  const snapshot = registry.kernel.ariaSnapshotWithRefs(root[0], { mode: "ai" });
  let next = 0;
  const mapped = new Map<string, string>();
  const lines = snapshot.text.split("\n");
  const limit = Math.min(4000, Math.max(1, input.budget));
  const tree = lines.slice(0, limit).join("\n").replace(/\[ref=([^\]]+)\]/g, (_match, ref: string) => {
    let external = mapped.get(ref);
    if (!external) {
      const matches = registry.kernel.querySelectorAll(registry.kernel.parseSelector(`aria-ref=${ref}`), document);
      if (matches.length !== 1) throw new Error("Playwright reference contract changed");
      external = `${input.prefix}e${++next}`;
      mapped.set(ref, external);
      registry.refs.set(external, matches[0]);
    }
    return `ref=${external}`;
  });
  const truncated = Math.max(0, lines.length - limit);
  return { docId: registry.docId, tree: tree + (truncated ? `\n… (${truncated} more nodes)` : ""), refs: registry.refs.size, nodes: Math.min(limit, lines.length), truncated };
}

export interface QueryInput { key: string; snapshotId: string; docId: string; prefix: string; role?: string; name?: string; text?: string; testId?: string; containerRef?: string; state?: string }
export function pageQuery(input: QueryInput) {
  const registry = pageRegistry(input.key);
  if (registry.snapshotId !== input.snapshotId || registry.docId !== input.docId) return { error: "stale_document" };
  const root = input.containerRef ? registry.refs.get(input.containerRef) : document;
  if (!root || (root instanceof Element && !root.isConnected)) return { error: "stale_document" };
  const quote = (text: string) => JSON.stringify(text);
  let selector: string;
  if (input.role) {
    if (!/^[a-z]+$/.test(input.role)) throw new Error("invalid role");
    selector = `internal:role=${input.role}${input.name !== undefined ? `[name=${quote(input.name)}s]` : ""}`;
  } else if (input.testId !== undefined) selector = `internal:testid=[data-testid=${quote(input.testId)}s]`;
  else if (input.text !== undefined) selector = `internal:text=${quote(input.text)}s`;
  else throw new Error("query requires role, text or testId");
  const matches = registry.kernel.querySelectorAll(registry.kernel.parseSelector(selector), root);
  const refs: string[] = [];
  for (const element of matches.slice(0, 100)) {
    let ref = [...registry.refs].find(([, candidate]) => candidate === element)?.[0];
    if (!ref) {
      if (registry.refs.size >= 4000) throw new Error("reference budget exceeded; observe a smaller scope");
      ref = `${input.prefix}e${registry.refs.size + 1}`; registry.refs.set(ref, element);
    }
    refs.push(ref);
  }
  return { count: matches.length, refs, ambiguous: matches.length > 1, state: input.state && matches.length === 1 ? registry.kernel.elementState(matches[0], input.state).matches : undefined };
}

export function pageReady() { return { ready: document.readyState !== "loading", url: location.href, width: innerWidth, height: innerHeight, scrollX, scrollY }; }
