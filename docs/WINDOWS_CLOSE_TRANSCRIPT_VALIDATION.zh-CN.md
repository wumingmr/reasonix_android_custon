# Windows 关闭与 transcript 诊断验收说明

[English](WINDOWS_CLOSE_TRANSCRIPT_VALIDATION.md)

本文记录 `main-v2` 上 Windows 重复关闭竞态修复及 transcript follow 诊断的验收边界。
未知的 transcript 同步根因尚未定位，因此本文不声称已经修复该未知根因。

## 已实现行为

- 标题栏最小化、最大化、最大化状态查询和关闭直接通过 preload IPC 交给 Electron。
  仍使用旧 Go 窗口方法名的调用方由通用 invoke 层转到同一个原生窗口所有者。
- `QuitSequencer` 统一处理窗口关闭、应用退出、系统退出和更新重启。重复请求复用同一次
  草稿准备和服务收尾；后台关闭策略检查期间到达的应用退出会把当前事务升级为真实退出。
  恢复编辑和草稿失败对话框结束前，准备事务持续持有所有权；期间到达的退出请求在所有权
  释放后继续执行。
- 服务开始 shutdown 时立即发布 `stopping`，公开的 `ready` 同时变为 false；新的业务调用
  返回“正在退出”，shutdown/status 仍通过当前服务会话完成。
- 服务进入 `stopping` 后，共同的 follower 所有者停止本地和远程订阅，并阻止晚到的加载
  回调重新订阅；旧世代的在途响应不能更新已经显示的聊天内容，也不能再发送清理 RPC。
- transcript 故障以受限字段记录 stage、reason、错误类型、传输类型、revision、commit、活动 attempt
  数、失败次数、持续时间、服务阶段和服务 generation。相同故障每 30 秒最多输出一次可见
  汇总，原因变化立即记录。只有增量跟随成功才记录恢复，重新安装快照不会清空故障计数和
  持续时间，故障期间的重复快照也不再写入 breadcrumb。宿主缺少诊断能力、同步抛错或异步
  拒绝都不影响同步恢复。renderer → shell 端点拒绝自由文本、未知字段、超过 2 KiB
  的载荷、不可信发送者，以及每秒超过十条的已接收事件。

长期诊断以轮转的 `shell.log` 为准。启动日志包含 shell 版本、channel、commit、PID 和运行
generation；握手成功日志包含服务构建、PID 和 generation；退出日志包含 shutdown requestId、
触发原因、草稿保存耗时、服务收尾耗时、总耗时和结果。

`%APPDATA%\reasonix\diagnostics\lifecycle\` 中的文件是临时生命周期证据。正常退出会删除
当前运行对应的文件；策略关闭诊断或使用开发构建时也可能不创建文件。因此正常退出后目录
为空属于预期行为。退出后的调查应采集 `%APPDATA%\reasonix\logs\shell.log` 和 `service.log`。

## 检查后修复验证

检查基线：`a40c1eca5f9c6849ee82c5e027798085d9692519`；修复提交：`fee5c199b9838e2f1c606f962327f5fb5404a67f`。
以下本地 macOS 检查已通过：

| 目录 | 命令与证据 |
| --- | --- |
| `desktop/electron` | `pnpm typecheck`、`pnpm test`（235 项）、`pnpm build`；最后的日志顺序调整再次通过全部 22 项 lifecycle 测试 |
| `desktop/frontend` | `pnpm typecheck`、`pnpm test:typecheck`、`pnpm build`，资源预算未放宽 |
| `desktop/frontend` | `pnpm test:transcript`、`pnpm test:app-lifecycle`、`pnpm test:remote` |
| `desktop/frontend` | `pnpm exec tsx src/__tests__/transcript-follow-client.test.ts`（22 项）、`pnpm exec tsx src/__tests__/transcript-session-follower.test.ts`（37 项） |
| `desktop/frontend` | `node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/remote-session-history-prime.test.tsx`（14 项） |
| `desktop/frontend` | `pnpm test:app-browser`，覆盖 Chromium 生命周期、导航、运行时和通知行为 |
| 仓库 | `git diff --check` |

新增回归通过 deferred Promise 和假时钟控制草稿失败对话框、恢复编辑、迟到基线、延迟加载、
拒绝和重试时序；同时覆盖本地与远程 follower、持续增量失败时的诊断限频、诊断能力缺失或
失败，以及按世代区分的订阅清理。未修改 Go 源码。Windows 原生正式包验收仍未执行。

## Windows 正式包验收

使用同一个最终安装包或便携包，隔离 `REASONIX_HOME`，仅使用包内服务，不启用开发握手绕过。

1. 确认严格 shell/service 握手成功，并完成真实 renderer `Version` 调用。
2. 交叉执行连续标题栏关闭、Alt+F4、托盘退出、后台隐藏和重新打开；使用测试夹具暂停草稿
   准备后重复这些动作。
3. 在存在未发送草稿、活动会话和多个标签时重复测试，重启后核对所有持久状态。
4. 使用确定性夹具暂停服务收尾，再次重复关闭和退出；确认只有一个 requestId 和一次收尾事务。
5. 确认 shell 与服务均退出，无崩溃覆盖层、未处理 rejection 或残留进程。
6. 分别注入本地与远程 transcript 故障；确认 `shell.log` 和复制的崩溃文本包含稳定 stage/reason，
   并能通过构建 commit、服务 generation 和绝对发生时间关联。
7. 确认正常退出后 lifecycle 目录可以为空，而轮转日志仍然保留。

在 Windows 原生矩阵完成前，状态只能写为“实现和本地测试完成”，不能写为“Windows 问题验证通过”。

## 独立迁移调查交接

历史归属迁移不属于本次改动。最小脱敏模型是：3 个会话登记在 workspace **A**，一个待处理
导入操作指向 workspace **B**。后续调查需要对照 workspace registry 中的 membership/lifecycle
记录，以及 migration ledger 中的来源映射、目标、操作 revision 和完成回执。在目标归属得到
证明前，不移动、复制或删除会话及待处理操作。
