// Adapted from DeepSeek Harness c291e7961a, ui-tool/components/ToolRow.tsx (MIT).
// Reasonix provides authorized content and inspection through slots, without Cordis.
import { useState, type ReactNode } from "react";
import { ScanSearch } from "lucide-react";
import { useT } from "../../lib/i18n";
import { presentError } from "../../lib/errorPresentation";
import { DisclosureRow } from "./DisclosureRow";
import { StateDot, type StateDotState } from "./StateDot";
import css from "./ToolRow.styles";

export function ToolRow({ icon, title, summary, state, dot, statusLabel, errorSummary, children, inspectLabel, inspect, beforeToggle }: {
  dot?: StateDotState;
  icon: ReactNode; title: string; summary: string; state: "running" | "done" | "error" | "stopped" | "unknown";
  statusLabel: string; errorSummary?: string; children: ReactNode;
  inspectLabel: string; inspect: (trigger: HTMLElement) => void; beforeToggle: () => void;
}) {
  const [open, setOpen] = useState(false);
  const t = useT();
  const failureLine = state === "error" && errorSummary ? presentError(errorSummary, t).summary : undefined;
  const leading = dot ? <StateDot state={dot} /> : state === "error" || state === "stopped"
    ? <StateDot state={state === "error" ? "error" : "warning"} /> : icon;
  return <div className={`${css.root} chat-tool`} data-state={state}>
    {state !== "done" && <span className="sr-only">{statusLabel}</span>}
    <DisclosureRow icon={leading} title={title} open={open} expandable expandOnRowClick keepContentWhenOpen
      rowClassName={css.row} leadingClassName={css.leading} titleClassName={css.title} chevronClassName={css.chevron}
      onToggle={() => { beforeToggle(); setOpen(value => !value); }}
      collapsedContent={(failureLine || summary) && <><span className={css.sep} aria-hidden />
        <span className={`${css.summary}${failureLine ? ` ${css.errorSummary}` : ""}`}>{failureLine || summary}</span></>}>
      <div className={css.bodyWrap}>
        {children}
        <button className={css.inspectButton} type="button" onClick={event => inspect(event.currentTarget)}><ScanSearch size={12} />{inspectLabel}</button>
      </div>
    </DisclosureRow>
  </div>;
}
