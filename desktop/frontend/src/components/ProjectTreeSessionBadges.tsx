import { GitBranch } from "lucide-react";
import { useT } from "../lib/i18n";

export function ProjectTreeSessionBadges({ node, forkedFromLabel, recoveryLabel }: {
  node: { historical?: boolean; historicalBranch?: boolean };
  forkedFromLabel?: string;
  recoveryLabel?: string;
}) {
  const t = useT();
  return <>
    {node.historical && <span className="project-tree__topic-recovery">{t(node.historicalBranch ? "history.branchBadge" : "history.legacyBadge")}</span>}
    {forkedFromLabel && <span className="project-tree__topic-recovery" title={forkedFromLabel}><GitBranch size={10} />{forkedFromLabel}</span>}
    {recoveryLabel && <span className="project-tree__topic-recovery" title={recoveryLabel}>{recoveryLabel}</span>}
  </>;
}
