import { create } from "zustand";

export const browserElementDrafts = create<{ target?: { taskId: string; sessionId: string }; pending: Record<string, { id: number; text: string }>; add(taskId: string, sessionId: string, value: unknown): void; remove(key: string): void }>(set => ({
  pending: {},
  add: (taskId, sessionId, value) => set(state => ({ pending: { ...state.pending, [JSON.stringify([taskId, sessionId])]: { id: Date.now(), text: JSON.stringify({ browserElement: value, executableReference: false }, null, 2) } } })),
  remove: key => set(state => { const pending = { ...state.pending }; delete pending[key]; return { pending }; }),
}));
