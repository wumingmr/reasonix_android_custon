# Session termination / 暂停与恢复机制

## Contract / 契约

Reasonix keeps the Go runtime, v4 physical log, SQLite projections and Desktop
Stop RPC. Cancellation now stages explicit message mutations and accepts them
with the terminal facts in one atomic commit. `history/replace` remains available
for import and intentional history edits, but is not the cancellation commit.
`model/context-replace` intentionally retains the exact provider workset.

保留 Go 运行时、v4 物理日志、SQLite 投影和 Desktop Stop RPC。暂停清理先生成明确
消息变更，再与回合终结事实一起原子提交；导入和显式编辑仍可使用 `history/replace`。
暂停不替换整段展示历史，`model/context-replace` 继续保存精确模型工作集。

| Owner / 所有者 | Responsibility / 职责 |
| --- | --- |
| `internal/control/termination_policy.go` | Pure legacy cleanup policy, including fallback input, complete tool pairs, LocalOnly and compaction. / 无副作用的既有清理策略，包括真实输入兜底、完整工具对、LocalOnly 和摘要。 |
| `internal/control/termination.go` | Detached plan, turn identity, atomic terminal acceptance and bounded Flush. / 独立计划快照、回合身份、原子终结和有界持久化。 |
| `internal/session/recovery.go` | Deterministic tool/interaction/step closure shared with restart recovery; never reruns a tool. / 运行终结与重启共享确定性收尾，不重跑工具。 |
| `internal/session/transcript_metadata.go` | Bounded input previews and stable identities survive externalized history; retractions repair catalog and reply anchors. / 历史外置后保留输入身份及截断预览，撤回同步修复目录及回复锚点。 |

The input-preview metadata grows with input identities, not message bodies.
History pages and recent-message windows remain bounded. Synthetic cleanup also
tracks the active turn's committed IDs, so compaction cannot hide messages that
must be retracted; absence from the model working set does not authorize deleting
older display history. Unresolved side-effect records use the existing
`Session.Replace` retention policy before IDs are assigned and events prepared.

输入预览元数据随输入身份数量增长，不保留完整消息正文；分页和最近消息窗口保持
有界。合成回合还按当前回合已提交的 ID 撤回，避免压缩移走模型上下文后漏删，也
不会因旧消息不在模型窗口中而删除旧历史。未决副作用记录在分配稳定 ID、生成事件
之前应用已有 `Session.Replace` 保留规则，避免提交后产生额外临时消息。

## Events and ordering / 事件与顺序

`message/retract` is required. Its strict payload is
`{"messageIds":["stable-id"],"reason":"diagnostic-only"}`. IDs must be nonempty,
trimmed and unique. Writers sort cleanup IDs. A missing/already retracted ID is a
projection no-op. Later upsert restores the ID at a higher version; raw events and
content blobs are retained. Unknown fields remain errors.

`message/retract` 为 required 事件。严格载荷如上，ID 非空、无重复且无首尾空白；
清理写入按 ID 排序。投影对不在当前视图或已撤回的 ID 不操作；同 ID 可由后续
upsert 以更高版本恢复。原始事件和正文不物理删除，未知字段仍拒绝。

Normal termination order is cancellation signal, worker exit, planning, terminal
gate, atomic acceptance, executor synchronization, independent 15-second Flush,
then completion publication. The existing cooperative cancellation grace remains
15 seconds. Within the batch the order is message changes, model context, tool /
interaction / step closure, recovery state, then turn end.

正常顺序为立即发取消信号、等待协作退出、生成计划、进入终结门闩、原子接受、
同步 executor、独立 15 秒 Flush、发布完成；协作取消宽限期仍为 15 秒。Batch 内
顺序为消息变更、模型上下文、工具／交互／步骤收尾、恢复状态、回合结束。

`turn-finalize:<turnID>` identifies the unique terminal operation. The runtime
checks execution generation and open turn while holding its commit gate. The
controller additionally checks the captured execution token. Accepted terminal
retries only flush; persistence failure keeps `recovery_required` and disarms
automatic Goal continuation. Repairs after a closed turn use a deterministic
`turn-repair:<payload hash>` operation and never add another turn end.

