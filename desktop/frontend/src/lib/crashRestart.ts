import { desktopHost } from "./desktopHost";
import { t } from "./i18n";

export function crashRestartButton(): HTMLButtonElement {
  const restart = document.createElement("button");
  restart.className = "crash-overlay__restart";
  restart.textContent = t("crash.restart");
  restart.onclick = async () => {
    restart.disabled = true;
    // Resolved at click time through the host adapter, like the send action; a
    // reload still recovers a renderer-only crash when the relaunch is refused.
    const relaunch = desktopHost().app?.RestartApplication;
    try {
      if (!relaunch) throw new Error("no desktop host");
      await relaunch();
    } catch {
      window.location.reload();
    }
  };
  return restart;
}
