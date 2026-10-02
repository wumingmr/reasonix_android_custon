# Desktop graphics recovery / 桌面图形故障恢复

Hardware acceleration remains enabled by default. To change the saved preference,
open **Settings → General → System → Hardware acceleration** and restart Reasonix.
Disabling acceleration can help with graphics compatibility, but a successful
restart alone does not establish the cause of a freeze.

默认开启硬件加速。可在 **设置 → 通用 → 系统 → 硬件加速** 修改偏好，重启后生效。
关闭加速可能改善图形兼容性，但重启成功本身不能确认卡死原因。

Repeated GPU failures or renderer failures can offer a native recovery dialog.
A renderer is automatically reloaded at most once within 60 seconds; a window
that remains unresponsive for 15 seconds can also offer recovery. Memory growth
or an unclean exit alone is not classified as a GPU failure.

连续 GPU 故障或页面故障会提供原生恢复对话框。页面在 60 秒内最多自动刷新一次；
界面持续无响应 15 秒也可提供恢复入口。仅凭内存增长或异常退出不会判定为 GPU 故障。

**Restart in compatibility mode** disables acceleration for that launch only.
After startup, choose **Keep disabled** to save the preference, or **This launch
only** to use the existing preference next time. Restart interrupts running tasks.
Reasonix attempts to save drafts before shutting down; if saving fails or takes
more than five seconds during recovery, proceeding requires confirmation that
unsaved draft changes may be lost.

**以兼容模式重启** 仅对本次启动关闭加速。启动后选择 **保持关闭** 可保存偏好，
选择 **仅本次关闭** 则在下次启动时使用原有偏好。重启会中断正在执行的任务。
Reasonix 会先尝试保存草稿；恢复过程中保存失败或超过五秒时，必须确认可能丢失
未保存草稿修改后才能继续重启。

**Open diagnostics** opens the local logs directory. Recent unacknowledged GPU
failures may offer recovery on the next launch of the same build, within 24 hours.
Graphics records remain local and do not upload diagnostics. If no recovery
dialog is available, fully quit Reasonix and launch with `REASONIX_DISABLE_GPU=1`
for a temporary override that does not change the saved preference.

**查看诊断** 打开本地日志目录。同版本在 24 小时内再次启动时，尚未确认的 GPU
故障可再次提供恢复提示。图形故障记录仅保存在本地，不会上传诊断数据。
若无法使用恢复弹窗，可完全退出 Reasonix 后带 `REASONIX_DISABLE_GPU=1` 启动，
临时关闭加速，不改变已保存的偏好。

## Linux display backend / Linux 显示后端

Under a Wayland session that also provides XWayland (`DISPLAY` is set), Reasonix
starts its window with `--ozone-platform=x11`, because some drivers fail to
initialise EGL on Electron's Wayland backend and the app exits before a window
appears. A pure Wayland session without `DISPLAY` keeps the native backend. Set
`REASONIX_OZONE_PLATFORM=wayland` to use native Wayland anyway, `x11` to force
X11, or `auto` for the default. An `--ozone-platform` argument given on the
command line always wins.

在同时提供 XWayland（设置了 `DISPLAY`）的 Wayland 会话下，Reasonix 默认以
`--ozone-platform=x11` 启动窗口：部分驱动在 Electron 的 Wayland 后端上无法初始化
EGL，应用会在窗口出现前退出。没有 `DISPLAY` 的纯 Wayland 会话保持原生后端。
设置 `REASONIX_OZONE_PLATFORM=wayland` 可仍使用原生 Wayland，`x11` 强制 X11，
`auto` 为默认行为。命令行显式传入的 `--ozone-platform` 始终优先。

See the [shell implementation and validation notes](../desktop/electron/README.md#hardware-acceleration-recovery).
实现及验证说明见上述链接。
