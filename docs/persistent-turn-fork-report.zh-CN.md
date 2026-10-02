# 持久轮次分叉实施报告

日期：2026-09-14
基线：`main-v2` 的 `236db8c16`

已完成的轮次现在可以分叉为独立的子会话：父会话继续运行时分叉、父会话只读时分
叉，以及重启之后分叉。判定“该轮次已结束”的依据是持久化的事件日志，
绝不是 checkpoint：checkpoint 继续只负责自己的文件回滚职责。

## 已交付能力

- **轮次边界来自日志。** 会话投影为每个已完成轮次记录两个事实：`MessageID`，
  即该轮次最终文本回复在 transcript 中的稳定身份；`BoundarySequence`，即结束
  该轮次的提交的最后一个 sequence。资格在整个收尾提交投影完毕后计算：轮次必须
  已关闭、边界必须是提交末尾，并且不能遗留 interaction 或 tool authority。
  完成状态绝不从回答文本、经过时间或运行状态推断。
- **切分覆盖整个提交。** `BoundarySequence` 是收尾提交的最后一个 sequence，
  而不是 `turn/end` 事件的 sequence。轮次结束与随之结束的状态可以共处同一个
  提交，若在事件处切分，要么会继承半个操作，要么会被安全前缀检查拒绝。
  这两种拒绝各自保留自己的类型化原因（`ErrForkActiveAuthority`、
  `ErrForkBoundaryNotAtomic`），并且绝不会为了迁就而裁剪提交。
- **所有读取路径共用一次目标查询。** `ForkTarget` / `ForkTargetSet` /
  `ForkTargets` / `ForkSequence` 只依据已提交的事件作答，因此活跃的源、
  被其他进程占用的源，以及对已关闭会话的冷读会产生相同的目标。未结束的轮次以
  `available: false, reason: turn_open` 列出，正是这一点让界面可以只禁用该轮
  次，而不是在会话运行期间禁用所有轮次。
- **旧历史被拒绝，而不是被猜测。** 只保留消息、没有轮次记录的源会报告
  `verifiable: false` 和空目标列表。
- **只读源和冷源可以分叉。** `Session.Fork` 不再拒绝没有持有租约写者的会话，
  冷读句柄会暴露自己的目录，因此子会话无需取得父会话的写者租约即可复制父会话
  持有的文件。被其他进程占用的父会话保留自己的租约和正在运行的任务。
- **创建与导航分离。** `Service.CreateFork` 发布子会话并返回其身份，
  不打开 runtime、不切换 controller，也不写入父会话。
  `Controller.CreateForkSession` 不接受 rotation gate，因此正在运行的父会话
  继续运行。桌面端随后在新标签页中打开子会话；当这次 attach 失败时，
  结果仍带有子会话 id 和一个可恢复错误，且子会话绝不会被删除。
- **未知结果可跨重启恢复。** Desktop 在任何本地写入或网络请求前生成
  `operationId`，并把 pending 记录持久化到 `fork-operations.json`。超时、断连、
  解码失败和不确定内部错误会保留该记录；成功结果先持久化为 completed，等 UI
  打开或接管子会话后才确认删除。确认后的再次主动点击会获得新 id，因此同一轮
  仍可创建另一个独立子会话。
- **远端创建不接管父会话。** 带会话栅栏的 `GET /fork-targets` 和 `POST /fork-session` 只
  做创建，不触碰前台会话、广播绑定和租约。它们以 `session-fork-targets-v1`
  对外声明；连接到不支持该能力的服务器的桌面端会报告服务器不受支持，
  而不会回退到 `/fork`。读取响应返回权威源身份；创建必须携带
  `sourceSessionId`、`turnId`、`boundarySequence` 和 `operationId`，拒绝响应为
  结构化 JSON。
- **按钮跟随持久化日志，而不是 checkpoint。** transcript 通过稳定的消息身份
  把轮次匹配到它的目标（用 `ForkTargetView.messageId` 对比渲染出的回答的消息
  id），因此实时完成、分页历史和冷恢复共用同一套映射。创建时携带目标的源会话、
  generation 和边界，tab 切换后不能把继承的同名 turn id 解释到另一个会话。分叉不再读取
  checkpoint 或会话级的运行标志；轮次运行期间，只有未结束的那个轮次保持不可
  用。checkpoint 仍然只驱动文件 rewind。
- **每种拒绝都会说明自身。** 分叉入口会报告自己的状态——轮次未结束、
  目标仍在加载、边界无法确认、源会话已陈旧、服务器不受支持、创建进行中——并以英文、
  简体中文和繁体中文完成本地化；失败会进入既有的通知通道，而不是被吞掉。
