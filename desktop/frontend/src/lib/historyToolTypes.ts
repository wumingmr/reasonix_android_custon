export interface HistoryToolCall {
  resultObservation?: import("../generated/desktopContract.generated").ToolObservation;
	partial?: boolean;
	pending?: boolean;
	parentId?: string;
	argChars?: number;
	startedAt?: number;
  id: string;
  name: string;
  arguments: string;
  resolvedName?: string;
  capabilityId?: string;
  resolvedReadOnly?: boolean;
  subject?: string;
  summary?: string;
  diff?: string;
  added?: number;
  removed?: number;
  argumentsArchived?: boolean;
}
