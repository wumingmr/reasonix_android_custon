import { recordFrontendDiagnostic } from "./frontendDiagnosticBridge";

type RequestStats = { active: number; queued: number };

type RequestDiagnosticOptions = {
  projectKind: string;
  creationTopics: boolean;
  sequence: number;
  stats: () => RequestStats;
};

export function createProjectTreeRequestDiagnostic(options: RequestDiagnosticOptions): (phase: string, fields?: Record<string, unknown>) => void {
  const startedAt = typeof performance !== "undefined" ? performance.now() : Date.now();
  const now = () => typeof performance !== "undefined" ? performance.now() : Date.now();
  const emit = (phase: string, fields: Record<string, unknown> = {}) => {
    const stats = options.stats();
    recordFrontendDiagnostic("workspace", "workspace.session-list", {
      phase,
      sequence: options.sequence,
      durationMs: Math.max(0, now() - startedAt),
      queueDepth: stats.queued,
      activeRequests: stats.active,
      scope: options.projectKind === "global_folder" ? "global" : "project",
      variant: options.creationTopics ? "creation" : "workbench",
      ...fields,
    });
  };
  emit("queued");
  return emit;
}
