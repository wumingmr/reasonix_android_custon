/** Durable local-session identity. Paths and workspace selection are not identity. */
export interface SessionRef {
  hostId: string;
  sessionId: string;
}

export interface SessionRuntimeIssue {
  code: "session_lease_held" | "startup_failed";
  message: string;
  retryable: boolean;
  holderPid?: number;
  holderHost?: string;
  acquiredAt?: string;
}
