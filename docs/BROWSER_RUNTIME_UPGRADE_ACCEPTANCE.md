# 内置浏览器运行时升级：实现与验收记录

日期：2026-09-21。Reasonix 分析基线 `61cc393b2027391bc70742b1ce50531c3efd0b02`，
分支 `codex/browser-runtime-upgrade`。ZCode `872ad960de7e` 仅作为设计参考，未复制其
webview/CSS 截图宿主，也未实际运行完整 ZCode 应用。

代码按 P0、P1、语义运行时、增强能力/UI、日志/交付边界及选择器修正分阶段提交；
功能代码检查点为 `8de8bf84d`。后续集成至 `feature/browser-runtime-observability`，
已创建草稿 PR #10635；没有发布或修改用户的已安装应用。

本文区分代码实现、本地自动化、原生运行和发布验收。macOS 原生测试通过不代表
Windows/Linux 或已签名安装包通过。当前不应宣布三平台发布门槛全部完成。

## 故障与修复

原反馈表现为：导航返回成功，快照报 `browser host: Script failed to execute`，
截图得到 0×0。URL 返回只能证明导航请求得到响应，无法证明注入和渲染表面可用。

原页面函数通过业务函数 `toString()` 注入，生产构建的函数名保留辅助逻辑可能留下
页面环境不存在的 `__name`。现在页面脚本独立构建为自包含、压缩、`keepNames:false`
的 bundle；主进程保留原构建参数。运行时检查版本与 SHA256，打包检查资源存在。
测试在没有宿主辅助变量的环境执行真实产物，保留旧机制的失败回归。

截图保留同一 `WebContentsView`，不重开 URL。macOS 后台捕获临时借用不激活、忽略
鼠标的宿主，前台有效表面直接捕获。实际 compositor 探针与 5 秒准备预算决定是否
就绪；导航、接管、关闭和取消释放租约。用户重新展示页面优先，释放时读取当前 UI
意图。正常后台节流只在观察/捕获租约期间临时解除。

Electron 校验 PNG 签名、尺寸、解码与像素预算，Go 在进入模型上下文前再次解码。
原子写入完成后才返回；损坏图、空图和失效元素引用均报错，不改截整个视口。
内联仍限制 8 MiB；有效超限文件保留为产物。整页图在捕获前执行像素预算检查。

## 已实现的能力与边界

| 项目 | 实现位置与行为 |
| --- | --- |
| 固定版本语义内核 | `desktop/electron/scripts/playwright-page-resource.mjs` 验证 Playwright 1.62.1 注入源码及构造/方法契约，构建期绑定资源和许可证。升级不兼容时构建失败；不在运行时搜索私有路径。 |
| 页面引用 | `pageSemantic.ts`、`snapshot.ts`、`frameRuntime.ts`、`refResolver.ts`：隔离世界、Reasonix refs、文档 token、跨域/OOPIF 会话与父框架坐标映射，释放自行建立的 CDP 会话。 |
| 身份与取消 | `hostCalls.ts` 绑定 grant、任务、会话、页面、epoch、视口 revision、requestId/deadline，返回前复核；取消传播到等待和租约。导航已派发后的取消维持 unknown，不回滚或重放导航。 |
| 错误 | `page_not_ready`、`script_runtime_error`、`surface_unavailable`、`invalid_image`、`stale_document`、`taken_over`、`capability_unsupported` 等分类及 RPC data 保留。 |
| 观察元数据 | 快照/截图可选返回 URL、时间、文档代际、视口版本、CSS 尺寸；截图保留实际 PNG 尺寸与坐标观察 token。旧结果字段保留。 |
| 操作反馈 | 队列、准备、读取/交互/捕获、完成与失败状态进入标签视图，失败保留诊断编号和经脱敏的摘要；宿主日志仅含 build、平台、requestId、阶段、耗时和错误分类。 |
| 首次展示 | 当前任务/会话每轮首次新标签展开，后续更新不重新展开，其他任务不抢面板。 |
| 响应式 | 自然模式及手机 393×852、平板 768×1024、桌面 1280×720、自定义和旋转。CSS 视口与 Fit/显示比例独立；DPR 1，AI 标签固定逻辑视口，截图临时布局不改偏好。 |
| 元素附件 | 受信任 UI IPC 先接管；主文档选择器高亮、点击选取、Esc 退出。有限语义/文本/位置/样式加入原任务草稿，不自动发送、不采集密码值，不作为可执行旧 ref。 |
| 诊断 | console/exception/network/navigation 分开，200 条/256 KiB 上限，游标与类型筛选。URL 去凭据/query/hash，常见敏感字段脱敏，不采集请求体或认证头。不可用与零错误分开。 |
| 录制 | 原页面、隔离 renderer、目标限定媒体授权、无音频、WebM。25 fps 目标帧率，默认 20 秒、最长 90 秒、64 MiB 上限，一次最多一个任务；实际解码后才发布产物。 |
| 录制中断 | AI 录制遇接管、撤权、导航、页面/视口/捕获表面变化中断，清理临时产物；用户录制由 UI 管理。关闭面板后仍可从 View 菜单或 Ctrl/⌘+Shift+R 停止。 |
| 恢复 | 版本化原子元数据、未知字段保留、未来版本只读；逻辑占位不创建页面进程，重新授权/观察。本地预览从原文件引用重新授权。 |
| 远程 | 可选 Executor/HTTP 能力协议，旧端明确不支持；录制文件复用现有 SFTP 产物中继，远端结果不直接暴露桌面本地路径。 |
| 资源 | 32 个逻辑标签，无自动关闭；普通 AI/UI 捕获共用串行队列，录制独立长期租约，按需加载模块。 |

