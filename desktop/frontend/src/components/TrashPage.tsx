import type { SessionMeta } from "../lib/types";
import { useT } from "../lib/i18n";
import { ManagementPageShell } from "./ManagementPageShell";
import { ArchivedSessionsList } from "./ArchivedSessionsList";
import "./TrashPage.css";

export function TrashPage({ active, onBack, list, restore, purge, onOpenSession }: {
  onOpenSession: React.ComponentProps<typeof ArchivedSessionsList>["onOpenSession"];
  active: boolean; onBack: () => void; list: () => Promise<SessionMeta[]>;
  restore: (path: string) => Promise<void>; purge: (path: string) => Promise<void>;
}) {
  const t = useT();
  return <ManagementPageShell active={active} onBack={onBack} className="trash-center"
    title={t("history.archivedSection")} description={t("history.archiveExplanation")}>
    <ArchivedSessionsList active={active} onOpenSession={onOpenSession} legacyList={list} legacyRestore={restore} legacyPurge={purge} />
  </ManagementPageShell>;
}
