# Manual conversation creation and upgraded data

This is a forward fix for the draft-first behavior introduced by #10469. It is
intended for the next incremented Desktop release, with a matching Electron
shell and Go service. It does not replace historical tags or downgrade storage.

## User behavior

Each explicit local New Conversation action reserves its own operation, Session
and Topic identities, persists the canonical session, registers it with the
workspace, and starts its runtime. Retrying an operation keeps those identities.
Navigation completion is independent: background creation cannot replace a newer
selection. A failed operation remains visible for retry; existing empty sessions
are not reused. Remote, IM, automation and worktree creation keep their own APIs.

The last closed or archived conversation leaves an empty welcome page. Choose a
project and explicitly create a conversation to get an editor. Closing is not
deletion. Archiving retains unsent input; permanent deletion cleans its input
only after the canonical purge has established the tombstone.

## Input and recovery

`desktop/session-ui-v1.sqlite` holds creation operations, input revisions,
submission associations and conflict copies. Chat history remains in Session v5;
workspace ownership remains in registry v3. No UI operation identity or input
storage metadata is inserted into provider messages.

Text, attachment references, paste blocks, invocations, selected text and history
references are saved by full SessionRef, with a 250 ms debounce and revision CAS.
Pending Goal mode is saved with input; effective model and permission settings
remain owned by the formal session. Image previews and browser File objects are
not serialized. Missing files remain visible for repair. Delayed work retains
its original session target. Conflicting versions are kept and can be inspected.

A normal application exit waits for input and attachment work to persist. Save
failure retains the window. A crash can only recover the last acknowledged
database revision. Sending persists an association with the source revision and
submission identity first. Input is cleared after acceptance; a lost reply is
checked against durable receipts. Reupgrade compares both accepted submission
identities and canonical user-message history, including old/imported writes
without a SubmissionID. Assistant streaming and model-setting changes do not
invalidate unsent input. An absent ordinary user-message row is never
proof that a shell or management command did not execute. Unknown results remain
recoverable and are not automatically sent again.

## Old data

Workspace drafts are retired, including drafts with unsent content. Desktop no
longer reads, migrates, restores, reconciles or advertises these records during
startup or navigation. The historical `desktop/drafts-v1.sqlite` file is left
untouched; there is no recovery notice or automatic conversion into formal
conversations. Already-created formal conversations and their histories remain
ordinary sessions. Formal input persistence in `session-ui-v1.sqlite` continues
to support saving, switching, restart and the normal exit barrier.

Before a schema 1–3 draft database is upgraded, SQLite `VACUUM INTO` creates a
consistent `.pre-v4.sqlite` backup including committed WAL content. Existing v4
data is not destructively migrated. Unknown future formats are left untouched.

Automatic empty-session batch registration and retry are disabled. The existing
`legacy-empty-session-cleanup-v1.json` remains readable; committed archives can
be reconciled, and trash can be manually restored. Pending candidates stay put.

| Route | Compatibility |
| --- | --- |
| 1.38.3–1.38.8 → this release | Tagged JSONL/event histories and revision-1 framed stores remain explicit import sources |
| 1.38.9 → this release | Existing migration chain upgrades registry v1 and reads history |
| 1.38.10 / 1.38.11 / subsequent data → this release | Registry v3, Session v5 and existing draft records retained |
| This release → 1.38.10 | Registry v3 remains readable, but its revision-2 session reader rejects revision 3 introduced by #10545; whole-session downgrade is unsupported |
| This release → 1.38.11 | Tested tagged reader/writer accepts the shared session format; the old binary ignores the independent UI input file |
| Reupgrade | UI input is retained; changed accepted history requires review before sending |
| Downgrade to 1.38.9 | Unsupported: its registry reader cannot read v3 |
| Unknown future format | Refuse writes; preserve the file |

Older binaries keep their own cleanup behavior. Never run different versions
against one directory without the existing instance coordination and write locks.
Recovery does not require deleting a data directory, clearing caches or reinstalling.

## 中文说明

本次通过新的递增版本回滚 #10469 的用户行为，保留 Session v5、工作区注册表 v3
及后续归档、恢复、历史加载修复，不是安装旧版。

每次点击本地“新建会话”立即创建独立正式会话并启动运行时，不复用已有空会话。
首次发送使用该会话。关闭或归档最后一个会话显示欢迎页，不自动补建。
创建失败保留操作和原身份，可重试；后台完成不抢占最新页面。

