import { app, dialog } from "electron";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { consumeGraphicsRecoveryArg, GraphicsSettingsStore, loadGraphicsBootstrap } from "../../src/main/graphics.js";
import { confirmRecoveryDraftLoss, installGraphicsRecovery } from "../../src/main/graphicsRecoveryHost.js";
import { QuitSequencer } from "../../src/main/lifecycle.js";
import { MainWindow, DEFAULT_GEOMETRY } from "../../src/main/window.js";
import { AppZoomStore } from "../../src/main/zoomStore.js";

const profile = process.env.REASONIX_RECOVERY_TEST_HOME!;
mkdirSync(profile, { recursive: true });
app.setPath("userData", profile);
const boot = loadGraphicsBootstrap(profile, process.env, process.argv);
if (boot.shouldDisable) app.disableHardwareAcceleration();
const temporary = consumeGraphicsRecoveryArg(process.argv);
const graphics = new GraphicsSettingsStore(boot.configPath, boot);
const events: string[] = [], prompts: Electron.MessageBoxOptions[] = [], responses: number[] = [];
let relaunchArgs: string[] = [], flushHangs = false, reloads = 0;
let duringDialog: (() => void) | undefined;
if (process.env.REASONIX_RECOVERY_NATIVE_DIALOG !== "1") {
  dialog.showMessageBox = (async (...args: unknown[]) => {
    const options = args[args.length - 1] as Electron.MessageBoxOptions;
    prompts.push(options);
    duringDialog?.();
    return { response: responses.shift() ?? options.cancelId ?? 0, checkboxChecked: false };
  }) as typeof dialog.showMessageBox;
}
const log = { info: (s: string) => events.push(s), warn: (s: string) => events.push(s), error: (s: string) => events.push(s) };
let main: MainWindow;
const lifecycle = new QuitSequencer({
  app: { quit: () => { if (!lifecycle.onBeforeQuit()) return; events.push("quit-complete"); }, relaunch: args => { relaunchArgs = args; } },
  service: { beforeClose: async () => false, shutdown: async () => { events.push("service-shutdown"); } },
  flushRenderer: () => flushHangs ? new Promise(() => {}) : main.flushSessionDraft(),
  recoveryDraftTimeoutMs: 20, confirmRecoveryDraftLoss,
  onCloseAllowed: () => { events.push("close-allowed"); }, log,
});
const host = installGraphicsRecovery({ graphics, lifecycle, log, logsDir: profile, build: "test", temporary });
main = new MainWindow({
  preloadPath: join(profile, "unused-preload.cjs"),
  appURL: "data:text/html,<title>Reasonix recovery test</title><h1>Recovery test</h1>",
  platform: process.platform, log, zoomStore: new AppZoomStore(join(profile, "zoom.json")),
  isQuitting: () => lifecycle.isQuitting,
  onRendererFailure: (details, canReload) => host.recovery.fault({ role: "renderer", ...details }, canReload),
  onUnresponsive: () => host.recovery.unresponsive(), onResponsive: () => host.recovery.responsive(),
  onAppDomReady: () => { reloads++; }, onCloseRequested: async () => {}, onShellAction: () => {},
});
app.on("before-quit", () => main.allowClose());
const snapshot = () => ({ reloads, relaunchArgs, phase: lifecycle.currentPhase, temporary, argv: process.argv, startup: graphics.current.startupEnabled, actual: app.isHardwareAccelerationEnabled(), events, prompts });
async function until(check: () => boolean, timeout = 10_000): Promise<void> {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (check()) return; await new Promise(resolve => setTimeout(resolve, 25)); }
  throw new Error("Recovery fixture condition timed out");
}
void app.whenReady().then(async () => {
  main.create(DEFAULT_GEOMETRY); await main.loadApp(); main.show("test");
  const mode = process.env.REASONIX_RECOVERY_TEST_MODE;
  if (mode === "gpu" || mode === "native") {
    flushHangs = true; responses.push(0, 0);
    app.emit("child-process-gone", {}, { type: "GPU", reason: "crashed", exitCode: 1 });
    app.emit("child-process-gone", {}, { type: "GPU", reason: "crashed", exitCode: 1 });
    if (mode === "native") console.log("Native recovery dialog ready; choose restart and confirm disposable draft loss.");
    await until(() => lifecycle.currentPhase === "completed", mode === "native" ? 180_000 : 10_000);
  } else if (mode === "renderer") {
    main.browserWindow!.webContents.forcefullyCrashRenderer();
    await until(() => reloads === 2);
    main.browserWindow!.webContents.forcefullyCrashRenderer();
    await until(() => prompts.length === 1);
  } else if (mode === "software" || mode === "keep") {
    responses.push(mode === "keep" ? 0 : 1); host.healthy();
    await until(() => prompts.length === 1 && !host.recovery.isPrompting);
  } else if (mode === "record") {
    app.emit("child-process-gone", {}, { type: "GPU", reason: "crashed", exitCode: 1 });
  } else if (mode === "previous") {
    await until(() => prompts.length === 1 && !host.recovery.isPrompting);
  } else if (mode === "during-dialog" || mode === "during-keep") {
    const fail = () => app.emit("child-process-gone", {}, { type: "GPU", reason: "crashed", exitCode: 1 });
    duringDialog = () => { duringDialog = undefined; fail(); };
    if (mode === "during-keep") { responses.push(1); host.healthy(); }
    else { fail(); fail(); }
    await until(() => prompts.length === 1 && !host.recovery.isPrompting);
  }
  writeFileSync(join(profile, "result.json"), JSON.stringify(snapshot()));
  app.exit(0);
}).catch(error => { console.error(error, snapshot()); app.exit(1); });
