import type { StreamAttemptAction } from "./types";

export interface WireStreamAttempt {
  id: string;
  action: StreamAttemptAction;
  attempt?: number;
  max?: number;
  /** Fixed enum only: connection_reset | premature_eof | idle_timeout */
  reason?: string;
}
export interface WireCompaction {
  trigger?: string; // "auto" | "manual"
  messages?: number; // done: how many messages were folded into the summary
  summary?: string; // done: the briefing (empty on an aborted pass)
  archive?: string; // done: archive path, if any
}
export interface WireSessionOperation {
  operationId: string;
  kind: string;
  activity: string;
  status: string;
  operationRevision?: number;
  runtimeEpoch?: string;
  errorCode?: string;
  detail?: string;
  applied?: boolean;
  inputTokens?: number;
  resultTokens?: number;
  messages?: number;
  summary?: string;
  archive?: string;
}
