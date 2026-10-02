import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import {
  loadWorkbenchSortMode,
  WORKBENCH_SORT_CREATED_DEFAULT_MIGRATION_KEY,
  WORKBENCH_SORT_KEY,
} from "../lib/projectTreeTopic";

function installLocalStorage(seed: Record<string, string> = {}): Storage {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
  globalThis.localStorage = dom.window.localStorage;
  for (const [key, value] of Object.entries(seed)) localStorage.setItem(key, value);
  return localStorage;
}

{
  const storage = installLocalStorage();
  assert.equal(loadWorkbenchSortMode(), "created", "a fresh install defaults to creation time");
  assert.equal(storage.getItem(WORKBENCH_SORT_KEY), "created", "the new default is persisted");
  assert.equal(storage.getItem(WORKBENCH_SORT_CREATED_DEFAULT_MIGRATION_KEY), "1", "the reset runs only once");
}

{
  const storage = installLocalStorage({ [WORKBENCH_SORT_KEY]: "updated" });
  assert.equal(loadWorkbenchSortMode(), "created", "an existing updated-time choice is reset on upgrade");
  assert.equal(storage.getItem(WORKBENCH_SORT_KEY), "created", "the legacy choice is overwritten for older readers too");
}

{
  const storage = installLocalStorage({ [WORKBENCH_SORT_KEY]: "updated" });
  assert.equal(loadWorkbenchSortMode(), "created");
  storage.setItem(WORKBENCH_SORT_KEY, "updated");
  assert.equal(loadWorkbenchSortMode(), "updated", "a manual choice made after migration remains authoritative");
}

{
  const storage = installLocalStorage({ [WORKBENCH_SORT_KEY]: "updated" });
  const interruptedStorage = {
    get length() { return storage.length; },
    clear: () => storage.clear(),
    getItem: (key: string) => storage.getItem(key),
    key: (index: number) => storage.key(index),
    removeItem: (key: string) => storage.removeItem(key),
    setItem: (key: string, value: string) => {
      if (key === WORKBENCH_SORT_CREATED_DEFAULT_MIGRATION_KEY) throw new Error("interrupted migration");
      storage.setItem(key, value);
    },
  } satisfies Storage;
  globalThis.localStorage = interruptedStorage;
  assert.equal(loadWorkbenchSortMode(), "created", "an interrupted migration still uses creation time");
  assert.equal(storage.getItem(WORKBENCH_SORT_CREATED_DEFAULT_MIGRATION_KEY), null, "an interrupted migration remains retryable");
  globalThis.localStorage = storage;
  assert.equal(loadWorkbenchSortMode(), "created", "the next launch completes an interrupted migration");
  assert.equal(storage.getItem(WORKBENCH_SORT_CREATED_DEFAULT_MIGRATION_KEY), "1");
}

{
  installLocalStorage({
    [WORKBENCH_SORT_CREATED_DEFAULT_MIGRATION_KEY]: "1",
    [WORKBENCH_SORT_KEY]: "created",
  });
  assert.equal(loadWorkbenchSortMode(), "created", "a migrated creation-time choice remains authoritative");
}

console.log("  PASS  project tree sort preference migration");
