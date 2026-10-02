import { useLayoutEffect, useState } from "react";
import {
  createFileNavigationOwner,
  currentFileNavigationOwner,
  setFileNavigationOwner,
} from "../lib/fileNavigationCommands";
import type { FileNavigationOwner } from "../lib/fileNavigationOwner";

type Runtime = { owner: FileNavigationOwner; attachment: number };

/**
 * The navigation owner of this running app instance. The instance is created
 * once and registered so the compatibility entry points reach the same records;
 * it is disposed when the runtime goes away, which cancels every record, every
 * pending open command and every one-shot operation it still held.
 */
export function useFileNavigationRuntime(): FileNavigationOwner {
  const [runtime] = useState<Runtime>(() => ({ owner: createFileNavigationOwner(), attachment: 0 }));
  useLayoutEffect(() => {
    const attachment = ++runtime.attachment;
    setFileNavigationOwner(runtime.owner);
    // StrictMode reconnects effects synchronously without ending the runtime,
    // so only the last attachment may unregister and dispose.
    return () => queueMicrotask(() => {
      if (runtime.attachment !== attachment) return;
      if (currentFileNavigationOwner() === runtime.owner) setFileNavigationOwner(null);
      runtime.owner.dispose();
    });
  }, [runtime]);
  return runtime.owner;
}
