import { useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ArrowUpRight, Layers, X } from "lucide-react";
import { useT } from "../lib/i18n";
import { openExternal } from "../lib/bridge";
import { claimStudioIntroduction, STUDIO_DOWNLOAD_URL } from "../lib/studioDiscovery";
import "./StudioIntroduction.css";

export function StudioIntroduction({ onClose }: { onClose: () => void }) {
  const t = useT();
  const titleId = useId();
  const descriptionId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const continueRef = useRef<HTMLButtonElement>(null);
  const closeRef = useRef(onClose);
  useLayoutEffect(() => { closeRef.current = onClose; }, [onClose]);
  useLayoutEffect(() => {
    const previous = document.activeElement;
    continueRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        closeRef.current();
      }
      if (event.key !== "Tab") return;
      const controls = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled), summary, a[href]") ?? [])
        .filter((element) => element.getClientRects().length > 0);
      const first = controls[0];
      const last = controls[controls.length - 1];
      if (!first || !last) return;
      if (event.shiftKey && (document.activeElement === first || !dialogRef.current?.contains(document.activeElement))) {
        event.preventDefault(); last.focus();
      } else if (!event.shiftKey && (document.activeElement === last || !dialogRef.current?.contains(document.activeElement))) {
        event.preventDefault(); first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);

  return createPortal(
    <div className="modal-backdrop studio-intro-backdrop" data-app-overlay="" role="presentation"
      onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div ref={dialogRef} className="modal studio-intro" role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={descriptionId}>
        <div className="studio-intro__masthead">
          <span className="studio-intro__brand"><Layers size={18} aria-hidden="true" /> REASONIX STUDIO</span>
          <button className="btn btn--small studio-intro__close" type="button" aria-label={t("studio.close")} onClick={onClose}><X size={18} aria-hidden="true" /></button>
        </div>
        <div className="studio-intro__body">
          <h2 id={titleId}>{t("studio.title")}</h2>
          <p id={descriptionId} className="studio-intro__lead">{t("studio.description")}</p>
          <div className="studio-intro__features">
            <div><span>01</span><h3>{t("studio.sessions")}</h3><p>{t("studio.sessionsBody")}</p></div>
            <div><span>02</span><h3>{t("studio.control")}</h3><p>{t("studio.controlBody")}</p></div>
            <div><span>03</span><h3>{t("studio.future")}</h3><p>{t("studio.futureBody")}</p></div>
          </div>
          <p className="studio-intro__reassurance">{t("studio.optional")}</p>
          <details className="studio-intro__details">
            <summary>{t("studio.migrationTitle")}</summary>
            <ul>
              <li>{t("studio.migrationInstall")}</li>
              <li>{t("studio.migrationData")}</li>
              <li>{t("studio.migrationProjects")}</li>
            </ul>
          </details>
        </div>
        <div className="studio-intro__footer">
          <p>{t("studio.downloadHint")}</p>
          <div className="studio-intro__actions">
            <button ref={continueRef} className="btn" type="button" onClick={onClose}>{t("studio.continue")}</button>
            <button className="btn btn--primary" type="button" onClick={() => openExternal(STUDIO_DOWNLOAD_URL)}>{t("studio.download")}<ArrowUpRight size={16} aria-hidden="true" /></button>
          </div>
        </div>
      </div>
    </div>, document.body,
  );
}

export function StudioDiscoveryCard() {
  const t = useT();
  const [open, setOpen] = useState(false);
  return <>
    <div className="studio-discovery">
      <Layers size={24} aria-hidden="true" />
      <div><strong>{t("studio.explore")}</strong><p>{t("studio.cardBody")}</p></div>
      <button className="btn btn--small" type="button" onClick={() => { claimStudioIntroduction(); setOpen(true); }}>{t("studio.learnMore")}<ArrowUpRight size={14} aria-hidden="true" /></button>
    </div>
    {open && <StudioIntroduction onClose={() => setOpen(false)} />}
  </>;
}
