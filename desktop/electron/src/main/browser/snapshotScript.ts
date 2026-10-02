/// <reference lib="dom" />
// Wire types only. The version-qualified Playwright adapter owns semantics;
// no handwritten fallback is shipped when its build contract fails.
export interface SnapshotInput { key: string; snapshotId: string; prefix: string; selector: string; budget: number }
export interface SnapshotOutput { docId: string; tree: string; refs: number; nodes: number; truncated: number }
export interface PageRegistry { docId: string; snapshotId: string; refs: Map<string, Element> }
