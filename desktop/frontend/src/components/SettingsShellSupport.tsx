import { SettingsSelect } from "./SettingsSelect";
import { CircleAlert, CircleCheck, ExternalLink, RefreshCw, SquareTerminal, GitBranch, Info } from "lucide-react";
import { openExternal } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { asArray } from "../lib/array";
import type { SandboxView, ShellCapabilityView } from "../lib/types";
import { CopyButton } from "./CopyButton";

// The Sandbox settings section's shell surface: interpreter preference, the
// current session's bound shell vs what a reload would pick, and diagnostics.
// Windows supports explicit Git Bash selection alongside native PowerShell.
// Automatic selection keeps its PowerShell preference.

function effectiveShellLabel(value: string, t: ReturnType<typeof useT>): string {
  switch (value) {
    case "git-bash": return t("settings.effectiveShellGitBash");
    case "pwsh": return t("settings.effectiveShellPwsh");
    case "powershell": return t("settings.effectiveShellPowershell");
    case "bash": return t("settings.effectiveShellBash");
    case "zsh": return t("settings.effectiveShellZsh");
    case "sh": return t("settings.effectiveShellSh");
    case "auto": return t("common.auto");
    default: return value.trim() || t("common.none");
  }
}

function capabilityLabel(id: string, t: ReturnType<typeof useT>): string {
  switch (id) {
    case "git-bash": return t("settings.effectiveShellGitBash");
    case "powershell": return t("settings.effectiveShellPowershell");
    case "pwsh": return t("settings.effectiveShellPwsh");
    case "zsh": return t("settings.shellCapabilityZsh");
    case "sh": return t("settings.shellCapabilitySh");
    case "git": return t("settings.gitCapability");
    default: return t("settings.effectiveShellBash");
  }
}

function visibleCapabilities(capabilities: ShellCapabilityView[], windows: boolean): ShellCapabilityView[] {
  return capabilities.filter(({ id }) => windows
    ? id === "git-bash" || id === "pwsh" || id === "powershell"
    : id === "bash" || id === "zsh" || id === "sh");
}

function selectedPreference(preference: string, windows: boolean): string {
  const normalized = preference.trim().toLowerCase();
  if (windows) return normalized === "bash" || normalized === "pwsh" || normalized === "powershell" ? normalized : "auto";
  return normalized === "bash" ? "bash" : "auto";
}

function RepairCard({ message, guidance, busy, reloadSession }: {
  message: string;
  guidance?: { manager: string; command?: string } | null;
  busy: boolean;
  reloadSession: () => void;
}) {
  const t = useT();
  return (
    <div className="shell-support__card">
      <div className="shell-support__hint">{message}</div>
      {guidance?.command && (
        <>
          <div className="shell-support__repair-command">
            <code>{guidance.command}</code>
            <CopyButton text={guidance.command} className="btn btn--small" label={t("settings.shellCopyCommand")} />
          </div>
          <div className="shell-support__repair-safety">{t("settings.shellRepairCommandHint")}</div>
        </>
      )}
      <div className="shell-support__actions">
        <button type="button" className="btn btn--small" disabled={busy} onClick={reloadSession}>
          <RefreshCw size={13} aria-hidden="true" />
          <span>{t("settings.shellRepairReload")}</span>
        </button>
      </div>
    </div>
  );
}

function field(label: string, control: React.ReactNode, stacked = false) {
  return (
    <div className={`settings-field${stacked ? " settings-field--stacked" : ""}`}>
      <div className="settings-field__copy">
        <div className="settings-field__copy-body">
          <div className="settings-field__label">{label}</div>
        </div>
      </div>
      <div className="settings-field__control">{control}</div>
    </div>
  );
}

function DetectionRow({ cap, t }: { cap: ShellCapabilityView; t: ReturnType<typeof useT> }) {
  return (
    <tr className={`shell-capability__row${cap.available ? " shell-capability__row--available" : ""}`}>
      <th scope="row"><span className="shell-capability__name">{cap.id === "git" ? <GitBranch size={16} aria-hidden="true" /> : <SquareTerminal size={16} aria-hidden="true" />}<span>{capabilityLabel(cap.id, t)}{cap.id === "git" && <small> · {t("settings.shellDependency")}</small>}</span></span></th>
      <td><span className="shell-capability__status">{cap.available ? <CircleCheck size={16} aria-hidden="true" /> : <CircleAlert size={16} aria-hidden="true" />}{t(cap.available ? "settings.shellDetected" : "settings.shellNotDetected")}</span></td>
      <td><div className="shell-capability__path"><span className="shell-capability__detail" title={cap.path}>{cap.path || "—"}</span>
      {cap.path && <CopyButton text={cap.path} label={t("settings.copyEnvironmentPath", { name: capabilityLabel(cap.id, t) })} showInlineLabel={false} />}</div></td>
    </tr>
  );
}