正式会话的未发送内容按 SessionRef 保存到独立 SQLite 文件，支持切换和重启恢复。
正常退出等待保存，保存失败保留窗口；异常退出只恢复已经确认落盘的版本。
跨窗口冲突保留双方副本；发送结果未知时保留恢复状态，不自动重发。
图片预览重新生成，用户原始附件不因丢弃输入而删除。

旧草稿机制已停用，包括有未发送内容的草稿。启动和导航不再读取、迁移、恢复或
协调旧草稿，不显示项目标签或恢复提示，也不自动转成正式会话。历史
`desktop/drafts-v1.sqlite` 文件留在原处，不主动删除或改写。
已经创建的正式会话及其聊天记录继续保留；正式会话的新版输入保存、切换恢复、
重启恢复和正常退出前保存不受影响。

再次升级时，同时比较接收回执和正式用户消息历史，覆盖没有 SubmissionID 的旧路径
或导入写入。历史推进后保留原输入并要求核实；模型设置变化、助手流式输出不会单独
触发此限制。旧版归档的会话继续保持归档，永久删除后的迟到输入保存不能将会话复活。

历史会话夹具来自 1.38.3–1.38.11 九个发布标签的真实存储实现；注册表覆盖 1.38.9 的 v1
及 1.38.10、1.38.11 的 v3。#10469、#10572 中间构建及 1.38.11 的旧草稿覆盖未发送、
仅设置、已转换、全部提交阶段、冻结设置、缺失附件及冲突副本。
夹具只含合成数据，生成脚本不读取用户数据目录。

停用历史空会话自动清理；已在回收站的内容保持原位，可以手动恢复。升级不改写为
旧存储格式。不支持降级到 1.38.9；1.38.10 虽然可读注册表 v3，但不能读取 #10545
引入的会话物理格式 revision 3，不能作为整套会话的降级路线。1.38.11 的真实存储读写器
已通过往返验证；它保留但不能读写新增输入文件。再次升级后，历史已推进的输入需要核实，
不自动发送。完整的版本、PR 和新增测试证据见 [兼容测试报告](testing/manual-session-compatibility.md)。

## Verification scope

### Creation without waiting for runtime initialization / 无需等待运行时的新建流程

Each explicit New click owns an independent operation and session ID. Replaying
that operation keeps the same session. Workspace reservation and publication
resolve the current directory owner inside the registry transaction, so a
workspace-ID merge does not invalidate the creation journal. Replay validates
the original directory before registering anything; it cannot move the request
to another project or revive an archived/deleted session.

Once the session and its tab exist, the UI opens that tab while its original
runtime builder continues. Input is saved against the new session; sending stays
disabled until readiness. Completion never selects a tab, so a later navigation
wins. Temporarily unavailable project folders retry in the existing scheduler.
Old `target_changed` failures are reconciled once; real conflicts receive a
specific terminal result. Diagnostic export lives under Details.

每次明确点击“新建”都有独立操作和会话 ID；同一次请求的重试复用原身份。
工作区预约、挂载在注册表事务内解析目录当前所属的工作区，自动处理 ID 合并。
重试不会注册无关项目、转移目录或复活已经归档、删除的会话。
会话与标签页建立后即可进入输入界面，文字保存到新会话；原运行时继续初始化，
准备好后才允许发送。后台完成不改变选择，连续点击时最后一次导航生效。
目录暂不可用时自动等待；旧版 `target_changed` 失败会自动协调一次。
无法协调的真实冲突给出明确结果，诊断导出收在“详情”中。

Creation feedback is scoped to the selected navigation intent and shown next to
the composer. The explicit creation observer also supplies UI progress; there is
no second global recovery poll or in-memory failure catalog. Reopening a pending
session discovers only its own operation and then observes that operation.
Preparing has no actions, readiness removes the notice, and a terminal failure
has one primary action (retry the same operation, choose a project, or create a
new session). Closing a notice only hides it for the current selection; it never
cancels host recovery or deletes input. New-session/project actions do not move
input between sessions. Diagnostic export is available under Details on errors.

创建提示只属于当前导航选择，并显示在输入框旁。创建请求的观察结果直接用于显示，
不再另设全局恢复轮询或内存失败列表；重新打开未完成会话时，只查找并观察它自己的
创建操作。准备中没有操作按钮，就绪后提示消失；失败只提供一个主要操作：复用原操作
重试、选择项目或新建会话。关闭提示仅在当前选择下隐藏提示，不取消后台恢复、不删除
输入；新建或选择项目不跨会话搬移输入。诊断导出只在错误的“详情”中提供。