- **旧路径行为不变。** `Fork`、`ForkForTab`、`ForkWorktreeForTab`、
  `ForkRemoteTab`、`POST /fork` 以及所有 rewind scope 都保留原有语义和
  rotation 保护，包括脏工作区检查。会切换会话的那些分叉命令仍然对 CLI 和旧客
  户端可用，即使桌面端 UI 已不再调用它们。

## 兼容结论

持久会话格式没有改变：v4 日志、manifest、帧编解码和内容存储均未改动，子会话
仍以当前格式写入。Desktop 新增独立的 Host operation 日志；可重建的 recovery
投影版本同时升级，避免旧缓存缺少新的 availability 字段。

| 字段或格式 | 旧数据行为 | 新读者行为 | 旧读者行为 | 结论 |
| --- | --- | --- | --- | --- |
| `events.frames`、`manifest.json`、帧、`.content-v1` | 不变 | 照常读取 | 能读取新版写入 | 无格式变化 |
| `Projection`、`TurnBoundary`（+availability） | 持久事件不变 | 从完整提交重新计算 | recovery 投影 v1 被拒绝并重建 | 安全的缓存失效 |
| `fork-operations.json` | 不存在 | Desktop Host 原子创建，确认后删除 | 忽略 | 增量 Host 状态 |
| Host RPC 契约 | 发布前修正 | anchor 取代仅 turn 参数，并增加确认接口 | 能力尚未发布 | 可直接修正 |
| Serve 能力集 | 增量令牌 | 声明 `session-fork-targets-v1` | 旧桌面端使用 `/fork` | 安全 |
| Rewind checkpoints（`.ckpt/` sidecar） | 不变 | 不变；rewind 仍使用它们 | 不变 | 安全 |

测试期间发现的既有不一致，不是本次引入，也保持原样：`projectLegacyImport` 在
`legacy/import` 上接受 `source` 字段，但 `internal/session/history_index.go`
在 `DisallowUnknownFields` 下解码同一事件时只接受 `messages`，因此携带
`source` 的导入事件会使历史索引重建失败。唯一的生产写入方只输出
`{"messages": …}`，所以当前没有任何路径会触发它；将来若有写入方加上
`source`，就会破坏冷历史分页。

## 缓存契约

`scripts/check-cache-impact.sh` 报告 **"No cache-sensitive prompt/tool files changed."**
没有任何对 provider 可见的提示、记忆前缀、工具 schema 或请求序列化被改动，
因此不适用任何缓存命中警告。

新的投影字段不属于 `provider.Message`，`ModelMessages` 的构造也未改变；
`TestProviderRequestBytesSurviveSessionV4RoundTrip` 通过。子会话通过目标边界
继承完全相同的事件前缀，因此它的模型上下文就是父会话在该边界处的投影，
并且没有任何 UI 锚点、禁用原因或操作 id 进入模型消息。

本报告不声明子会话首次请求的缓存命中率影响。经证实的结论更窄：父会话的请求字
节不变，且子会话继承的前缀等于父会话在目标边界处的投影。

## 验证证据

以下命令在工作树中独立重跑，而不只是由实施智能体报告：

| 命令 | 结果 |
| --- | --- |
| `go test ./internal/session ./internal/control ./internal/serve ./internal/servecontract/... -count=1` | 在合并后的最终工作树 ok |
| `go test ./internal/session -run 'ForkTarget\|CreateFork\|ForkAvailability' -race -count=1` | ok |
| `cd desktop && go test -race -run 'ForkTargets\|CreateFork\|ForkOperation\|ForkedSessionLocator' -count=1 .` | ok |
| `go test ./... -run '^$' && go build ./internal/... ./cmd/...` | ok |
| 根目录与 Desktop 的 `golangci-lint run --timeout=5m ./...` | 0 issues |
| `go run ./tools/repolint` | clean（1,230 个基线 finding） |
| `scripts/check-cache-impact.sh` | 没有 cache-sensitive 文件变更 |
| `go run ./tools/desktopinventory -check` | current，共 761 项 |
| `cd desktop && go test -run 'HostContract\|HostCommandOwners\|HostShellRemote' -count=1 .` | ok |
| `cd desktop && go test -count=1 .` | 在合并后的最终工作树 ok |
| `cd desktop/frontend && pnpm build` | ok，包含 typecheck 与 bundle budget |
| `tsx src/__tests__/turn-fork-transcript.test.tsx` | ok |
| `node scripts/run-tests.mjs --keep-going`（frontend） | 360 个 test suite 全部通过 |
| `en.ts` / `zh.ts` / `zh-TW.ts` 之间的 locale 一致性 | 11 个 `chat.branch*` key 在三个文件中齐全 |
| `node bench/fork-targets.mjs`（Chromium，真实 Transcript） | 在最终工作树 PASS |
| `node bench/fork-targets-app.mjs`（已构建应用，`/?mock=1`） | 在最终工作树 PASS |
| `make lint-cross` | 根目录 linux/darwin/windows clean；在 4 个既有的 Desktop linux tray unused stub 处停止，这些文件与 `origin/main-v2` 一致 |

