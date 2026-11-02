import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { app } from "../lib/bridge";
import { desktopHost } from "../lib/desktopHost";
import { claimStudioIntroduction, isStableOneVersion, studioIntroductionPending } from "../lib/studioDiscovery";
import { useUpdater } from "../lib/useUpdater";

const StudioIntroduction = lazy(() => import("../components/StudioIntroduction").then((module) => ({ default: module.StudioIntroduction })));

/** Optional campaign, subordinate to setup, user input, tasks and updates. */
export function StudioDiscoveryLifecycle({ safeToShow, stableRelease }: { safeToShow: boolean; stableRelease: boolean }) {
  const [eligible, setEligible] = useState(false);
  const [open, setOpen] = useState(false);
  const interacted = useRef(false);
  const { status } = useUpdater();
  const safe = safeToShow && (status.kind === "idle" || status.kind === "upToDate");

  useEffect(() => {
    if (!stableRelease || desktopHost().kind === "none" || !studioIntroductionPending()) return;
    let cancelled = false;
    void Promise.all([app.Version(), app.NeedsOnboarding()]).then(([version, needsSetup]) => {
      if (!cancelled) setEligible(isStableOneVersion(version) && !needsSetup);
    }).catch(() => { /* Promotion must never block startup. */ });
    return () => { cancelled = true; };
  }, [stableRelease]);

  useEffect(() => {
    const markInteraction = () => { interacted.current = true; };
    document.addEventListener("pointerdown", markInteraction, true);
    document.addEventListener("keydown", markInteraction, true);
    return () => {
      document.removeEventListener("pointerdown", markInteraction, true);
      document.removeEventListener("keydown", markInteraction, true);
    };
  }, []);

  useEffect(() => {
    if (!eligible || !stableRelease || !safe || open) return;
    const timer = window.setTimeout(() => {
      if (interacted.current || document.visibilityState === "hidden" || document.querySelector('[data-app-overlay], [role="dialog"], [aria-modal="true"]')) return;
      if (claimStudioIntroduction()) setOpen(true);
    }, 1500);
    return () => window.clearTimeout(timer);
  }, [eligible, stableRelease, safe, open]);

  // New work or approvals take precedence even if they arrive after it opens.
  useEffect(() => { if (!safe) setOpen(false); }, [safe]);
  return open ? <Suspense fallback={null}><StudioIntroduction onClose={() => setOpen(false)} /></Suspense> : null;
}
