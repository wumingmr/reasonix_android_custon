import { useEffect, useSyncExternalStore } from "react";

import type { BrowserTabView } from "./browserHost";
import type { FileResourceRef } from "./fileResource";

type Binding = { ref: FileResourceRef; url: string };

const bindings = new Map<string, Binding>();
const listeners = new Set<() => void>();
let revision = 0;

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};
const snapshot = () => revision;

export function bindFileBrowserPreview(tabId: string, ref: FileResourceRef, url: string): void {
  bindings.set(tabId, { ref, url });
  revision += 1;
  listeners.forEach((listener) => listener());
}

export function useFileBrowserPreview(tab: BrowserTabView | undefined): Binding | undefined {
  useSyncExternalStore(subscribe, snapshot, snapshot);
  const binding = tab ? bindings.get(tab.id) : undefined;
  const stale = Boolean(binding && tab?.url !== binding.url);
  useEffect(() => {
    if (stale && tab) bindings.delete(tab.id);
  }, [stale, tab]);
  return stale ? undefined : binding;
}
