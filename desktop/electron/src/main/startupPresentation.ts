export type StartupLifecycle = "starting" | "ready" | "failed";
export type StartupPresentation = "none" | "focus" | "diagnostic";
export type StartupPresentReason = "boot" | "second-instance" | "activate";

// Starting stays hidden, including a second icon click. Failed startups still
// need the recovery page; an existing window is focused, not replaced.
export function startupPresentation(input: {
  serviceReady: boolean;
  hasWindow: boolean;
  lifecycle: StartupLifecycle;
}): StartupPresentation {
  if (input.serviceReady || input.lifecycle === "ready") return "focus";
  if (input.lifecycle === "failed") return "diagnostic";
  return input.hasWindow ? "focus" : "none";
}

export function startupLifecycle(lifecycle: string): StartupLifecycle {
  if (lifecycle === "failed" || lifecycle === "ready") return lifecycle;
  return "starting";
}
