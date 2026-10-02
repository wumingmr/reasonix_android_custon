import type { Meta, TabMeta } from "./types";

export function runtimeReadyForSubmit(meta?: Meta): boolean {
  if (!meta || meta.ready !== true || meta.startupErr) return false;
  return !meta.runtime || meta.runtime.phase === "ready";
}

// A local durable identity remains readable when execution cannot acquire a
// lease or finish recovery. Remote tabs keep their negotiated reader route.
export function needsColdHistory(meta?: Meta | TabMeta): boolean {
  return Boolean(meta && !meta.remote && (!meta.ready || meta.startupErr)
    && (meta.sessionPath || meta.session?.sessionId));
}

export function metaWithoutCanonicalTodos(meta?: Meta): Meta | undefined {
  if (!meta || meta.canonicalTodos === undefined) return meta;
  return { ...meta, canonicalTodos: undefined };
}