唯一终结键为 `turn-finalize:<turnID>`。runtime 在提交门闩内检查执行代际及开放
回合，controller 还检查执行 token；已接受终结只重试 Flush。持久化失败保持
`recovery_required` 并停止 Goal 自动续跑。已关闭回合的修复使用确定性的
`turn-repair:<载荷哈希>`，不重复追加 `turn/end`。

Watchdog uses accepted model state, captured input/prefix identities and accepted
compaction summaries. Sealing rejects late business output; it does not prove a
tool process exited. Started tools without a result remain unknown; dispatched
tools without start evidence are not-started. Existing results are not duplicated.
Stop receipts acknowledge acceptance only, and switching another session does
not wait for this session's disk cleanup. Old callbacks retain their session and
turn identity. No polling service or provider-visible diagnostic text is added.

watchdog 根据已接受模型状态、捕获的输入／前缀身份和摘要封存。封存拒绝迟到业务
输出，但不代表工具进程已退出：已启动无结果记 unknown，仅派发无启动证据记
not-started；已有结果不补写。Stop receipt 仅确认请求已接受，切换其他会话不等待
本会话磁盘清理。旧回调保留原会话／回合身份，不新增轮询或模型可见诊断文字。

## Upgrade and rollback / 升级与回退

Physical codec stays v4; new sessions use storage revision 2. Readers accept
revisions 1 and 2. A revision-1 writable open validates the log and acquires the
exclusive lease before atomically publishing and syncing revision 2. It does not
rewrite the event log. Read-only open neither upgrades nor repairs. Fault tests
cover rejected leases, invalid logs and failed atomic manifest publication.

物理 codec 仍为 v4，新会话 revision 2；新版读取 revision 1、2。旧会话可写打开
在日志验证和独占租约成立后原子同步新版 manifest，不改原日志；只读打开不升级、
不写修复。故障测试覆盖租约冲突、损坏日志和 manifest 原子发布失败。

History projection is version 8, search version 4, recovery projection version 5,
catalog metadata version 3. Old derived caches rebuild on demand. Retraction
expires MVCC rows without resetting version watermarks; changed visible-turn
ordinals get new rows so previous snapshot cursors keep their previous view.
Retracted inputs and replies disappear from current catalog/fork navigation.

历史投影版本 8、搜索版本 4、恢复投影版本 5、目录元数据版本 3；旧缓存按需重建。
撤回使 MVCC 当前行过期，不重置版本水位；变化的回合序号写新版本行，旧快照仍可读。
撤回输入与回复后，当前目录和 fork 导航同步更新。

**Do not lower manifest revisions to roll back.** An old executable cannot safely
read revision 2. A code rollback must retain revision-2 decoding and projections,
or restore a complete pre-upgrade backup into a separate location. Do not mix an
old manifest with a new event log.

**禁止降低 manifest 版本号实现降级。** 旧程序不能安全读取 revision 2。代码回退
必须保留新版解码和投影，或将完整升级前备份恢复到独立位置；不得混用旧 manifest
和新事件日志。

## Verification record / 验证记录

Baseline: `b21deef03eb859c2e923f1f6dd8a8ee1b0d4493e`, with the existing decoder and
message-version repair retained. Packages were built from the implementation
working tree before its PR commit; they are local verification artifacts.
The local verification date is 2026-09-16, macOS arm64.

基线为上述提交，保留已有解码器与消息版本修复。验证包从 PR 提交前的实现工作树
构建，仅作为本地验证产物。
本地验证日期为 2026-09-16，平台为 macOS arm64。