元素选择器目前面向主文档及其 Shadow DOM；跨域 iframe 的语义快照、查询、点击走
工具运行时，不能将它们误报为已覆盖跨域 iframe 的人工元素选择 UI。

## 可选工具

原 14 个工具名称、说明及 schema 的序列化 SHA256 与基线一致：
`1625d99eb8ff973c6d6ebadc2eb12dd30098b6e434d2c4a9b3a80dd3da73bb1b`。
新工具加入按需 capability inventory，不改常驻系统前缀。

| 工具 | 使用约束 |
| --- | --- |
| `browser_query` | documentToken + role/name/text/testId，可用 containerRef 或已有元素 frameRef 指定范围；多匹配明确返回歧义。 |
| `browser_wait` | 默认 3 秒，上限 8 秒；等待 DOMContentLoaded、精确 URL、元素状态，不依赖 networkidle。 |
| `browser_viewport` | get/set/reset，set/reset 需要新 operationId，宽 320–3840、高 320–2160。 |
| `browser_pointer` | hover/drag/click；坐标须来自当前视口截图 observationToken，页面/滚动/尺寸变化后重新观察。 |
| `browser_diagnostics` | 有界、脱敏的页面诊断，支持 after 游标和 kind。 |
| `browser_record` | start/status/stop/cancel；写操作使用原操作账本，只有 completed 的有效文件才可交付。 |

推荐验证流程：复用确认过地址的任务标签 → 有界等待 → 语义快照 → 定位并操作 →
元素状态/URL/下载验证 → 必要时截图或录制。旧 ref 不重试，unknown 写操作不重放。
动态项目读取开发服务器实际地址，静态文件使用现有 `browser_preview`。

## 本地验证

- Electron TypeScript 检查及 252 项单元/契约测试通过，包括独立页面产物、Playwright
  适配、图片拒绝、受控取消与迟到回调、恢复占位、诊断上限和敏感字段处理。
- 根 Go `go test -p 2 ./...` 全量通过，包含浏览器/CDP、boot 和缓存前缀契约。Desktop 浏览器、文件预览及被全量超时截断的
  fork 用例定向运行通过。原工具前缀 golden 测试通过。
- 前端正式构建、类型检查、浏览器面板/首次展开/草稿接入相关测试执行；初始包预算
  保持原上限，最终 canary 壳构建通过懒加载维持约 2046.9 KiB / 2047.0 KiB，不增加预算。
  元素选择迟到后仍归原会话、返回原会话只交付一次的回归通过；关闭面板取消选择器的
  实际页面 bundle 回归通过。
- macOS arm64 / Electron 44.2.0：生产压缩参数下，隔离用户目录执行真实原生页面。
  同源/跨域隔离快照、跨域按钮点击、后台截图保留页面变量与表单、393×852 响应式 PNG、
  1280×720 WebM 解码、接管中断、100 轮页面生命周期通过。最终 `remainingWebContents=0`，
  正常 `app.quit()` 完成，未把强制退出算作成功。
- 构建了本地未签名 macOS 测试壳，验证 ASAR 内页面 bundle 与 manifest 摘要一致，录制
  脚本、preload、Playwright LICENSE/NOTICE 齐全；此步骤不是正式安装包验证，未把缺失服务二进制的
  壳包宣称为可发布完整应用。

原生证据位于 `desktop/electron/artifacts/browser-runtime/darwin/`：
`result.json`、`browser-evidence.png`、`browser-recording.webm`。PNG 已直接查看，内容为
确定性测试页面和 iframe 按钮，并非空图。WebM 在 Electron 中实际加载解码确认尺寸。
测试报告的 durationMs 是捕获会话时长。另外用本机已有 ffprobe/ffmpeg 独立验证短片：
VP8、1280×720、25 个可解码帧，PTS 从 0 到 0.960 秒；提取首帧并直接查看，确认包含
Save 按钮和 retained 输入框。`video-probe.json` 和 `video-first-frame.png` 保存在同一
证据目录。ffmpeg 仅用于本地验证，没有加入应用、构建或运行时依赖。

