import { desktopHost } from "../lib/desktopHost";
export interface ForegroundBrowserTurn { taskId?: string; turnId?: string; sessionId?: string; reveal(): void }
export function observeFirstBrowserOpen(current: () => ForegroundBrowserTurn) {
  const browser = desktopHost().browser;
  if (!browser) return () => {};
  const seen = new Map<string, string>();
  let closed = false, revision = 0;
  let known = new Set<string>();
  const off = browser.onTabs(tabs => {
    revision++;
    const additions = tabs.filter(tab => !known.has(tab.id));
    known = new Set(tabs.map(tab => tab.id));
    const { taskId, turnId, sessionId, reveal } = current();
    if (!taskId || !turnId || seen.get(taskId) === turnId || !additions.some(tab => tab.taskId === taskId && (!sessionId || !tab.sessionId || tab.sessionId === sessionId))) return;
    seen.set(taskId, turnId); reveal();
  });
  const initial = revision;
  void browser.list().then(tabs => { if (!closed && initial === revision) known = new Set(tabs.map(tab => tab.id)); }).catch(() => {});
  return () => { closed = true; off(); };
}
