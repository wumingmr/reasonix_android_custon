import { useCallback, useEffect, type RefObject } from "react";

import type {
  ModelInfo,
  SessionDraftSettings,
  SessionDraftSubmissionView,
  SessionDraftView,
} from "../generated/desktopContract.generated";
import { app } from "../lib/bridge";

export function canonicalJSON(value: unknown): string {
  return JSON.stringify(value, (_key, item) => item && typeof item === "object" && !Array.isArray(item)
    ? Object.fromEntries(Object.keys(item).sort().map(key => [key, item[key]])) : item);
}

export function sameDraftSettings(left: SessionDraftSettings, right: SessionDraftSettings): boolean {
  // An inherited model is a live projection, not an editor change. Keep this
  // comparison aligned with the backend draft digest for saves and submission.
  if (left.modelSource === "default" && right.modelSource === "default") {
    return canonicalJSON({ ...left, model: "" }) === canonicalJSON({ ...right, model: "" });
  }
  return canonicalJSON(left) === canonicalJSON(right);
}

export function draftSubmissionLocksEditing(operation?: SessionDraftSubmissionView | null): boolean {
  return Boolean(operation && !["cancelled", "terminal_failed"].includes(operation.phase));
}

export type InheritedDraftModelEntry = {
  draft: SessionDraftView;
  settings: SessionDraftSettings;
  models?: ModelInfo[];
  generation: number;
  lifecycle: "active" | "converted" | "discarded";
  preparingSubmission: boolean;
  operation?: SessionDraftSubmissionView;
  modelReadVersion: number;
};

export function applyInheritedModel(entry: InheritedDraftModelEntry, settings: SessionDraftSettings, readVersion: number) {
  if (entry.modelReadVersion !== readVersion || entry.settings.modelSource !== "default"
    || settings.modelSource !== "default") return;
  entry.settings = { ...entry.settings, model: settings.model };
  entry.draft = { ...entry.draft, settings: { ...entry.draft.settings, model: settings.model } };
}

export function useInheritedDraftModels<T extends InheritedDraftModelEntry>(
  entriesRef: RefObject<Map<string, T>>,
  disposed: RefObject<boolean>,
  publish: (draftId?: string | null) => void,
) {
  const refresh = useCallback(async () => {
    const targets = [...entriesRef.current.values()]
      .filter((entry) => entry.lifecycle === "active" && entry.settings.modelSource === "default"
        && !entry.preparingSubmission && !draftSubmissionLocksEditing(entry.operation))
      .map((entry) => ({ id: entry.draft.id, entry, generation: entry.generation, readVersion: ++entry.modelReadVersion }));
    await Promise.all(targets.map(async (target) => {
      try {
        const context = await app.GetDraftContext(target.id);
        const entry = entriesRef.current.get(target.id);
        if (disposed.current || !entry || entry !== target.entry || entry.lifecycle !== "active" || entry.generation !== target.generation
          || entry.modelReadVersion !== target.readVersion || entry.settings.modelSource !== "default"
          || context.draft.settings.modelSource !== "default" || entry.preparingSubmission
          || draftSubmissionLocksEditing(entry.operation)) return;
        entry.models = context.models ?? [];
        applyInheritedModel(entry, context.draft.settings, target.readVersion);
        publish(target.id);
      } catch {
        // Keep the last effective model until the next catalog refresh or open.
      }
    }));
  }, [disposed, entriesRef, publish]);

  useEffect(() => {
    const listener = () => { void refresh(); };
    window.addEventListener("reasonix:model-catalog-changed", listener);
    return () => window.removeEventListener("reasonix:model-catalog-changed", listener);
  }, [refresh]);
}
