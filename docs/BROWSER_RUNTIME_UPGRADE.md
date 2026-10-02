# Browser runtime upgrade / 浏览器运行时升级

## Contract / 契约

Go owns task/session grants, cancellation, write outcomes and artifact delivery.
Electron owns pages, document epochs, viewport revisions and rendering leases.
The independently built page runtime owns DOM observations. React owns presentation
intent and unsent drafts; it never infers readiness from visibility.

Go 管理任务授权、取消、写入结果及产物；Electron 管理页面、文档代际、视口与渲染租约；
独立构建的页面脚本管理 DOM 观察；React 只管理展示意图与草稿。

Async results revalidate grant generation, task/session, tab/page identity, document
epoch and viewport revision. Takeover, cancellation and shutdown release only
their own resources. Unknown writes are never replayed. Shared/temporary login
partitions and the existing provider-visible tool schemas remain unchanged.

异步结果重新校验授权代际、任务/会话、标签/页面身份、文档代际及视口版本；接管和取消只释放
自身资源；未知写入不得重放。登录分区及现有模型工具 schema 保持不变。

## Delivery / 交付顺序

1. P0: independent page bundle, PNG validation at both boundaries, structured errors
   and production-bundle regression. 独立页面脚本、双端图片校验和生产构建回归。
2. P1: same-page background rendering leases, cancellation, identity validation and
   first-open presentation without focus stealing. 同页面后台渲染、取消和首次展示。
3. P2: semantic observation/query/wait, responsive viewports, operation feedback,
   element attachments and bounded diagnostics. 语义定位、响应式、元素附件和诊断。
4. P3: bounded WebM recording, lazy tab recovery, tab limits and negotiated remote
   capabilities. 有界录制、懒恢复、资源限制和远程协商。

## Acceptance / 验收

No empty, invalid, stale or mismatched image is a successful artifact. Capture
preparation proves a nonzero compositor surface within five seconds. Temporary
capture layout never persists preferences. Current-task first open reveals the pane;
dismissal suppresses reopening in that turn. New tools are on-demand. Recording
defaults to silent 25 fps/20 seconds, capped at 90 seconds/64 MiB, one per app.
Recovery stores navigation metadata, not forms, grants, refs or pending writes.
At most 32 logical tabs per window; live pages are not silently evicted.

空图、损坏图、过期图及尺寸不符均返回失败；准备渲染最多五秒且验证原生表面。临时布局不写
偏好。当前任务首次打开显示面板，本轮手动关闭后不重开。录制默认无声、25 帧、20 秒，上限
90 秒与 64 MiB。恢复不保存表单、授权、引用或待执行操作。每窗口最多 32 个标签。

Production-bundle and native tests cover open/snapshot/input/capture, background
and hidden windows, takeover, cancellation, delayed replies and resource cleanup.
Unit tests, hosted CI and native package/platform evidence are reported separately.
Unqualified platforms return a clear background-capture error, never steal focus.

生产与原生验证覆盖完整工具链、后台和隐藏窗口、接管、取消、迟到回执及清理。单测、CI 和
原生平台证据分别报告；未经验证的平台明确拒绝后台捕获，不抢焦点。

## Implementation evidence / 实施证据（2026-09-21）

- P0: production-minified host executes the packaged page bundle in a clean
  JavaScript realm; no injected bundler helper. PNG validation precedes publication
  in Electron and model delivery in Go. Packager requires both runtime resources.
- P1 in progress: same-WebContents native capture lease, serialized capture queue,
  deadline/cancel propagation, grant revocation and document/viewport revalidation.
  Foreground-turn first-open subscription is independent of the lazy browser panel.
- Electron: 243 tests passed; TypeScript passed. Desktop Go browser tests and root
  `internal/browser/...` passed. Native macOS/Electron 44.2.0 production-minification
  smoke produced 1600×1200 PNG from an 800×600 hidden page, with unchanged page ID,
  JS variable and input value; no focused window. This is not installed-package,
  Windows/Linux, remote, recording, or vision-model acceptance evidence.
- Native reproducer: `cd desktop/electron && node scripts/browser-runtime-smoke.mjs`.
  Background capture remains unavailable outside macOS pending native qualification.
- P2/P3 and the remaining P1 acceptance cases are not completed by the above tests.
