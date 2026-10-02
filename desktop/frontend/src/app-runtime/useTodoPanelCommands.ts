import { useEffect, useMemo, useState } from "react";
import { useCommittedCommand } from "../lib/useCommittedCommand";
import type { Todo } from "../lib/tools";
import {
  dismissedTodoKeyForScope,
  resolveTodoPanelTodos,
  scopedTodoBatchKey,
  scopedTodoDismissalKey,
  shouldShowTodoPanel,
  todoBatchKey,
  todoContinueTarget,
  todoDismissalKey,
  todoPanelScope,
} from "../lib/todoVisibility";
import type { Translator } from "../lib/i18n";
import type { Item } from "../lib/useController";
import type { TabMeta } from "../lib/types";
import type { useSessionOperations } from "./useSessionOperations";

export type TodoPanelCommandsInput = {
  items: readonly Item[];
  running: boolean;
  pendingPrompt: boolean;
  meta: {
    canonicalTodos?: Todo[] | null;
    sessionPath?: string;
    eventChannel?: string;
  } | undefined | null;
  activeTab: TabMeta | undefined;
  activeTabId: string | undefined;
  remote: boolean;
  remoteReady: boolean;
  controllerReady: boolean;
  sessionKey: string;
  operations: ReturnType<typeof useSessionOperations>;
  t: Translator;
  ports: {
    remoteSend(text: string): Promise<void>;
    sendToTab(tabId: string, text: string): Promise<void>;
  };
};

/**
 * Owns the pinned task list above the composer: the canonical todo_write
 * projection and the dismiss/continue commands. The shared runtime snapshot
 * is the only current-state source; transcript tool cards remain history and
 * are never replayed into the live panel. Dismissal is local presentation state
 * for this mounted UI and cannot outlive a real turn boundary.
 */
export function useTodoPanelCommands(input: TodoPanelCommandsInput) {
  const { activeTab, activeTabId, remote, t, ports } = input;
  const metaTodos = input.meta?.canonicalTodos;
  const todos = useMemo(() => resolveTodoPanelTodos(metaTodos), [metaTodos]);
  // Dismissal is a view preference for this mounted UI only. Persisting a todo
  // fingerprint made stale progress survive real turn boundaries and become a
  // second business-state store.
  const [dismissedTodoKeys, setDismissedTodoKeys] = useState<Set<string>>(() => new Set());

  useEffect(() => {
    // turn_started publishes an authoritative empty list before any new
    // todo_write. Dropping presentation keys here prevents an identical list
    // in the next turn from inheriting the previous turn's dismissal.
    if (todos.length === 0) setDismissedTodoKeys(new Set());
  }, [todos]);
  const todoKey = useMemo(() => todoDismissalKey(todos), [todos]);
  const todoBatch = useMemo(() => todoBatchKey(todos), [todos]);
  const todoScope = useMemo(
    () => todoPanelScope({ activeTab, activeTabId, eventChannel: remote ? undefined : input.meta?.eventChannel }),
    [activeTab, activeTabId, remote, input.meta?.eventChannel],
  );
  const dismissedTodo = useMemo(
    () => dismissedTodoKeyForScope(todoScope, dismissedTodoKeys, todoKey),
    [dismissedTodoKeys, todoKey, todoScope],
  );
  const scopedTodoKey = useMemo(() => scopedTodoDismissalKey(todoScope, todoKey), [todoKey, todoScope]);
  const scopedTodoBatch = useMemo(() => scopedTodoBatchKey(todoScope, todoBatch), [todoBatch, todoScope]);
  const showTodos = shouldShowTodoPanel(todoKey, dismissedTodo, todos);
  const dismissTodos = useCommittedCommand(() => {
    if (!scopedTodoKey) return;
    setDismissedTodoKeys((current) => {
      if (current.has(scopedTodoKey)) return current;
      const next = new Set(current);
      next.add(scopedTodoKey);
      return next;
    });
  });
  const handleTodoContinue = useCommittedCommand(() => {
    const targetTabId = todoContinueTarget(activeTabId, activeTabId, {
      ready: remote ? input.remoteReady : input.controllerReady,
      readOnly: Boolean(activeTab?.readOnly),
      running: input.running,
      pendingPrompt: input.pendingPrompt,
    });
    if (!targetTabId) return;
    const prompt = t("todo.continue");
    if (remote) {
      void ports.remoteSend(prompt);
      return;
    }
    void ports.sendToTab(targetTabId, prompt);
  });

  return { showTodos, scopedTodoBatch, todos, dismissTodos, handleTodoContinue };
}
