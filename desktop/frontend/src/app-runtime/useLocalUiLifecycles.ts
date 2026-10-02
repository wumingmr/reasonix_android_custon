import { useEffect, useRef } from "react";
import { activeTabMirror } from "./activeTabMirror";

export function useRetiredProjectTreeUiMigration() {
  useEffect(() => {
    try {
      localStorage.removeItem("projectTree:timeFilter");
      localStorage.removeItem("projectTree:workbenchOrganize");
    } catch {
      /* localStorage unavailable */
    }
  }, []);
}

export function useDecisionSurfaceFocus(input: {
  surface: string | null;
  activeTabId?: string | null;
  closeOverlays: () => void;
}) {
  const { surface, activeTabId, closeOverlays } = input;
  const previous = useRef<string | null>(null);
  const surfaceRef = useRef<string | null>(surface);
  surfaceRef.current = surface;
  useEffect(() => {
    if (surface) {
      closeOverlays();
      previous.current = surface;
      return;
    }
    const hadSurface = previous.current !== null;
    previous.current = null;
    if (!hadSurface) return;
    const tabAtRelease = activeTabId;
    const frame = requestAnimationFrame(() => {
      if (surfaceRef.current !== null || activeTabMirror().current !== tabAtRelease) return;
      (document.getElementById("composer-input") as HTMLTextAreaElement | null)?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [activeTabId, closeOverlays, surface]);
}

export function useActiveTabUiReset(input: {
  activeTabId?: string | null;
  setClearPending: (value: boolean) => void;
  setInsertTarget: (value: "composer") => void;
}) {
  const { activeTabId, setClearPending, setInsertTarget } = input;
  useEffect(() => {
    setClearPending(false);
    setInsertTarget("composer");
  }, [activeTabId, setClearPending, setInsertTarget]);
}

export function useVerificationRevealReset(input: {
  activeTabId?: string | null;
  completionSummary: unknown;
  sessionPath?: string;
  turnStartAt?: number | null;
  reset: (value: null) => void;
}) {
  const { activeTabId, sessionPath, turnStartAt, reset } = input;
  useEffect(() => { reset(null); }, [activeTabId, sessionPath, reset, turnStartAt]);
}
