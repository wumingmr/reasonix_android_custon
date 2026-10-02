import { act } from "react";
import assert from "node:assert/strict";
import { getTranscriptStore } from "../../lib/transcriptStore";
import type { useController } from "../../lib/useController";

export const flushPromises = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));

export async function verifyEarlyReadableHistory(
  controller: () => ReturnType<typeof useController> | undefined,
  release: () => void, flush: () => Promise<void>,
  waitFor: (label: string, predicate: () => boolean) => Promise<void>,
) {
  await act(async () => { release(); await flush(); });
  await waitFor("tab-b early readable history", () =>
    controller()?.state.items.some(item => item.kind === "user" && item.text === "early B") ?? false);
  assert.equal(controller()?.state.hydrating, false, "early history is readable before runtime activation");
  assert.equal(controller()?.state.backendActivationPending, true, "readable history retains the runtime write fence");
}

export async function verifyDetachedPinRelease(
  controller: () => ReturnType<typeof useController> | undefined, flush: () => Promise<void>,
) {
  const store = getTranscriptStore();
  store.setPinned("tab-h", true);
  await act(async () => { controller()?.commitSingleSurfaceNavigation("tab-g"); await flush(); });
  assert.equal(store.tabIsPinned("tab-h"), false, "a detached runtime cannot pin its renderer history forever");
}