浏览器 bench 针对真实的 `Transcript` 运行，使用隔离的 fixture 数据，
并读取渲染后的 DOM，而不是内部状态：

- 已完成的轮次渲染出可用入口：不含 `aria-disabled`，tooltip 和 `aria-label`
  在英文下为 "Branch into a new conversation"，在简体中文下为
  "在新对话中分支"。
- 未结束的末尾轮次渲染为 `aria-disabled="true"`，原因提示为
  "This turn has not finished yet, so it has no boundary to branch from."
  （"该轮次尚未结束，还没有可供分支的边界。"）。
- 历史中不保留轮次记录的源会把边界渲染为无法确认
  （"该轮次在会话记录中没有可确认的分支边界。"）。
- 点击会派发目标的源会话、generation、稳定 `turnId` 与边界；operation id 由
  Desktop 生成，不再由 renderer 生成。
- 在已构建应用中，一次点击会切到子会话标签页；把 fixture 强制为 attach 失败
  时，通知会指明已创建的子会话，并且**第二次点击会返回同一个子会话**；
  确认成功后再次主动点击才会创建第二个子会话。

会话测试覆盖：controller 已不存在、会话以只读方式重新打开后仍列出已完成的轮
次；冷分叉只继承到目标轮次为止的前缀；末尾轮次未结束时更早的轮次仍可分叉；
未知轮次 id 被拒绝，而不是重定向到最新轮次；切分覆盖结束该轮次的整个提交；
按操作 id 幂等重试；只读源产生可写子会话且源日志不变；提交后执行授权仍未关闭
  的边界以自己的原因被拒绝，并且不发布任何内容；同一原子提交稍后解除 authority
  时允许分叉；源替换返回 `stale_source`；Host 重建后恢复 operation；以及只保留
  消息的历史被报告为无法确认。

## 已知缺口

- **在另一个 rewind 正在提交时请求的分叉，不再被 UI 阻塞。** 这是去掉会话级
  禁用后的预期结果；改由 host 以自己的原因拒绝它。
- 携带 `source` 字段的导入 `legacy/import` 事件会使历史索引重建失败（见兼容
  结论）。这是既有问题，当前没有任何写入方会触发。

## 明确未包含的证据

- **浏览器中的远端分叉。** 远端创建路径仅由 node 测试覆盖（锚定调用形式、
  结构化拒绝、确认操作）。没有任何浏览器运行覆盖它，因为那需要一
  个可 attach 的实时 Serve 界面。
- **两个 bench 门禁没有接入 `package.json`。** 它们可以用上面的命令运行，
  但尚未在 CI 中运行，因此没有任何机制能阻止它们失效。
- **打包的桌面端和原生 shell。** 没有构建、签名或启动任何包，因此生产模式下
  的 shell/service 启动未经验证。
- **Windows 与 Linux 运行时。** 根目录的 linux、darwin、windows 跨平台 lint
  已通过；Desktop linux 跨平台 lint 仍被 4 个既有且与 `origin/main-v2` 一致
  的 tray unused stub 阻塞；两个平台都没有运行打包应用。
- **真实 provider 调用。** 没有进行任何真实 API 运行；子会话继承的上下文是依
  据持久化投影验证的，而不是依据 provider 验证的。
- **竞争条件下的跨进程租约行为。** 只读路径被设计为不加锁，测试也覆盖了关闭
  后的冷读，但没有任何测试驱动两个活跃进程争用同一个会话。

## 值得保留的 fixture 发现

bench 中的 locale 切换最初按同步方式断言，大约每七次运行就会失败一次。
原因是应用自身的特性，与本次分叉工作无关：`src/lib/i18n.tsx` 按需加载 locale
词典，`translate` 在该 chunk 解析完成前回退到英文，因此切换后的首次渲染显示
英文是合理的。十次运行测得的生效延迟为 12-98 ms，因此没有任何内容丢失或卡住
——页面本来就没有承诺同步切换。bench 现在改为等待渲染出的值
（`page.waitForFunction`，10 s 上限），而不是 sleep，并在最终工作树通过。
今后任何读取本地化文本的浏览器检查都必须照此处理。
