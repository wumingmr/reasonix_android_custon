import type { Item } from "./useController";
import { fileIdentity } from "./filePaths";

type ToolItem = Extract<Item, { kind: "tool" }>;
export type TurnFileOperation = "written" | "modified";
export interface TurnFileView {
  path: string;
  toolCallId: string;
  operation: TurnFileOperation;
}

const MUTATION_PATHS: Record<string, { field: string; operation: TurnFileOperation }> = {
  write_file: { field: "path", operation: "written" },
  edit_file: { field: "path", operation: "modified" },
  multi_edit: { field: "path", operation: "modified" },
  notebook_edit: { field: "path", operation: "modified" },
  delete_range: { field: "path", operation: "modified" },
  delete_symbol: { field: "path", operation: "modified" },
  move_file: { field: "destination_path", operation: "written" },
};

export { fileIdentity };
/** Derive file facts only from successful native mutation tools. */
export function deriveTurnFiles(calls: readonly ToolItem[]): TurnFileView[] {
  const files = new Map<string, TurnFileView>();
  for (const call of calls) {
    const definition = MUTATION_PATHS[call.name];
    if (!definition || call.status !== "done" || call.error || call.readOnly) continue;
    if (/\bno changes made\b/i.test(call.output ?? "")) continue;
    let args: Record<string, unknown>;
    try {
      const parsed: unknown = JSON.parse(call.args || "{}");
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) continue;
      args = parsed as Record<string, unknown>;
    } catch { continue; }
    const path = args[definition.field];
    if (typeof path !== "string" || !path.trim()) continue;
    const key = fileIdentity(path);
    if (!key) continue;
    const existing = files.get(key);
    const value: TurnFileView = {
      path: existing?.path ?? path.trim(),
      toolCallId: call.id,
      operation: definition.operation,
    };
    files.set(key, value);
  }
  return [...files.values()];
}