export function ShellInterpreterFields({
  sb,
  windows,
  busy,
  setShell,
  reloadSession,
}: {
  sb: SandboxView;
  windows: boolean;
  busy: boolean;
  setShell: (prefer: string) => void;
  reloadSession: () => void;
}) {
  const t = useT();
  const capabilities = visibleCapabilities(asArray(sb.shellCapabilities), windows);
  if (windows) capabilities.sort((a, b) => ["git-bash", "pwsh", "powershell"].indexOf(a.id) - ["git-bash", "pwsh", "powershell"].indexOf(b.id));
  const preference = (sb.shell || "auto").trim().toLowerCase();
  const selected = selectedPreference(preference, windows);
  const currentShell = String(sb.effectiveShell || selected);
  const resolvedShell = String(sb.resolvedShell || selected);
  const autoLabel = windows ? t("settings.shellAutoWindows") : t("settings.shellAuto");
  const git = sb.gitCapability ?? null;
  const bashMissing = !windows && !capabilities.some((capability) => capability.id === "bash" && capability.available);
  const nativeFallback = sb.resolvedShell === "zsh" || sb.resolvedShell === "sh";
  const gitBashMissing = windows && !capabilities.some((capability) => capability.id === "git-bash" && capability.available);

  return (
    <>
      <div className="sandbox-group sandbox-group--shell">
      <h3>{t("settings.shellInterpreter")}</h3>
        <div className="sandbox-shell-choice">
        <span className="sandbox-shell-current">{t("settings.shellCurrent")}<strong>{effectiveShellLabel(currentShell, t)}</strong></span>
        <SettingsSelect className="mem-select set-grow" value={preference} selectedLabel={preference === selected ? undefined : autoLabel} disabled={busy} onValueChange={(value) => setShell(value)}>
          <option value="auto">{autoLabel}</option>
          {windows ? (
            <>
              <option value="bash">{t("settings.effectiveShellGitBash")}</option>
              <option value="pwsh">{t("settings.shellPwsh")}</option>
              <option value="powershell">{t("settings.shellPowershell")}</option>
            </>
          ) : <option value="bash">{t("settings.shellBash")}</option>}
        </SettingsSelect>
        </div>
      {sb.shellReloadRequired && field(t("settings.resolvedShell"),
        <div className="settings-readonly-field">
          {effectiveShellLabel(resolvedShell, t)}
          {sb.shellReloadRequired && <button type="button" className="btn btn--small set-shell-reload" disabled={busy} onClick={reloadSession}>
            <RefreshCw size={13} aria-hidden="true" />
            <span>{t("settings.shellReloadNow")}</span>
          </button>}
        </div>)}
      </div>
      <div className="sandbox-group sandbox-group--environment">
        <h3>{t("settings.shellDetection")}</h3>
        <p className="sandbox-group__hint">{t("settings.shellDetectionHint")}</p>
        <div className="shell-support">
          <div className="shell-support__detection">
          <table className="shell-capability-table" aria-label={t("settings.shellDetection")}>
            <thead><tr><th scope="col">{t("settings.environmentName")}</th><th scope="col">{t("settings.environmentStatus")}</th><th scope="col">{t("settings.environmentPath")}</th></tr></thead>
            <tbody>
            {capabilities.map((capability) => <DetectionRow key={capability.id} cap={capability} t={t} />)}
            {git && <DetectionRow cap={git} t={t} />}
            </tbody>
          </table>
          </div>
          {gitBashMissing && (
            <div className="shell-support__card">
              <div className="shell-support__hint">{t("settings.shellInstallManualNotice")}</div>
              <div className="shell-support__actions">
                <button type="button" className="btn btn--small" onClick={() => void openExternal("https://git-scm.com/download/win")}>
                  <ExternalLink size={14} aria-hidden="true" />
                  <span>{t("settings.shellInstallManualLink")}</span>
                </button>
                <button type="button" className="btn btn--small" disabled={busy} onClick={reloadSession}>
                  <RefreshCw size={13} aria-hidden="true" />
                  <span>{t("settings.shellRepairReload")}</span>
                </button>
              </div>
            </div>
          )}
          {!windows && bashMissing && !nativeFallback && (
            <RepairCard message={t("settings.shellBashManualRepair")} guidance={sb.shellRepairGuidance ?? null} busy={busy} reloadSession={reloadSession} />
          )}
          {git && !git.available && !windows && (
            <RepairCard message={t("settings.gitManualRepair")} guidance={sb.gitRepairGuidance ?? null} busy={busy} reloadSession={reloadSession} />
          )}
        </div>
        <p className="sandbox-boundary"><Info size={16} aria-hidden="true" /><span>{t(windows ? "settings.sandboxBoundaryHintWindows" : "settings.sandboxBoundaryHint")}</span></p>
      </div>
    </>
  );
}
