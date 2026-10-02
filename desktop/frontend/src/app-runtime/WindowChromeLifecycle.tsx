import { useEffect } from "react";
import { app } from "../lib/bridge";
import { browserPlatformOverride, normalizeDesktopPlatform } from "../lib/desktopPlatform";
import { setDesktopPlatform, setViewportSize, useWindowChromeStore } from "../store/windowChrome";
import {
  RIGHT_DOCK_TREE_MIN_WIDTH,
  SIDEBAR_MIN_WIDTH,
  saveRightDockTreeWidth,
  saveSidebarWidth,
  useLayoutStore,
} from "../store/layout";

/**
 * Owns the desktop chrome listeners that feed the windowChrome store: the
 * native platform probe, viewport resize, the data-platform attribute and the
 * layout minimum-width guards. Renders nothing; App composes it once beside
 * AppRuntimeEffects so every chrome consumer reads one store.
 */
export function WindowChromeLifecycle() {
  const platform = useWindowChromeStore((state) => state.platform);
  const sidebarWidth = useLayoutStore((state) => state.sidebarWidth);
  const setSidebarWidth = useLayoutStore((state) => state.setSidebarWidth);
  const rightDockTreeWidth = useLayoutStore((state) => state.rightDockTreeWidth);
  const setRightDockTreeWidth = useLayoutStore((state) => state.setRightDockTreeWidth);

  useEffect(() => {
    document.documentElement.setAttribute("data-platform", platform);
  }, [platform]);

  useEffect(() => {
    let cancelled = false;
    const override = browserPlatformOverride();
    if (override) {
      setDesktopPlatform(override);
      return () => {
        cancelled = true;
      };
    }
    void app.Platform()
      .then((value) => {
        if (!cancelled) setDesktopPlatform(normalizeDesktopPlatform(value));
      })
      .catch((e) => {
        console.warn("platform probe failed", e);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const onResize = () => {
      setViewportSize(window.innerWidth, window.innerHeight);
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  useEffect(() => {
    if (sidebarWidth >= SIDEBAR_MIN_WIDTH) return;
    setSidebarWidth(SIDEBAR_MIN_WIDTH);
    saveSidebarWidth(SIDEBAR_MIN_WIDTH);
  }, [setSidebarWidth, sidebarWidth]);

  useEffect(() => {
    if (rightDockTreeWidth >= RIGHT_DOCK_TREE_MIN_WIDTH) return;
    setRightDockTreeWidth(RIGHT_DOCK_TREE_MIN_WIDTH);
    saveRightDockTreeWidth(RIGHT_DOCK_TREE_MIN_WIDTH);
  }, [rightDockTreeWidth, setRightDockTreeWidth]);

  return null;
}
