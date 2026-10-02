import { useRef, useState } from "react";
import { app } from "./bridge";
import { useT } from "./i18n";
import { useToast } from "./toast";
import { sessionTitleErrorKey, sessionTitleSelector } from "./sessionTitleOperation";

export function useSessionTitleOperation(refresh: () => Promise<void>, onTopicsChanged?: () => Promise<void> | void) {
  const [renaming, setRenaming] = useState<Set<string>>(() => new Set());
  const operations = useRef(new Map<string, string>());
  const requestSequence = useRef(0);
  const t = useT();
  const { showToast } = useToast();
  const rename = async (target: string) => {
    if (operations.current.has(target)) return;
    const requestId = `${target}:${++requestSequence.current}`;
    operations.current.set(target, requestId);
    setRenaming(current => new Set(current).add(target));
    try {
      const result = await app.AIRenameSessionTarget(sessionTitleSelector(target));
      await refresh();
      await onTopicsChanged?.();
      if (result.title) showToast(t("projectTree.aiRenameDone", { title: result.title }));
    } catch (error) {
      showToast(t(sessionTitleErrorKey(error)), "error");
    } finally {
      if (operations.current.get(target) === requestId) {
        operations.current.delete(target);
        setRenaming(current => {
          const next = new Set(current);
          next.delete(target);
          return next;
        });
      }
    }
  };
  return { renaming, rename };
}