首次全量运行的历史记录：根 `go test ./...` 的 control 包、Desktop 的主包达到
默认 10 分钟包级总时限；session 包一项 projection 测试文件锁超时。对应三个用例
分别重跑通过（约 30 秒、Desktop 定向套件约 2 秒、session 约 7 秒）。随后根 Go
降低包间并发，以默认超时完成全量通过：control 284 秒，session 169 秒。保留首次
失败事实，也未修改无关测试或加长超时掩盖失败。

Desktop 以 `go test -p 1 ./...` 再次全量运行仍在 601 秒时触及默认包级时限；没有
独立断言失败，超时时当前 `TestResumeSessionPageBuildsFirstScreenFromOneDurableRead`
仅运行 0 秒。因此当时 Desktop 全量保持未通过，不能将当前用例误判为死锁，也不能将
定向浏览器测试通过写成 Desktop 全量通过。
该被截断用例随后单独重跑通过（5.881 秒）。
本轮已用仓库现有分片完成全部用例，未改包级超时；见文末的后续验收记录。

macOS 测试进程曾遇退出阻塞。上游
[Electron PR #51488](https://github.com/electron/electron/pull/51488)
说明 AppKit 崩溃恢复弹窗可能影响测试；runner 仅对隔离测试子进程传
`-ApplePersistenceIgnoreState YES`，不修改系统偏好。应用产品启动参数不受影响。

## CI 与发布门槛

### 后续 bug 检查（2026-09-21）

对实现检查点 `20508570d` 的本地审查确认并修复四项问题，每项均先运行失败回归：

- 用户录制被普通点击/输入的接管代际变化中断。录制现区分页面/服务生命周期与控制权
  代际；用户输入不终止用户录制，导航、崩溃、服务断开仍终止，AI 录制仍受接管约束。
- 准备阶段的工具/UI stop 未取消准备，可能继续等完整录制时长。所有停止入口共用状态处理。
- `abortable` 已取消入口没有消费底层操作的迟到拒绝，产生 unhandled rejection。
- 嵌套 OOPIF 新会话建立后，释放父会话失败会遗漏子会话。现在先登记所有权，退出时
  清理所有自行建立的会话，不断开外部拥有的 debugger。

Electron 类型检查及 256 项测试通过。首次 macOS 原生运行验证用户录制在输入报告后继续，
停止生成 WebM，AI 接管仍中断，最终 webContents 为零。

**修复前失败记录：** 后续运行在跨域 iframe 快照或
旧 ref 的隔离环境解析阶段超时。按命令计时的跟踪将失败定位到 `Page.createIsolatedWorld`，
不是语义脚本解析（成功调用约 11–12 ms）。曾试验在新 OOPIF 会话启用 Runtime，失败依旧，
已撤销该未证实的修复；没有增加 deadline 或添加自动重试。失败退出还曾触及测试 runner
60 秒进程上限，强制退出不算验收成功。成功的早期结果不能覆盖这些后续失败。

可用 `REASONIX_BROWSER_TRACE_RPC=1 node electron/scripts/browser-runtime-smoke.mjs`
（工作目录 `desktop`）输出仅命令名和耗时的诊断。

### CDP 生命周期修复与复验（2026-09-21）

对照 ZCode `872ad96` 后，采用操作级 context 复用、同一 iframe 身份重新解析和统一
连接所有权。没有复制其子框架失败静默退化行为，也没有放宽 1 秒子框架总预算。

- 根 debugger 由 `debuggerLease.ts` 管理；相邻观察、定位、输入共用连接，空闲 1500 ms
  才释放。计数涵盖尚未返回的底层 CDP 命令，调用方超时不等于命令完成。
- 发送函数绑定连接代际；页面崩溃、销毁或 debugger detach 使旧代际失效。销毁后的
  清理只取消计时器，不访问已销毁的原生对象；外部拥有的连接不被主动拆除。
- `FrameRuntime` 在一次操作内复用 context 与子会话，保留父会话直到操作结束。
  先释放对象，再断开相应 session；清理等待有界，迟到回复仍由原连接记录。
- 只有只读 attach 失败且同一 DOM object/backendNodeId、原生 frame 仍有效时，才
  重新解析改变的 frameId；没有再次执行已经派发的页面脚本、点击或上传。
- 请求取消传入 frame runtime；取消后停止派发后续命令，迟到 attach 结果只做清理。
  根连接复用后的上传仍恢复自己拥有的 Runtime 域状态，防止下次上传漏收 context 事件。

本机对照确认：保持根连接时此前失败的跨域观察/解析路径可以完成；按操作立即拆建
连接的旧路径出现 `Page.createIsolatedWorld` 超时。修复的是 Reasonix 的连接生命周期
边界，不据此宣称已定位 Chromium 内部实现缺陷。清理回归另外暴露了对象释放与 detach
并发会留下在途命令，现已用确定性屏障测试及顺序清理消除该路径。

最终验证：Electron 类型检查、264 项测试、正式参数 shell build 通过。macOS arm64 /
Electron 44.2.0 在真实页面上完成 100 轮**跨域 iframe** 打开、观察、子元素解析、截图、
关闭；每轮仅保留宿主窗口，最终 `remainingWebContents=0`，正常 `app.quit()` 完成。
另验证空闲连接确实断开后可以重新观察，以及响应式 PNG、录制完成、AI 接管中断、用户
输入不打断用户录制。证据存于 `desktop/electron/artifacts/browser-runtime/darwin-cdp-lifecycle/`。

English: root CDP leases now outlive adjacent calls, track pending protocol commands, and fence
late cleanup by connection generation. Frame contexts and child sessions belong to one operation;
only a failed read-only attachment may refresh the same retained iframe owner. Type checks,
264 tests, a production-parameter shell build, and 100 native cross-origin lifecycle cycles passed
on macOS. Windows/Linux and signed installed packages remain separate, unverified release gates.

这些修复仍在本地工作区，未推送、创建 PR 或发布。以下跨平台、安装包及远程验收缺口
仍然适用；本次只改 Electron，不重跑此前有包级超时的 Desktop Go 全量测试。

现有 Linux、macOS、Windows 原生作业分别新增真实浏览器工具运行时 smoke，并上传
PNG/WebM/JSON 证据；macOS 作业执行 100 轮。尚未推送触发托管 CI，不能声称已通过。

Windows/Linux 后台捕获保持 capability_unsupported，并提示显示页面后重试；目前
只在这些平台的 CI 中验证前台路径。通过原生后台资格测试后才能独立放开平台开关。

发布前仍须完成：

1. 三平台最终 SHA 的正式安装包验证，包含窗口最小化、菜单/弹窗、用户接管/调整尺寸、
   Retina/Windows DPI 和截图坐标一致性；确认完整 Go 服务与壳资源 build 一致。
2. 真实 SSH 环境的录制文件转移、断线/重连和旧远端协商。当前有协议/路径约束测试，
   不等于真实网络端到端证据。
3. 正式应用重启后的任务身份恢复、原 HTML 重新授权，以及没有旧 grant/ref/操作重放。
4. 90 秒/64 MiB 边界核验；当前原生用例覆盖短片解码、独立帧时间线与接管中断。
5. 视觉模型通过完整 Go → provider 链路收到真实图像内容的验收；当前已覆盖图像解码/
   data URL 交付和直接查看产物，未调用外部视觉模型冒充该项通过。
6. 完成 Desktop 全量测试并汇总最终托管 CI 结果；根 Go 本地全量复核已经通过。

未通过的平台和场景维持明确不可用或未验收状态，不用另一页面、另一浏览器或静默
扩大截图范围伪装成功。P0 故障修复可独立评审，后续资格验收不应阻止其代码评审。

### 会话诊断中的浏览器证据 / Browser evidence in session diagnostics

“导出会话诊断”现在附带 `browserDiagnostics`。Markdown/JSON 对话导出的既有工具
调用展示不变。排障时导出发生问题的会话，先从 `commits` 找到工具调用及
`operationId`，再关联 `browserDiagnostics.operations.operations` 和
`browserDiagnostics.host.entries` 中的 `operationId`、`requestId`、`tabId`。

- 操作账本：只读导出当前会话最近 200 项，保留 `reserved`、`unknown` 等原状态；
  不恢复、不重放、不结算操作。未加载账本时只读磁盘，读取预算 64 MiB。
- 宿主事件：整个应用最多保留 512 项 / 256 KiB。包含请求开始/完成/失败、观察阶段、
  CDP 命令开始/完成/失败及耗时、页面诊断和标签/授权生命周期变化。
  `Page.createIsolatedWorld` 只有开始而没有结束时，可与同请求的失败阶段对照。
- 归属：导出前固定本地或远程主机与规范会话 ID；切换面板、关闭标签、撤销授权
  不会把其他会话的内容加入导出。相同远程文件路径不能替代规范会话身份。
- 脱敏：不导出 grant、DOM token、输入参数、请求体、CDP 表达式或操作参数摘要。
  页面诊断删除 URL 凭据、query/hash，并过滤常见凭据文本。日志本身可能包含页面
  输出的业务内容；这不是任意敏感文字都可自动识别的保证。
- 可用性：检查 `available`、`sessionEvidenceAvailable`、`truncated`、`retention`
  与 `coverage`。壳事件仅保留当前进程内存窗口；重启前事件不可恢复。
  账本新增可选归属字段；升级前记录或被旧版本重写而失去该字段的记录不猜测归属。
  对话提交的固定前缀与运行时采集时间可能不同，导出中分别说明。
- 兼容：新增内部 `host/browser.exportDiagnostics`，旧壳不支持时标记不可用；
  远程诊断仍使用原请求协议，浏览器部分在桌面本地合并，不要求升级远程服务。
  仅覆盖本桌面托管的远程会话浏览器，不采集远端独立 CDP 浏览器日志。

English: Session diagnostic exports now include scoped browser evidence, while ordinary
conversation exports retain their existing format. Durable operations are read without
settlement or replay. A bounded, in-memory shell window correlates request phases, CDP
timings, page errors and tab lifecycle events. Explicit coverage and availability fields
distinguish missing history from a clean run. Old shells remain usable, and remote reports
receive desktop browser evidence locally without changing the remote export protocol.
Optional ledger attribution is diagnostic-only; older writers may drop it, after which
those records are omitted rather than attributed by guessing.

本地验证 / Local evidence：Electron 270 项测试、typecheck、production build；
Desktop Go 浏览器、账本和会话导出定向测试。新增回归覆盖只读保留未知/预留状态、
预算与脱敏、保存对话框切换会话、同路径远程身份切换、旧壳、取消和旧远程端合并。
macOS Electron 44.2.0 的生产参数原生测试验证真实 console 错误、跨域 iframe CDP
追踪、关闭后的事件保留、脱敏及零残留页面。产物：
`desktop/electron/artifacts/browser-runtime/darwin-diagnostic-export/browser-diagnostics.json`。
该证据是原生测试 bundle，不等于签名安装包、Windows/Linux 或真实 SSH 验收。

#### 诊断导出复查修复 / Diagnostic export review fixes

- 远程归属解析和 executor 创建现在处于同一个 `remoteTabMu` 临界区。
  以会话身份而非临时展示路由生成 scope；`/resume` 尚未确认或身份不一致时拒绝
  新浏览器请求，不撤销旧 grant。回滚后继续使用原 executor。兼容仅提供路径的旧端。
  同时移除解析路径中的 `remoteTabMu → sessionMu` 反向锁获取。
- 页面诊断归属直接查询当前有效 grant，删除独立的 256 项归属缓存。
  事件仍遵守原 512 项 / 256 KiB 上限；已绑定页面在撤销授权后仍保留历史归属。
  不依据过期 generation 或相互冲突的 scope 猜测归属。
- URL 文本清理覆盖大小写混合协议。新增测试在宿主导出序列化层验证 OAuth code、
  signature、userinfo 和 fragment 均不出现在结果中。

English: Browser resolution and executor creation now share one lock epoch, reject
provisional or inconsistent remote identity, and retain the original grant on rollback.
Diagnostic attribution reads active grants instead of an independently evicted cache.
URL scrubbing handles protocol casing without changing the bounded event window or
the existing remote export protocol.

验证覆盖：受影响 Desktop Go 测试与定向 `-race`、270 项 Electron 测试、typecheck、
production build。原生复验产物位于 `artifacts/browser-runtime/darwin-diagnostic-fixes/`；
可在 `desktop/` 执行以下命令，把真实 Electron 证据送入 Go 的校验、脱敏和最终文件
合并流程，缺省不设置该变量时该测试使用宿主协议 fixture：

```sh
REASONIX_BROWSER_DIAGNOSTIC_FIXTURE=electron/artifacts/browser-runtime/darwin-diagnostic-fixes/browser-diagnostics.json go test . -run '^TestBrowserDiagnosticHostEvidenceFinalJSON$' -count=1
```

原生验证限制：首次运行在进入诊断用例前发生 OOPIF `DOM.enable` deadline 超时；
后续启用命令计时的同参数复验通过，未修改相关超时或加入重试。首次失败和后续追踪
分别保留为 `native-first-failure.log`、`native-trace.log`。这证明本次诊断修复的原生
路径可用，不证明此前的跨域 iframe 间歇故障已消除；根因仍待单独定位。

#### 跨域执行复查 / Cross-origin execution follow-up

- 修复文件上传自定义回调直接使用原始 CDP sender 的遗漏。现在每条命令使用
  FrameRuntime 的取消、deadline 和页面存活检查，并在返回前复核目标 iframe。
  取消、超时、导航或移除后的迟到查找结果不得触发文件写入；写操作已发出但没有
  回执时结束等待，由既有 ActionExecutor 保留 `unknown`，不自动重放。
- 确定性回归在修复前失败、修复后通过，覆盖四种查找失效顺序及写入回执不返回。
  即使底层命令尚未返回，也验证自有子 session 已清理。
- 原生追踪现在覆盖工厂创建的所有页面，记录命令编号、页面 ID、root/child、
  开始/结束及耗时，不记录参数或表达式；原生产物额外记录 Chromium 版本。
- 修复前基线 100 轮快照、引用解析、截图、关闭全部通过，2870 条命令均收到返回，
  最长 26 ms，页面零残留。证据位于 `artifacts/browser-runtime/darwin-dom-enable-baseline/`。
  本轮未复现此前的 `DOM.enable` 超时，不能据此认定其根因已消除；未修改超时、
  添加重试或移除 domain enable 命令。
- 修复后验证：276 项 Electron 测试、typecheck、production build 通过。
  macOS Electron 44.2.0 / Chromium 152.0.7977.76 原生复验验证真实跨域文件上传、
  idle 断开后重新观察、响应式截图、录制及页面零残留，证据位于
  `artifacts/browser-runtime/darwin-frame-fence/`。本轮不是 Windows/Linux 或签名包验收。

English: Custom frame callbacks now share the runtime's deadline, cancellation and
frame checks. Late upload lookups cannot dispatch after invalidation; a missing write
receipt terminates the waiter without replaying the operation or claiming certainty.
Deterministic tests failed before the fix and pass afterward. Native evidence covers
a real cross-origin upload and cleanup. The earlier 100-cycle baseline did not reproduce
the intermittent CDP stall and does not establish its root cause or resolution.

#### 完整工具链与跨平台补验 / Packaged toolchain and native follow-up

本轮基于 `20508570d` 加当前工作区修复，不是已发布版本。新增修复：

- `refResolver` 保留运行时的结构化错误；CDP 超时、脚本错误及取消不再统一伪装成
  stale ref。真正失效的文档仍返回 `stale_document`，同源与子框架路径均有回归。
- 面板首次展开不再使固定 CSS 视口的快照失效。Electron 分别维护逻辑视口版本与
  原生表面版本；自然视口尺寸变化仍失效，截图和输入继续校验物理表面变化。
  完整安装包曾在首次快照稳定复现该失败，修复后同链路通过。
- 鼠标 CDP 分发仅还原页面 zoom，保留 Electron emulation 的 Fit 比例。
  修复前安装包向约 `(211,94)` 派发，页面在 `(484,215)` 的 BODY 收到点击；
  修复后同页面的 BUTTON 收到点击。浏览器 zoom 与显示比例不能用一个因子还原。
- 原生和完整包 runner 在每次开始/失败时更新 `result.json`，防止失败后残留上一轮
  `completed:true`。CDP 追踪先缓存在内存，结束后输出，减少逐命令终端 IO 干扰时序。

English: Typed runtime errors survive ref resolution. Fixed logical viewports no longer
expire merely because the panel expands, while input/capture surface fences remain.
Mouse dispatch removes browser zoom but retains Electron's emulation display scale.
Native runners explicitly mark failed attempts instead of retaining prior success.

| 验证 / Check | 本轮证据与边界 / Evidence and limits |
| --- | --- |
| Electron | 279 项测试、TypeScript 检查通过；完整生产构建包含最新产品代码。 |
| Desktop Go | 现有 `desktop-windows-go-tests.mjs` 清单在 macOS 验证 2846 项顶层测试/示例/fuzz 种子无遗漏或重复；A–B、C、D、E–H、I–P、Q–S、T–Z 七组全部退出 0。每组仍用原包级 timeout，未声称单进程不分片运行已低于 600 秒。 |
| macOS 完整包 | 完整 Go service/CLI/renderer/Electron 应用，隔离数据目录、严格版本握手、实际 `use_capability` → Go → Electron → provider 图片字段。跨域按钮确认、关闭面板后截图、同一页面/表单保留、面板不重开、正常退出通过。provider 是确定性 loopback fixture，不代表真实模型理解图片。 |
| macOS 100 轮 | 同源和跨域框架同时存在，100 次打开/快照/ref/截图/关闭；3484 条 CDP 命令，页面最终零残留。该轮仅覆盖 Fit 点击，不包含随后新增的 50%/75% 扩展矩阵，不能覆盖其他失败。 |
| 90 秒录制 | 原生 WebM 正常封装并解码；ffprobe 确认 VP8、1280×720、2249 个包，画面 PTS 0–89.923 秒。未验证真实 64 MiB 边界。 |
| Windows 11 ARM64 | Electron 44.2.0 原生 fixture：前台截图、跨域快照/点击/上传、Fit 点击、响应式 PNG、录制/接管/停止、诊断脱敏、零残留和退出 0。VM 有 GPU 重启日志；不是物理设备 DPI、签名安装包或后台捕获验收。 |
| Ubuntu 24.04 ARM64 | Electron 44.2.0、Xvfb、普通用户且 sandbox 开启：同类前台 fixture 通过。不是 Wayland/真实桌面窗口矩阵或发行包验收。测试结束已撤掉新增共享目录并恢复 VM 挂起。 |

证据目录相对 `desktop/electron/artifacts/browser-runtime/`：
`darwin-packaged-provider/`、`darwin-fit-100/`、`darwin-90-second-recording/`、
`windows-native/`、`linux-native/`、`follow-up-checks/`。
完整包为 `desktop/build/candidate/darwin-arm64/Reasonix.app` 及
`dist/Reasonix-darwin-arm64.zip`，仅本地 ad-hoc 签名，未公证、未发布。

**仍未通过的门槛 / Remaining gates：**

- macOS 间歇 CDP 超时再次在 `DOM.enable` 复现。成功的 100 轮不能证明修复；
  已保留失败日志，未增大产品 deadline、静默漏掉框架或重放工具调用。
- 扩展响应式点击矩阵暴露另一个待定位现象：在同源和 OOPIF 并排、原生视图从捕获
  宿主归还、切换 Fit/50%/75% 后，正确坐标偶尔在主文档的 IFRAME 元素收到事件，
  子文档未收到点击。坐标重复换算已修复，但不能据此宣称 OOPIF 路由稳定。
  compositor 截图探测、原生 mouse 路由及启用观察租约的实验未消除失败，均未作为
  产品修复引入。新增失败断言保留在原生 runner；详见 `darwin-scale-matrix/`。
- Windows/Linux 后台捕获仍明确不支持；上述前台通过不改变能力开关。
- 真实 SSH 端到端产物交付、真实视觉模型感知、64 MiB 录制上限、重启恢复完整链路、
  各平台 DPI/窗口状态矩阵及正式签名安装包发布资格仍未完成。

English: Release qualification remains blocked. An intermittent CDP domain command
still stalls, and the expanded emulation matrix can deliver input to the embedding
IFRAME instead of the child document. Passing earlier fixtures are bounded evidence,
not a resolution of either failure. Windows/Linux background capture remains disabled;
real remote/model, recording-size, recovery and signed-package gates remain open.

### 输入所属进程修复 / Renderer-owned input follow-up

扩展矩阵确认：缩放和原生表面迁移之后，根 CDP 的跨进程命中路由可能使用上一帧信息。
同一次点击曾在父页面 IFRAME 收到 mousedown，却在子页面按钮收到 mouseup；普通 125%
缩放也曾丢失按下事件。统一 CDP mouseMove、模拟焦点、等待动画帧或先截图均不能单独
消除该错路由，未保留焦点模拟、额外截图或延时重试作为修复。

`mouseInput.ts` 现在在隔离 DOM 环境逐级解析命中的 frame：同进程目标直接发送到主
renderer，保留原生显示坐标；OOPIF 发送到其所属 CDP session，使用所属 renderer 的 CSS 局部坐标。每个
异步边界重新验证 frame 身份、视口和任务授权；取消后迟到结果不能派发输入。移动前
有界检查连续动画帧的目标/视口稳定性，失败要求重新观察，不自动重放点击。

确定性测试覆盖主文档、同进程子框架、OOPIF、取消、frame 替换、缩放变化与撤权。
页面稳定性测试执行真实压缩 bundle，并检查移动、替换、脱离及无法绘制时清理回调。
284 项 Electron 测试、类型检查和生产构建通过；macOS 最终源码完成三次完整缩放矩阵
及 100 轮混合 frame 生命周期，退出前页面数量归零。Windows ARM64 与 Linux ARM64/Xvfb
也通过了该输入路由的原生矩阵；CI 对最终提交的跨平台结果另行记录。

还修正了诊断原生 fixture 绕过观察/渲染租约直接读取隐藏页的问题，并增加明确的跨域
子树断言。该问题曾使 `Page.getFrameTree` 超过 1 秒；不能据此认定历史 `DOM.enable` /
`Page.createIsolatedWorld` 间歇超时属于同一原因，后者仍保留为合并前需核实的门槛。

English: Root CDP hit testing could route one click's press and release to different
renderers after zoom or native surface changes. Input now resolves frame ownership
in isolated DOM contexts and dispatches directly to the owning renderer, with
bounded stability checks and cancellation/identity fences. Deterministic tests and
the native scale matrix pass; macOS also completes 100 mixed-frame lifecycle cycles.
The diagnostic fixture now uses the same leases as the host tool. Historical CDP
domain/world stalls remain a separate investigation gate; these results do not
claim that an unrelated successful rerun resolves them.

### 连接级 iframe session 所有权 / Connection-owned iframe sessions

后续修复移除了每次快照、引用解析、点击和上传之间的 OOPIF detach/attach 及重复 domain
初始化。`FrameSessionPool` 由根 debugger 的连接代际持有；相邻操作共享目标 attachment，
各自重新获取文档 context、各自释放对象组。空闲断开清理整代资源，外部 debugger 保留。
失败 session 在最后一个借用者退出后回收；取消一个等待者不会拆掉另一等待者的 session，
迟到 attachment 只清理自己的 ID，不覆盖新目标。产品预算仍为 1 秒，没有重放写操作。

原生 fixture 增加机制断言：跨域快照、ref、点击和上传整条链只 attach 一次；真实空闲断开
后的观察恰好再 attach 一次。确定性测试覆盖共享初始化、单方取消、迟到 attachment、
目标自行断开、外部连接保留、对象清理与空闲释放顺序，以及已派发写操作的 unknown 语义。
287 项 Electron 测试与类型检查通过。macOS 最终源码完成扩展缩放矩阵和 100 轮混合
frame 生命周期，包含上述 attachment 次数断言，最终 `remainingWebContents=0`。
Windows ARM64、Linux ARM64/Xvfb 的新 session 所有权原生 fixture 也通过。

这解决了连接生命周期反复拆建的机制，历史超时日志继续保留；不把所有未来 CDP 超时
都归为同一原因。当前提交仍须通过 CI 和完整包链路后才满足合并门槛，正式发布的其他
环境限制仍以本文所列范围为准。

English: Target sessions now belong to the root debugger generation, while context
lookups and object groups remain operation-scoped. Adjacent snapshot/ref/input/upload
calls share one attachment; idle reconnect creates exactly one new attachment.
Deterministic interleavings cover shared initialization, cancellation, late results,
detachment, external connection ownership and unknown write receipts. All 287 tests
and the native macOS 100-cycle fixture pass, as do Windows ARM64 and Linux ARM64/Xvfb
fixtures. Historical timeout evidence is retained; final CI and packaged validation
are still required, and unrelated release qualification limits remain unchanged.

### 精确落点与嵌套框架 / Exact pointer coordinates and nested frames

进一步检查实际事件坐标发现：较宽按钮会掩盖重复缩放，Fit 比例 0.625 时点击虽成功，
实际位置约 `(31,11)`，偏离观察中心 `(50.828,18.75)`。子 CDP session 接受其 renderer
的 CSS 坐标，不应再乘外层 Fit 比例。若命中 OOPIF 内的同进程子框架，输入仍属于
OOPIF 的 renderer，不能使用最内层 frame 的局部坐标。运行时现在显式携带 session
所属 frame，输入分发从逐级遍历保存的坐标中选择该 owner 的坐标。

新增确定性嵌套框架回归，原生矩阵同时断言事件落点与观察中心相差不超过 2 CSS px。
288 项 Electron 测试、类型检查通过；macOS 三次缩放矩阵及 100 轮生命周期、Windows
ARM64 和 Linux ARM64/Xvfb 原生矩阵均通过，包含自然 125% zoom、Fit/50%/75%、同进程、
OOPIF 及 OOPIF 内同进程子框架。macOS 最新完整生产包通过 Go → Electron 跨域点击、
关闭面板后台截图、provider PNG 交付及正常退出验证。历史失败和正式发布环境缺口保留。

English: A broad button concealed duplicate scaling: child-session dispatch now uses
renderer-local CSS coordinates without reapplying outer Fit. Same-process descendants
inside an OOPIF use their owning OOPIF renderer's coordinates. All 288 unit tests,
typechecking, the macOS 100-cycle fixture, Windows/Linux native matrices and the latest
complete macOS package pass. Native checks assert the received point is within 2 CSS
pixels of the observed centre, including nested frames and natural zoom. These results
do not extend the release qualification scope documented above.

### 录制终态发布 / Recording terminal-state publication

CI 的 macOS 原生用例暴露收尾竞态：WebM 已校验并移动到目标路径，状态提前标为
`completed`，但异步清理尚未释放全局录制占用；紧接着启动下一次录制被拒绝。
现在清理期间保持 `finalizing`，清理完成后在同一同步边界释放占用并发布终态和产物。
取消和中断也沿用同一边界。确定性测试阻塞真实文件清理，验证清理前不发布产物、
清理后立即允许下一次录制；该测试在修复前失败，未添加等待重试。

English: macOS CI exposed completed status before asynchronous cleanup released the
global recording slot. Cleanup now stays finalizing; slot release, terminal status
and artifact publication happen together. A controlled file-cleanup barrier reproduces
the failure before the fix and covers both completion and cancellation.