| Contract / 契约 | Compatibility / 兼容行为 |
| --- | --- |
| Creation journal / 创建记录 | Phases, identities and schema unchanged; unknown fields preserved. 阶段、身份和格式不变，保留未知字段。 |
| `surfaceReady` RPC hint | Optional observation, never stored; absent means wait for ready on older hosts. 可选且不持久化，旧服务缺失时等待 ready。 |
| `waiting_workspace` progress | Transient only; unknown-status fallback remains available to older clients. 仅进程内状态，旧客户端使用未知状态兜底。 |

Regressions cover workspace merge between reservation and publication, restart
with a stale ID, lost-response replay, archived-session protection, automatic
directory retry, input saved before runtime readiness, and out-of-order frontend
navigation. 回归覆盖预约与挂载间合并、旧 ID 重启恢复、响应丢失重试、归档保护、
目录自动重试、运行时就绪前输入保存及前端乱序导航。

Release qualification must include isolated packaged macOS, Windows and Linux
application runs. Development-server tests cannot replace SQLite file-release,
normal exit, crash/restart, attachment recovery and shell/service handshake checks.
The implementation does not itself publish a release.

### Local qualification — 2026-09-22

- Historical fixtures and the real tagged registry readers/writers passed:
  1.38.9 rejects the upgraded v3 file; 1.38.10/1.38.11 can read and append to it,
  and the current reader retains that append on reupgrade.
  This is a registry-only boundary: the actual 1.38.10 session reader rejects
  physical revision 3; the 1.38.11 session writer can append and reupgrade
  correctly marks previous input for review. All nine releases have real
  historical session fixtures, source digests and repeated upgrade checks;
  see the [extended compatibility report](testing/manual-session-compatibility.md).
- Root Go packages passed across the full run and the separate full control
  package run. The first full run hit the aggregate ten-minute control-package
  limit; its current test had run for zero seconds. The complete control rerun
  passed in 363 seconds with an explicit 20-minute local ceiling.
- Desktop subpackages passed. All main-package tests were covered by the
  non-overlapping A–D, E–I, J–O, P–R, S–Z/Example/Fuzz groups; each passed.
  Initial larger groups hit the aggregate timeout, not a stuck individual test.
  Creation, restart, tagged upgrades and composer compatibility also passed
  with the race detector; SQLite CAS and draft backup tests passed separately.
  The new database uses the shared SQLite URI builder; drive/UNC encoding tests
  and database reopen with Chinese, space, `#` and `%` path characters passed.
- Frontend production build, bundle budgets, source and test typechecks,
  lifecycle/transcript regressions, input persistence and inbox recovery passed.
  The browser test exercises three actual New clicks and verifies three durable
  identities, formal input persistence and no new legacy draft.
- The ad-hoc signed macOS arm64 `v1.38.12-preview.1` package passed the native
  script with development/service overrides unset: matching shell/host RPC,
  three ready runtimes, duplicate-operation identity, normal exit, SQLite reopen,
  image reference/preview recovery, shell crash with orphan-service lease release,
  archive/restart, and an actionable empty welcome without an editor.
- Generated inventory, repository lint and whitespace checks passed. No lint
  baseline or bundle budget was increased.

Reproducible entry points: `scripts/generate-manual-session-fixtures.py`,
`desktop/frontend/bench/manual-session-creation.mjs`, and
`desktop/electron/scripts/manual-session-smoke.mjs <packaged executable>`.
All fixtures and native runs use isolated synthetic data.

**Remaining release evidence:** packaged Windows and Linux runs are not available
from this macOS validation. The complete old desktop applications were not
installed for downgrade testing: the reverse path uses their actual tagged
storage implementations. No official release, notarization, remote CI run or
historical tag modification was performed.

本地验证已覆盖历史存储实现往返、Go 测试集合及关键 race、前端生命周期和输入恢复、
生产构建与体积检查、macOS arm64 实际打包应用。大测试组曾因累计十分钟上限退出，
已通过不重叠分组或完整包补跑完成覆盖，没有跳过失败用例或提高仓库预算。

Windows、Linux 打包验证仍缺少实际平台证据；降级往返使用历史标签的真实存储实现，
未安装完整旧客户端。测试未读取或改写用户原数据，没有正式发布、修改历史标签或
执行远程 CI。这些边界不能用浏览器通过代替。
