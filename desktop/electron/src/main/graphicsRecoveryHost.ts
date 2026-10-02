import { app, dialog, shell } from "electron";
import { join } from "node:path";
import { GRAPHICS_RECOVERY_ARG, type GraphicsSettingsStore } from "./graphics.js";
import { GraphicsFaultRecord, GraphicsRecovery } from "./graphicsRecovery.js";
import type { QuitSequencer } from "./lifecycle.js";
import { errorText, type Logger } from "./log.js";

export async function confirmRecoveryDraftLoss(): Promise<boolean> {
  const result = await dialog.showMessageBox({
    type: "warning", title: "Draft not saved / 草稿未保存",
    message: "The unresponsive interface could not save your draft. Restart anyway? / 界面未能保存当前草稿，仍要重启吗？",
    detail: "Unsaved draft changes may be lost. Saved conversations will be kept. / 未保存的草稿修改可能丢失，已保存的会话会保留。",
    buttons: ["Restart anyway / 仍然重启", "Cancel / 取消"], defaultId: 1, cancelId: 1, noLink: true,
  });
  return result.response === 0;
}

export function installGraphicsRecovery(options: {
  graphics: GraphicsSettingsStore; lifecycle: QuitSequencer; log: Logger;
  logsDir: string; build: string; temporary: boolean;
}) {
  const { graphics, lifecycle, log, logsDir } = options;
  const record = new GraphicsFaultRecord(join(app.getPath("userData"), "graphics-fault.json"), options.build);
  const previousFailure = record.pending();
  const stopping = () => lifecycle.currentPhase !== "idle";
  const error = (value: unknown) => log.warn(`graphics recovery: ${errorText(value)}`);
  let actualAcceleration = graphics.current.startupEnabled;
  let keepOffered = false;
  const recovery = new GraphicsRecovery({
    stopping, accelerationEnabled: () => actualAcceleration,
    record: fault => {
      log.error(`graphics fault: ${JSON.stringify({ ...fault, requested: graphics.current.startupEnabled, actual: actualAcceleration })}`);
      try { log.info(`graphics fault processes: ${JSON.stringify(app.getAppMetrics())}`); } catch (e) { error(e); }
      record.record(fault);
    },
    acknowledge: () => record.acknowledge(), error,
    offer: async gpu => {
      await app.whenReady();
      if (stopping()) return;
      let response: number;
      do {
        const shownFault = record.checkpoint();
        const result = await dialog.showMessageBox({
          type: "warning", title: "Reasonix recovery / Reasonix 恢复",
          message: gpu
            ? "A graphics process failed. Try restarting without hardware acceleration. / 图形进程发生异常，可尝试关闭硬件加速后重启。"
            : "The interface failed or stopped responding. / 界面发生异常或持续无响应。",
          detail: "Restarting interrupts running tasks. Reasonix will try to save your draft first. / 重启会中断正在执行的任务，Reasonix 会先尝试保存草稿。",
          buttons: [gpu ? "Restart in compatibility mode / 以兼容模式重启" : "Restart / 重启", "Open diagnostics / 查看诊断", "Not now / 暂不重启"],
          defaultId: 2, cancelId: 2, noLink: true,
        });
        response = result.response;
        if (response !== 1) {
          try { record.acknowledge(shownFault); } catch (e) { error(e); }
        }
        if (response === 1 && !stopping()) await shell.openPath(logsDir);
      } while (response === 1 && !stopping());
      if (stopping()) return;
      if (response !== 0) return;
      const args = process.argv.slice(1).filter(arg => arg !== GRAPHICS_RECOVERY_ARG);
      if (gpu || options.temporary) args.push(GRAPHICS_RECOVERY_ARG);
      lifecycle.recoverRenderer(args);
    },
  });
  app.on("child-process-gone", (_event, details) => {
    if (details.type === "GPU") recovery.fault({ role: "GPU", reason: details.reason, exitCode: details.exitCode });
  });
  app.on("gpu-info-update", () => {
    try {
      actualAcceleration = app.isHardwareAccelerationEnabled();
      log.info(`graphics actual acceleration: ${actualAcceleration}`);
      void app.getGPUInfo("basic").then(info => log.info(`graphics device: ${JSON.stringify(info)}`)).catch(error);
    } catch (e) { error(e); }
  });
  const timer = setInterval(() => recovery.tick(), 1000);
  timer.unref();
  app.once("will-quit", () => clearInterval(timer));
  void app.whenReady().then(() => {
    if (previousFailure && !options.temporary) recovery.offer(true);
  }).catch(error);
  return {
    recovery,
    healthy: () => {
      recovery.healthy();
      if (!options.temporary || keepOffered || stopping() || recovery.isPrompting) return;
      keepOffered = recovery.notice(async () => {
        const shownFault = record.checkpoint();
        const result = await dialog.showMessageBox({
          type: "info", title: "Compatibility mode / 兼容模式",
          message: "Reasonix started with hardware acceleration disabled. / Reasonix 已关闭硬件加速并成功启动。",
          detail: "You can keep it disabled for future launches and change it later in Settings. This does not establish the cause of the failure. / 可将关闭状态用于后续启动，之后可在设置中修改。成功启动不代表已经确认故障原因。",
          buttons: ["Keep disabled / 保持关闭", "This launch only / 仅本次关闭"], defaultId: 1, cancelId: 1, noLink: true,
        });
        try { record.acknowledge(shownFault); } catch (e) { error(e); }
        if (result.response === 0 && !stopping()) {
          try { await graphics.setHardwareAcceleration(false); }
          catch (e) {
            error(e);
            await dialog.showMessageBox({
              type: "error", title: "Setting not saved / 设置未保存",
              message: "The preference could not be saved. Compatibility mode still applies to this launch. / 无法保存设置，本次启动仍保持兼容模式。",
              buttons: ["OK / 确定"], noLink: true,
            });
          }
        }
      });
    },
  };
}