| Evidence / 证据 | Result / 结果 |
| --- | --- |
| Policy and HTTP bytes / 策略及 HTTP 字节 | Seven fixtures through OpenAI-compatible, Anthropic thinking/signature and Responses adapters compare expected cleanup, new cleanup and persisted reopen requests byte-for-byte. / 七组夹具经三个真实 HTTP adapter 比较原预期、新清理及重开请求，逐字节一致。 |
| Deterministic termination / 确定性终结 | Double Stop, watchdog late output, accepted-before-fsync failure, fallback input, synthetic compaction, side-effect recovery retention and zero pause history replacements pass. / 连续 Stop、watchdog 迟到输出、接受后 fsync 失败、输入兜底、合成压缩、副作用恢复记录和零暂停历史替换通过。 |
| Projections / 投影 | History/search, snapshot MVCC, ID restoration, catalog/recent, fork targets, reply repair, checkpoint and cache-free reopen covered. / 历史搜索、快照、恢复 ID、目录、最近消息、fork、回复修复、检查点和无缓存重开均覆盖。 |
| Old binary / 旧二进制 | Built from baseline: lease held prevents upgrade; after upgrade warm history, cold read and write return unsupported storage version. / 从基线编译：旧租约阻止升级；升级后旧版热缓存、冷读、写入均报不支持版本。 |
| Frontend / 前端 | typecheck, transcript tests and Chromium transcript browser suite passed; no render errors, duplicate nodes or anchor drift. / 类型检查、transcript 单测和 Chromium 浏览器套件通过。 |
| Full root module / 根模块全量 | Latest full invocation passed (control 208.632s, session 113.589s, agent 161.773s, CLI 130.414s). A previous full run failed at the known WSL clipboard timeout; its isolated pass was not counted as a full-suite pass. The final reply-anchor refinement also passed its focused session suite. / 最近全量通过，耗时如左；此前一轮已知 WSL 剪贴板超时及单独重跑记录仍保留，不以单独通过代替全量。最后的回复锚点细化另通过 session 定向套件。 |
| Desktop module / Desktop 模块 | Independent `go test ./...` passed (277.720s); cancellation/cutover/recovery race suite passed (49.662s). The reversed compatibility-message order found in earlier runs was fixed before these passes. / 独立模块全量与相关 race 通过；此前发现的兼容消息反序问题已修复。 |
| Runtime races / 运行时并发 | session/control targeted race passed; the final watchdog-summary and termination race suite passed (11.918s). / session/control 定向 race 通过，最终 watchdog 摘要及终结 race 通过。 |
| Native macOS / macOS 原生 | Production-mode local ZIP exercised renderer→service Stop, old/new session switches, return, continuation, restart and clean exit using a local HTTP fixture. No real vendor credentials used. / 生产模式本地 ZIP 通过 renderer→service 暂停、旧／新会话切换、返回、继续、重启和正常退出，使用本地 HTTP 夹具，无真实厂商凭据。 |
| Windows ARM64 / Windows ARM64 | Passed on the local Parallels Windows 11 ARM64 VM (10.0.26200): native session tests 37.118s, control tests 23.632s, six Desktop regressions 7.630s. The complete portable ZIP passed production-mode renderer/service Stop → old/new conversation → return → continue → restart, unique transcript record IDs, visible reopened content and normal shell/service exit. / 已在本机 Parallels Win11 ARM64（10.0.26200）通过原生 session、control 和六个 Desktop 定向回归；完整便携 ZIP 通过生产模式暂停→旧／新会话→返回→继续→重启，记录 ID 无重复、重开内容可见，壳及服务正常退出。 |

The local packages are verification evidence, not signed/notarized production
releases. Native runtime reproduction is now covered on macOS ARM64 and Windows
11 ARM64; Windows x64 hardware was not exercised. Check the task's final result
for module runs and package identities; earlier interrupted/failed attempts are
not counted as passes.

本地包仅作为验证证据，不是正式签名／公证发布。现已覆盖 macOS ARM64 与
Windows 11 ARM64 的原生运行复现，未测试 Windows x64 硬件。最终模块运行结果
及包身份以任务最终结果为准；中途失败或中断不算通过。

Final tested ZIP: `dist/Reasonix-darwin-arm64.zip`, version
`v0.0.0-termination-local`, source revision `b21deef…+dirty`, SHA-256
`a3640d502d784194a95e1b96c27eb0ae9f39212eead5698b638e3cb9a6aecd55`.
It was extracted into a fresh directory and passed strict deep codesign
verification before the production-mode renderer/service scenario. The DMG was
built, but this record qualifies the ZIP, not a separate DMG install run.

