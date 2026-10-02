import type { ComposerInvocation } from "./invocationDisplay";

let nextID = 1;
export function newComposerInvocationID(invocations: ComposerInvocation[]): string {
  // Persisted inputs can contain IDs allocated by a previous renderer.
  let id: string;
  do { id = `invocation-${nextID++}`; }
  while (invocations.some(item => item.id === id));
  return id;
}
