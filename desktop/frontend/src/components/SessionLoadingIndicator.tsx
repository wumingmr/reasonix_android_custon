import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { useT } from "../lib/i18n";
import type { SessionAvailability } from "../lib/sessionAvailability";

/** Presentation deadlines only; neither data fetching nor ready content waits. */
export const SESSION_LOADING_DELAY_MS = 250;
export const SESSION_LOADING_DETAIL_MS = 2000;

export function SessionLoadingIndicator({ active, identity, source = "history" }: {
  active: boolean;
  identity: string;
  source?: SessionAvailability["source"];
}) {
  // Each pending episode owns its timers. Switching identity or completing a
  // load unmounts that episode, including when the next load uses the same tab.
  return active ? <PendingIndicator key={identity} source={source} /> : null;
}

function PendingIndicator({ source }: { source: SessionAvailability["source"] }) {
  const t = useT();
  const [stage, setStage] = useState<"quiet" | "loading" | "slow">("quiet");
  useEffect(() => {
    let current = true;
    const loading = setTimeout(() => { if (current) setStage("loading"); }, SESSION_LOADING_DELAY_MS);
    const slow = setTimeout(() => { if (current) setStage("slow"); }, SESSION_LOADING_DETAIL_MS);
    return () => { current = false; clearTimeout(loading); clearTimeout(slow); };
  }, []);
  if (stage === "quiet") return null;
  const label = t(stage !== "slow" || source === "runtime" ? "common.loading"
    : source === "connection" ? "remoteSurface.connecting" : "sessionRecovery.loadingHistory");
  return <div className="session-loading-indicator" role="status" aria-live="polite">
    <Loader2 size={16} className="session-recovery__spinner" aria-hidden="true" />
    <span>{label}</span>
  </div>;
}
