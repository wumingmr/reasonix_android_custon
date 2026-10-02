import { ErrorMessage } from "../components/ErrorMessage";
import { lazy, Suspense } from "react";
import type { Translator } from "../lib/i18n";
import { RemoteReclaimBanner } from "../components/RemoteReclaimBanner";
import { UpdateBanner } from "../components/UpdateBanner";
import type { HistoricalSessionBannerProps } from "../components/SessionTakeoverDialog";

const SessionRuntimeOverlays = lazy(() => import("../components/SessionTakeoverDialog").then((module) => ({ default: module.SessionRuntimeOverlays })));

export type SessionStatusBannersProps = {
  t: Translator;
  takenOver: boolean;
  reclaimTabId: string;
  reclaimBusyTabId: string | null;
  onReclaim: (tabId: string) => void;
  leaseBlocked: { tabId: string; message: string } | null;
  startupError: string | undefined;
  takeoverDialogTabId: string | null;
  onOpenTakeover: (tabId: string) => void;
  onCloseTakeover: () => void;
  configWarnings: readonly string[];
  onOpenConfigFile: () => void;
  onReloadConfigFile: () => void;
  onDismissConfigWarnings: () => void;
  providerSetupNeeded: boolean;
  needsOnboarding: boolean | null;
  onConfigureProvider: () => void;
  updateChecksEnabled: boolean;
  onShowReleaseNotes: (latest: string) => void;
  historical?: HistoricalSessionBannerProps;
};

/** Presentation-only banner stack between the topic bar and the main pane. */
export function SessionStatusBanners(props: SessionStatusBannersProps) {
  const { t } = props;
  return (
    <>
      {props.takenOver ? (
        <RemoteReclaimBanner
          tabId={props.reclaimTabId}
          busyTabId={props.reclaimBusyTabId}
          onReclaim={props.onReclaim}
        />
      ) : null}
      {props.leaseBlocked ? (
        <div className="banner banner--error">
          <span className="banner__msg"><ErrorMessage error={t("topbar.startupError", { msg: props.leaseBlocked.message })} /></span>
          <span className="banner__spacer" />
          <button type="button" className="btn btn--small" onClick={() => props.onOpenTakeover(props.leaseBlocked!.tabId)}>
            {t("takeover.bannerButton")}
          </button>
        </div>
      ) : props.startupError ? (
        <div className="banner banner--error">
          <span className="banner__msg"><ErrorMessage error={t("topbar.startupError", { msg: props.startupError })} /></span>
        </div>
      ) : null}
      {props.configWarnings.length > 0 && (
        <div className="banner banner--warning banner--actionable">
          <span className="banner__msg" title={props.configWarnings.join("\n")}>
            <ErrorMessage error={t("config.loadWarning", { msg: props.configWarnings.join("\n") })} />
          </span>
          <span className="banner__spacer" />
          <button type="button" className="btn btn--small" onClick={props.onOpenConfigFile}>
            {t("config.openConfig")}
          </button>
          <button type="button" className="btn btn--small" onClick={props.onReloadConfigFile}>
            {t("config.reloadConfig")}
          </button>
          <span className="banner__hint">{t("config.doctorHint")}</span>
          <button type="button" className="btn btn--small" onClick={props.onDismissConfigWarnings}>
            {t("updater.dismiss")}
          </button>
        </div>
      )}
      {props.providerSetupNeeded && (
        <div className="banner banner--warning banner--actionable">
          <span className="banner__msg">{t("onboarding.inlinePrompt")}</span>
          <span className="banner__spacer" />
          <button type="button" className="btn btn--small" onClick={props.onConfigureProvider}>
            {t("onboarding.configureProvider")}
          </button>
        </div>
      )}
      <UpdateBanner
        enabled={props.updateChecksEnabled}
        onShowReleaseNotes={props.onShowReleaseNotes}
      />
      {props.takeoverDialogTabId || props.historical ? <Suspense fallback={null}>
        <SessionRuntimeOverlays takeoverTabId={props.takeoverDialogTabId} onCloseTakeover={props.onCloseTakeover} historical={props.historical} />
      </Suspense> : null}
    </>
  );
}
