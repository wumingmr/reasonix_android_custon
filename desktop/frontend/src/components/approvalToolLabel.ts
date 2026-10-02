import type { Translator } from "../lib/i18n";
import { isShellToolName, isPowerShellToolName } from "../lib/shellToolIdentity";

export function approvalToolLabel(tool: string, t: Translator): string {
  if (isPowerShellToolName(tool)) return "PowerShell";
  if (isShellToolName(tool)) return t("approval.toolLabelBash");
  switch (tool) {
    case "bash": return t("approval.toolLabelBash");
    case "edit_file": return t("approval.toolLabelEditFile");
    case "write_file": return t("approval.toolLabelWriteFile");
    case "multi_edit": return t("approval.toolLabelMultiEdit");
    case "move_file": return t("approval.toolLabelMoveFile");
    case "web_fetch": return t("approval.toolLabelWebFetch");
    case "run_skill": return t("approval.toolLabelRunSkill");
    case "remember": return t("approval.toolLabelRemember");
    case "forget": return t("approval.toolLabelForget");
    case "sandbox_escape": return t("approval.toolLabelSandboxEscape");
    case "config_write": return t("approval.toolLabelConfigWrite");
    case "plan_mode_read_only_command": return t("approval.toolLabelPlanModeReadOnly");
    case "exit_plan_mode": return t("approval.toolLabelExitPlan");
    default: return tool;
  }
}
