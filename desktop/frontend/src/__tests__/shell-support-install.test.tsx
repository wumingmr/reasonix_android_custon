// Run: tsx src/__tests__/shell-support-install.test.tsx
//
// Sandbox settings shell support contract: Windows exposes Git Bash and native
// PowerShell runtimes, while macOS/Linux expose Bash with copy-only native repair
// guidance. The 1.38.10 layout keeps diagnostics and write roots inline.

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { SettingsPanel } from "../components/SettingsPanel";
import { LocaleProvider } from "../lib/i18n";
import type { AppBindings } from "../lib/bridge";
import type { SettingsView } from "../lib/types";
import { baseSettings, flushPromises, installCanvasMock, waitFor } from "../test-support/settingsTestFixtures";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq(actual: unknown, expected: unknown, label: string) {
  const same = actual === expected ||
    (Array.isArray(actual) && Array.isArray(expected) && JSON.stringify(actual) === JSON.stringify(expected));
  if (same) {
    ok(true, label);
  } else {
    ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

async function shellOptionValues(rootEl: HTMLElement): Promise<string[]> {
  const trigger = rootEl.querySelector<HTMLButtonElement>('[aria-haspopup="listbox"]');
  await act(async () => {
    trigger?.click();
    await flushPromises();
  });
  const values = Array.from(document.querySelectorAll<HTMLElement>('[role="option"][data-value]'))
    .map((option) => option.dataset.value ?? "");
  await act(async () => {
    trigger?.click();
    await flushPromises();
  });
  return values;
}

console.log("\nshell support guidance");

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
Object.defineProperty(dom.window.HTMLElement.prototype, "attachEvent", { configurable: true, value: () => {} });
Object.defineProperty(dom.window.HTMLElement.prototype, "detachEvent", { configurable: true, value: () => {} });
installCanvasMock(dom.window as unknown as Window);
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
const copiedCommands: string[] = [];
const openedURLs: string[] = [];
Object.defineProperty(dom.window.navigator, "clipboard", {
  configurable: true,
  value: { writeText: async (value: string) => { copiedCommands.push(value); } },
});
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.CustomEvent = dom.window.CustomEvent;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.sessionStorage = dom.window.sessionStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
window.scrollTo = () => {};
window.matchMedia = (() => ({
  matches: false,
  media: "",
  onchange: null,
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => false,
})) as typeof window.matchMedia;
window.open = ((url?: string | URL) => {
  openedURLs.push(String(url));
  return null;
}) as typeof window.open;
localStorage.clear();

function windowsSettings(overrides: {
  shell?: string;
  reloadRequired?: boolean;
  manualUrl?: string;
  gitBashAvailable?: boolean;
}): SettingsView {
  const settings = baseSettings("standard");
  settings.sandbox = {
    ...settings.sandbox,
    shell: overrides.shell ?? "auto",
    effectiveShell: "powershell",
    resolvedShell: overrides.shell === "bash" ? "git-bash" : overrides.reloadRequired ? "pwsh" : "powershell",
    shellReloadRequired: overrides.reloadRequired ?? false,
    shellCapabilities: [
      { id: "git-bash", variant: "git-for-windows", available: overrides.gitBashAvailable ?? true, path: overrides.gitBashAvailable === false ? undefined : "C:\\Program Files\\Git\\bin\\bash.exe", source: "standard-path" },
      { id: "powershell", available: true, path: "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", source: "standard-path" },
      { id: "pwsh", available: true, path: "C:\\Program Files\\PowerShell\\7\\pwsh.exe", source: "standard-path" },
    ],
    gitCapability: { id: "git", available: true, path: "C:\\Program Files\\Git\\cmd\\git.exe", source: "standard-path" },
    shellInstallAction: { id: "git-for-windows", mode: "manual", available: false, manualUrl: overrides.manualUrl ?? "https://git-scm.com/download/win" },
  };
  return settings;
}

// Scenario 1: Windows detects Git Bash and can explicitly select its existing
// persisted "bash" preference without changing the PowerShell auto default.
{
  const rootEl = document.createElement("div");
  document.body.appendChild(rootEl);
  const root = createRoot(rootEl);
  let installCalls = 0;
  let cancelCalls = 0;
  let reloadCalls = 0;
  let settingsCalls = 0;
  const shellPreferenceCalls: string[] = [];
  let selectedShell = "auto";
  const desktopStub = installDesktopHostStub(({
    main: {
      App: {
        Settings: async () => {
          settingsCalls += 1;
          return windowsSettings({ shell: selectedShell, reloadRequired: true });
        },
        SetShellPreference: async (value: string) => { shellPreferenceCalls.push(value); selectedShell = value; },
        InstallShellSupport: async () => {
          installCalls += 1;
          return { status: "manual_required", manualUrl: "https://git-scm.com/download/win" };
        },
        CancelShellInstall: async () => { cancelCalls += 1; },
        ReloadSettings: async () => { reloadCalls += 1; },
      } as Partial<AppBindings> as AppBindings,
    },
  }).main.App, { externalOpens: openedURLs });
  await act(async () => {
    root.render(
      <LocaleProvider>
        <SettingsPanel initialTab="sandbox" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} />
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await waitFor("Windows Shell interpreter", () => rootEl.textContent?.includes("Shell interpreter") === true);
  const optionValues = await shellOptionValues(rootEl);
  eq(optionValues, ["auto", "bash", "pwsh", "powershell"], "Windows selector restores Git Bash alongside PowerShell");
  const shellTrigger = rootEl.querySelector<HTMLButtonElement>('[aria-haspopup="listbox"]');
  await act(async () => {
    shellTrigger?.click();
    await flushPromises();
    document.querySelector<HTMLElement>('[role="option"][data-value="bash"]')?.click();
    await flushPromises();
  });
  eq(shellPreferenceCalls, ["bash"], "Git Bash selection persists the backward-compatible bash preference");
  eq(rootEl.querySelector('[aria-haspopup="listbox"]')?.textContent, "Git Bash", "saved Bash preference remains selected after settings refresh");
  ok(rootEl.textContent?.includes("C:\\Program Files\\Git\\bin\\bash.exe") === true, "Windows displays the detected Git Bash executable");
  ok(rootEl.textContent?.includes("Git for Windows") !== true, "installed Git Bash needs no repair card");
  ok(rootEl.textContent?.includes("C:\\Windows\\System32\\WindowsPowerShell") === true,
    "Windows shell detection includes its resolved executable path");
  ok(rootEl.querySelector(".runtime-details") === null, "diagnostics remain inline in the restored layout");
  const sandboxSection = rootEl.querySelector(".settings-page--sandbox .settings-section");
  const headerApplyButton = sandboxSection?.querySelector<HTMLButtonElement>(".settings-section__actions button");
  eq(headerApplyButton?.textContent, "Reload configuration", "manual configuration action stays in the section header");
  ok(sandboxSection!.textContent!.indexOf("Runtime environment") < sandboxSection!.textContent!.indexOf("Workspace root")
    && sandboxSection!.textContent!.indexOf("Currently allowed directories") > sandboxSection!.textContent!.indexOf("Workspace root"),
    "runtime diagnostics precede the grouped file write settings");
  ok(!sandboxSection!.querySelector('input[type="checkbox"]'), "Windows network access is a status, not a disabled toggle");
  ok(sandboxSection!.textContent!.includes("shell commands are not restricted"), "Windows file-tool scope is explicit");
  await act(async () => { rootEl.querySelector<HTMLButtonElement>('[aria-label="Copy Git Bash path"]')!.click(); await flushPromises(); });
  eq(copiedCommands.at(-1), "C:\\Program Files\\Git\\bin\\bash.exe", "detected executable paths can be copied in full");
  eq(openedURLs.length, 0, "rendering Windows settings opens no external installer page");
  eq(installCalls, 0, "rendering Windows repair never calls InstallShellSupport");
  eq(cancelCalls, 0, "manual-only Windows repair never calls CancelShellInstall");

  const repairReloadButton = Array.from(rootEl.querySelectorAll("button")).find((button) => button.textContent?.includes("Reload current session"));
  ok(Boolean(repairReloadButton), "Windows shows reload only when the resolved runtime changed");
  await act(async () => {
    repairReloadButton!.click();
    await flushPromises();
  });
  eq(reloadCalls, 1, "Windows reloads only after the user requests it");
  eq(settingsCalls, 3, "preference selection and reload each refresh the Settings snapshot once");
  await act(async () => {
    headerApplyButton!.click();
    await flushPromises();
  });
  eq(reloadCalls, 2, "restored header action applies manual configuration changes");
  eq(installCalls, 0, "reload never calls the legacy install binding");
  await act(async () => { root.unmount(); });
}

// Missing Git Bash has a manual, fixed official download target, including
// when an old backend supplies an untrusted URL in its compatibility fields.
{
  const rootEl = document.createElement("div");
  document.body.appendChild(rootEl);
  const root = createRoot(rootEl);
  let reloadCalls = 0;
  let installCalls = 0;
  installDesktopHostStub({
    Settings: async () => windowsSettings({ gitBashAvailable: false, manualUrl: "https://evil.example/installer" }),
    ReloadSettings: async () => { reloadCalls += 1; },
    InstallShellSupport: async () => { installCalls += 1; return { status: "manual_required" }; },
  } as Partial<AppBindings> as AppBindings, { externalOpens: openedURLs });
  await act(async () => {
    root.render(<LocaleProvider><SettingsPanel initialTab="sandbox" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} /></LocaleProvider>);
    await flushPromises();
  });
  await waitFor("manual Git Bash repair", () => rootEl.textContent?.includes("Git Bash was not detected") === true);
  const download = Array.from(rootEl.querySelectorAll("button")).find(button => button.textContent?.includes("Download from git-scm.com"));
  const reload = Array.from(rootEl.querySelectorAll("button")).find(button => button.textContent?.includes("Re-detect and reload session"));
  ok(Boolean(download && reload), "missing Git Bash offers manual download and re-detection");
  await act(async () => {
    download!.click();
    reload!.click();
    await flushPromises();
  });
  eq(openedURLs.at(-1), "https://git-scm.com/download/win", "manual repair only opens the fixed official download page");
  eq(reloadCalls, 1, "manual repair re-detects and reloads once on request");
  eq(installCalls, 0, "manual repair never launches an installer");
  await act(async () => { root.unmount(); });
}

// Scenario 2: Linux reports bash/zsh/sh, offers an allowlisted distro command
// for copying, and only re-detects after the user explicitly requests it.
{
  const rootEl = document.createElement("div");
  document.body.appendChild(rootEl);
  const root = createRoot(rootEl);
  const linuxSettings = baseSettings("standard");
  linuxSettings.sandbox = {
    ...linuxSettings.sandbox,
    shellCapabilities: [
      { id: "bash", variant: "system", available: false, reason: "not-found" },
      { id: "zsh", variant: "system", available: false, reason: "not-found" },
      { id: "sh", variant: "system", available: true, path: "/bin/sh", source: "standard-path" },
    ],
    gitCapability: { id: "git", available: true, path: "/usr/bin/git", source: "path" },
    shellInstallAction: null,
    shellRepairGuidance: { manager: "apt", command: "apt-get install bash" },
  };
  let reloadCalls = 0;
  const desktopStub = installDesktopHostStub(({
    main: {
      App: {
        Settings: async () => linuxSettings,
        SetShellPreference: async () => {},
        InstallShellSupport: async () => ({ status: "unsupported_platform" }),
        CancelShellInstall: async () => {},
        ReloadSettings: async () => { reloadCalls += 1; },
      } as Partial<AppBindings> as AppBindings,
    },
  }).main.App, { externalOpens: openedURLs });
  await act(async () => {
    root.render(
      <LocaleProvider>
        <SettingsPanel initialTab="sandbox" desktopPlatform="linux" onClose={() => {}} onChanged={() => {}} />
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await waitFor("Linux detection", () => rootEl.textContent?.includes("Bash") === true);
  eq(await shellOptionValues(rootEl), ["auto", "bash"],
    "Linux selector contains no PowerShell runtimes");
  ok(!Array.from(rootEl.querySelectorAll("button")).some((button) => button.textContent?.includes("Install Git for Windows")),
    "Linux never renders a Windows install entry");
  ok(rootEl.textContent?.includes("zsh") === true && rootEl.textContent?.includes("POSIX sh") === true,
    "Linux detection reports zsh and POSIX sh alongside Bash");
  ok(rootEl.textContent?.includes("apt-get install bash") === true, "Linux missing Bash shows the distro repair command");
  ok(!rootEl.textContent?.includes("sudo apt-get") && !rootEl.textContent?.includes("sudo"),
    "Linux repair guidance never prescribes sudo");
  const copyButton = Array.from(rootEl.querySelectorAll("button")).find((button) => button.textContent?.includes("Copy command"));
  ok(Boolean(copyButton), "Linux repair command is copyable");
  await act(async () => {
    copyButton!.click();
    await flushPromises();
  });
  eq(copiedCommands.at(-1), "apt-get install bash", "copy action writes the exact allowlisted command");
  const repairReloadButton = Array.from(rootEl.querySelectorAll("button")).find((button) => button.textContent?.includes("Re-detect and reload session"));
  ok(Boolean(repairReloadButton), "Linux manual repair offers explicit re-detection");
  await act(async () => {
    repairReloadButton!.click();
    await flushPromises();
  });
  eq(reloadCalls, 1, "Linux repair reload remains an explicit user action");
  await act(async () => { root.unmount(); });
}

// Scenario 3: macOS falls back to zsh when Bash is missing, while Git remains
// a separate capability with its own copy-only Homebrew repair command.
{
  const rootEl = document.createElement("div");
  document.body.appendChild(rootEl);
  const root = createRoot(rootEl);
  const macSettings = baseSettings("standard");
  macSettings.sandbox = {
    ...macSettings.sandbox,
    effectiveShell: "zsh",
    resolvedShell: "zsh",
    shellCapabilities: [
      { id: "bash", variant: "system", available: false, reason: "not-found" },
      { id: "zsh", variant: "system", available: true, path: "/bin/zsh", source: "standard-path" },
      { id: "sh", variant: "system", available: true, path: "/bin/sh", source: "standard-path" },
    ],
    gitCapability: { id: "git", available: false, reason: "not-found" },
    shellInstallAction: null,
    shellRepairGuidance: null,
    gitRepairGuidance: { manager: "homebrew", command: "brew install git" },
  };
  const desktopStub = installDesktopHostStub(({
    main: {
      App: {
        Settings: async () => macSettings,
        SetShellPreference: async () => {},
        InstallShellSupport: async () => ({ status: "unsupported_platform" }),
        CancelShellInstall: async () => {},
        ReloadSettings: async () => {},
      } as Partial<AppBindings> as AppBindings,
    },
  }).main.App, { externalOpens: openedURLs });
  await act(async () => {
    root.render(
      <LocaleProvider>
        <SettingsPanel initialTab="sandbox" desktopPlatform="darwin" onClose={() => {}} onChanged={() => {}} />
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await waitFor("macOS shell inventory", () => rootEl.textContent?.includes("POSIX sh") === true);
  eq(await shellOptionValues(rootEl), ["auto", "bash"],
    "macOS selector contains no PowerShell runtimes");
  ok(rootEl.textContent?.includes("zsh") === true && rootEl.textContent?.includes("POSIX sh") === true,
    "macOS detection reports native zsh and POSIX sh");
  ok(!rootEl.textContent?.includes("brew install bash") && !rootEl.textContent?.includes("Bash is not detected"),
    "macOS native zsh fallback does not request a Bash install");
  ok(rootEl.textContent?.includes("Git") === true && rootEl.textContent?.includes("brew install git") === true,
    "macOS missing Git shows an independent Homebrew Git repair command");
  ok(rootEl.textContent?.includes("Shell after reload") === false,
    "identical current and resolved shells do not produce duplicate status rows");
  const gitCopyButton = Array.from(rootEl.querySelectorAll("button")).find((button) => button.textContent?.includes("Copy command"));
  await act(async () => {
    gitCopyButton!.click();
    await flushPromises();
  });
  eq(copiedCommands.at(-1), "brew install git", "macOS Git repair copies brew install git only");
  ok(!Array.from(rootEl.querySelectorAll("button")).some((button) => button.textContent?.includes("Install Git for Windows")),
    "macOS never renders the Windows install entry");
  await act(async () => { root.unmount(); });
}

// Drafts survive failed saves, a pending write cannot be submitted twice, and
// a newly loaded authoritative root replaces the previous displayed value.
{
  const rootEl = document.createElement("div");
  document.body.appendChild(rootEl);
  const root = createRoot(rootEl);
  const settings = windowsSettings({});
  let writes = 0;
  let failSave = true;
  let release: (() => void) | undefined;
  installDesktopHostStub({
    Settings: async () => structuredClone(settings),
    ReloadSettings: async () => { settings.sandbox.workspaceRoot = "C:\\Reloaded"; },
    SetSandbox: async (_bash: string, _network: boolean, workspaceRoot: string, allowWrite: string[]) => {
      writes++;
      if (failSave) { await new Promise<void>(resolve => { release = resolve; }); throw new Error("save rejected"); }
      settings.sandbox.workspaceRoot = workspaceRoot.trim();
      settings.sandbox.allowWrite = allowWrite;
    },
  } as Partial<AppBindings> as AppBindings);
  await act(async () => { root.render(<LocaleProvider><SettingsPanel initialTab="sandbox" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} /></LocaleProvider>); await flushPromises(); });
  const input = () => rootEl.querySelector<HTMLInputElement>('[aria-label="Additional writable directories"]')!;
  const rootInput = () => rootEl.querySelector<HTMLInputElement>('[aria-label="Workspace root"]')!;
  const change = async (element: HTMLInputElement, value: string) => {
    await act(async () => {
      element.focus();
      const previous = element.value;
      element.value = value;
      (element as HTMLInputElement & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
      element.dispatchEvent(new Event("input", { bubbles: true }));
      element.dispatchEvent(new Event("change", { bubbles: true }));
      element.dispatchEvent(new KeyboardEvent("keyup", { key: "a", bubbles: true }));
      await flushPromises();
    });
  };
  const enter = (element: HTMLInputElement) => element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  await change(input(), "C:\\Extra");
  await act(async () => { enter(input()); enter(input()); await flushPromises(); });
  eq(writes, 1, "rapid Enter submits a rule only once");
  ok(input().disabled, "rule draft is disabled while saving");
  await act(async () => { release!(); await flushPromises(); });
  eq(input().value, "C:\\Extra", "failed rule save preserves the entered directory");
  failSave = false;
  await act(async () => { enter(input()); await flushPromises(); });
  eq(input().value, "", "successful retry clears the directory draft");
  eq(settings.sandbox.allowWrite, ["C:\\Extra"], "successful retry stores exactly one directory");
  const reload = Array.from(rootEl.querySelectorAll("button")).find(b => b.textContent === "Reload configuration")!;
  await act(async () => { reload.click(); await flushPromises(); });
  eq(rootInput().value, "C:\\Reloaded", "reloaded configuration updates the root field without remounting");
  failSave = true;
  await change(rootInput(), "C:\\Edited");
  await act(async () => { enter(rootInput()); await flushPromises(); });
  eq(writes, 3, "root Enter starts a distinct save of the edited value");
  await act(async () => { release!(); await flushPromises(); });
  eq(rootInput().value, "C:\\Edited", "failed root save retains the editable draft");
  await waitFor("failed root save settles", () => !rootInput().disabled);
  eq(rootEl.querySelector(".sandbox-save-status")?.textContent, "Save failed. Please retry.", "failed saves never show saved status");
  failSave = false;
  await act(async () => { enter(rootInput()); await flushPromises(); });
  eq(settings.sandbox.workspaceRoot, "C:\\Edited", "root draft can be retried successfully");
  await change(rootInput(), "");
  await act(async () => { enter(rootInput()); await flushPromises(); });
  eq(settings.sandbox.workspaceRoot, "", "an empty root restores the current-workspace default");
  eq(rootInput().value, "", "saved empty root stays empty after refresh");
  await act(async () => { root.unmount(); });

  // The same RuleList owns permission rules; its failure behavior must agree.
  const permissionsRoot = createRoot(rootEl);
  let permissionWrites = 0;
  failSave = true;
  installDesktopHostStub({
    Settings: async () => structuredClone(settings),
    AddPermissionRule: async (list: "allow" | "ask" | "deny", rule: string) => {
      permissionWrites++;
      if (failSave) throw new Error("permission save rejected");
      settings.permissions[list].push(rule);
    },
  } as Partial<AppBindings> as AppBindings);
  await act(async () => { permissionsRoot.render(<LocaleProvider><SettingsPanel initialTab="permissions" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} /></LocaleProvider>); await flushPromises(); });
  const permissionInput = rootEl.querySelector<HTMLInputElement>('.set-rules input')!;
  await change(permissionInput, "Bash(rm:*)");
  await act(async () => { enter(permissionInput); enter(permissionInput); await flushPromises(); });
  eq(permissionWrites, 1, "permission rules also reject duplicate concurrent Enter");
  eq(permissionInput.value, "Bash(rm:*)", "failed permission rules preserve their draft too");
  failSave = false;
  await act(async () => { enter(permissionInput); await flushPromises(); });
  eq(permissionInput.value, "", "successful permission retry clears the shared rule draft");
  await act(async () => { permissionsRoot.unmount(); });
}

if (failed > 0) {
  console.error(`\n${failed} failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\n${passed} passed, 0 failed`);
