import type { ComposerInsertRequest, ToolApprovalMode, WireApproval } from "../lib/types";

export type ApprovalModalProps = {
  approval: WireApproval;
  onAnswer: (allow: boolean, session: boolean, persist: boolean) => void | Promise<void>;
  onResolveRecovery?: (action: "continue" | "continue_task" | "revise", feedback?: string) => void | Promise<void>;
  onRevisePlan?: (text: string) => void | Promise<void>;
  onExitPlan?: () => void | Promise<void>;
  onStop: () => void | Promise<void>;
  cwd?: string;
  tabId?: string;
  workspaceScopeKey?: string;
  insertRequest?: ComposerInsertRequest | null;
  onRevisionActiveChange?: (active: boolean) => void;
  toolApprovalMode?: ToolApprovalMode;
};