最终验证对象为上述版本和哈希的 ZIP（包含未提交改动），重新解压到干净目录，
通过严格深度签名结构检查后执行生产模式 renderer/service 流程。DMG 已构建，
但没有把 ZIP 验证冒充为另一次 DMG 安装验证。

Windows follow-up (2026-09-16): all changed source files were SHA-256 checked
against the macOS working tree before the VM build. The repository's complete
`desktop-build.sh windows/arm64` flow produced both the NSIS installer and the
portable archive. The initial NSIS permission failure was resolved by copying
the system-owned toolkit into the dedicated test directory; product code and
the original toolkit permissions were unchanged.

Windows 补验（2026-09-16）：构建前逐一比对所有改动文件的 SHA-256，确认与 macOS
工作树一致。在虚拟机内执行仓库完整 Windows ARM64 打包流程，生成 NSIS 安装器
和便携 ZIP。最初 NSIS 工具目录权限不足，通过复制工具到专用测试目录解决，
未修改产品代码或原工具目录权限。

Tested archive: `dist/Reasonix-windows-arm64.zip`, version
`v0.0.0-termination-local`, SHA-256
`859f720c74c59c86da62edc4a19bbf34aa166db63e3da0264771337ede253637`.
The archive passed structural verification (463 entries), was freshly extracted
to a dedicated disposable directory, and ran its bundled ARM64 Electron
and Go binaries with no development or service-path overrides. Both the legacy
submit RPC and the display/submission-ID RPC scenario passed; the latter also
waited for the startup overlay to leave and checked visible restored content.
The NSIS installer was built, but an install/uninstall cycle was not performed.

实测便携包版本及哈希如上，463 个包条目的结构校验通过，重新解压后直接运行包内
ARM64 Electron 和 Go 服务，未使用开发模式或外部服务路径。旧提交 RPC 与带展示
文本／提交 ID 的 RPC 两条流程均通过，后者还等待启动遮罩退出并确认恢复内容已
渲染。NSIS 安装器已构建，但未执行安装／卸载周期。

Local evidence is retained separately: Windows build, session/control,
Desktop and native-run logs, plus continued/restarted window screenshots. These
use disposable homes and a loopback HTTP fixture, not user session data or vendor
credentials. No production release is represented by this record.

本地另行保留构建、session/control、Desktop、原生运行日志以及继续和
重启后的窗口截图。全部使用临时数据目录和回环 HTTP 夹具，没有使用用户会话数据
或厂商凭据。本记录不代表正式发布。

PR review follow-up: restoring a retracted input in a repair batch without a
live turn now preserves its original turn ownership through checkpoint and
full-log reopen. The regression first failed with a hidden original turn and
then passed after retaining an identity-only tombstone. This follow-up has
focused session/control, race and lint evidence; the package hashes above
predate it and must not be treated as rebuilt final-head packages.

PR 评审补充：不带活动回合的修复批次恢复已撤回输入时，现在保留原回合归属，
覆盖检查点及全量日志重开。回归先复现原回合仍被隐藏，再以仅含身份的撤回记录
修复。此补充有 session/control 定向、race 和 lint 证据；上方包哈希早于此修复，
不能作为重新构建后的最终提交包证据。

Integration with the ProjectTree/session-recovery changes on `main-v2` retains
their cold-history preparation, complete-batch indexing and authored-preview
rules. The bounded catalog reducer now shares input metadata with the execution
projection, including retracted/restored ownership, while discarding message
bodies and full turn boundaries. All four derived projection versions advance
beyond both branches so an older cache cannot mask either side's changes.

与主分支 ProjectTree／会话恢复修复整合后，保留冷历史准备、完整批次索引和用户
原文预览规则。有界目录重建器共享执行投影的输入元数据及撤回／恢复归属，同时
丢弃消息正文和完整回合边界。四类派生投影版本均超过双方原版本，确保旧缓存不会
掩盖任一方的修复。
