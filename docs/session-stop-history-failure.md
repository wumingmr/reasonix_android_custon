# History loading fails after Stop / 暂停后历史加载失败

This document records the original reader repair baseline. The subsequent
append-only termination and revision-2 migration are documented in
[Session termination](session-termination.md); its upgrade rules supersede the
baseline compatibility table below.

本文记录最初的读取器修复基线。后续追加式终结和 revision 2 升级见
[暂停与恢复机制](session-termination.md)，升级兼容规则以新文档为准。

## Confirmed mechanism / 已确认机制

Stopping a turn can call `Controller.replaceSessionAfterCancel`, which records
`history/replace` with the existing message IDs and `reason` metadata. Opening a
session subsequently reads that event through the history index. Two independent
reader defects made a valid durable transcript unreadable:

1. Execution/recovery accepted `reason` and `sourceSequences`, but the history
   and search decoders only declared `messages`. Strict decoding rejected the
   valid event with `json: unknown field "reason"`. Legacy imports had the same
   discrepancy for `source`, `goal`, `modelRef`, and `modelIdentity`.
2. History replacement retired previous message rows but reset their version
   counters. Reusing the same ID inserted version 1 again and failed SQLite's
   `(message_id, version)` primary key. Both incremental indexing and rebuilding
   from the log failed. Loading only current-row versions also broke restoration
   of a message removed by an earlier replacement.

暂停回合可能调用 `Controller.replaceSessionAfterCancel`，写入保留原消息 ID、
附带 `reason` 的 `history/replace` 事件。随后打开会话会通过历史索引读取此事件。
两个独立的读取缺陷使合法记录无法显示：

1. 执行和恢复层接受 `reason`、`sourceSequences`，历史和搜索解码器却仅声明
   `messages`，严格解码直接报 `json: unknown field "reason"`。旧会话导入的
   `source`、`goal`、`modelRef`、`modelIdentity` 也存在同类字段不一致。
2. 历史替换保留旧数据库行，却把版本计数清零；相同 ID 再写版本 1，触发
   `(message_id, version)` 主键冲突，增量更新和完整重建均失败。另外，只读取
   当前有效行的版本，也会让先删除、后恢复的消息发生冲突。

The fix shares the existing event schemas across execution, recovery, history,
and search. History versions retain their high-water marks across replacement
and are restored from all indexed versions, including retired identities.
Unknown fields and missing/null message lists still fail validation.

修复复用已有事件字段定义，使执行、恢复、历史和搜索一致；替换时保留消息版本
水位，增量读取时恢复全部身份的最大版本，包括已退出当前历史的消息。
未知字段、缺失或为 null 的消息列表仍会被拒绝。

## Evidence and limits / 证据与边界

- Base checkout: `b21deef03eb859c2e923f1f6dd8a8ee1b0d4493e`.
- Submitted diagnostics identify preview build `6e4617121fd4`. The real
  Stop → switch away → read sequence fails on that exact unmodified production
  code with `unknown field "reason"`; the same regression passes with this fix.
- The recorder contains 1,521 events over 120,005 ms, including three
  `navigation.settle` / `data-failed` results and one `paint-ready` success.
  It contains no backend exception text, so it cannot independently establish
  the failing event or session for every reported incident.
- The submitted report's index/log watermark differences are observations,
  not proof that every cache must update at each turn end. These indexes are
  derived data and can legitimately lag. The confirmed decoder/primary-key
  failures explain why a subsequent index read can repeatedly fail to catch up.
- The v5 workspace registry owns session membership; fewer records in legacy
  topic tables do not by themselves prove missing conversations. Lease warnings
  and concurrent-version damage are not established causes by these artifacts.
- The older base also treats history `preparing` as a failure; the submitted
  preview already has preparation waiting. No frontend retry change is included
  in this repair of the durable event readers.

当前基线为上述 `b21deef…`。在诊断指定的原始预览版 `6e4617121fd4` 上，真实
“暂停 → 切走 → 读取”回归同样报 `unknown field "reason"`；应用本修复后通过。
录制数据有三次 `data-failed`、一次 `paint-ready`，但没有后端异常内容，不能
据此断言所有用户每一次失败都只有此原因。报告中的索引落后可作为现象，不能
单独证明每回合结束时必须同步所有缓存。旧 topic 数量也不能替代 v5 workspace
注册表判断会话是否丢失；租约告警、不同版本混用尚无直接因果证据。
较老基线的 `preparing` 误报在用户预览版中已有等待处理，本修复未叠加前端重试。

## Compatibility / 兼容性

| Surface / 范围 | Behavior / 行为 |
| --- | --- |
| Durable events / 持久事件 | Existing fields and bytes unchanged; readers accept the already valid schema. / 字段与写入字节不变，读取已有合法格式。 |
| SQLite history / 历史缓存 | Same schema and primary key; monotonically increasing versions. Failed index transactions roll back and can be retried by the fixed reader. / 表结构及主键不变，版本递增，失败事务回滚后可由修复版继续读取。 |
| Earlier readers / 旧读取器 | Still contain these defects; a read fix does not repair an old executable. / 旧程序仍有本缺陷，不能靠新版读取修复旧程序。 |
| Provider and RPC / 模型与接口 | No prompt, provider payload, RPC shape, or session identity change. / 不改提示词、模型请求、RPC 结构或会话身份。 |

All reproductions use disposable sessions. No submitted user data was modified.
Tests cover repeated rewrites, removed/restored identities, search, imported
metadata, cached restart, rebuild after restart, desktop page/window/compatibility
readers, and Stop → switch → return → send. Subsequent Windows ARM64 packaged
execution is recorded in [Session termination](session-termination.md). A retest
of the reporters' original session logs remains external verification.

复现全部使用临时会话，未修改用户资料。回归覆盖重复重写、删除后恢复消息、
搜索、导入元数据、保留缓存重启、重启后重建索引、Desktop 三种历史读取接口，
以及暂停后切走、切回、再发送。后续 Windows ARM64 包运行验证见暂停与恢复文档；
用户原始会话日志复测仍待外部验证。

## Local validation / 本地验证

- Passed: session/control regression tests on both the base checkout and the
  submitted preview commit with the patch; focused Go race tests; desktop
  session switch/cancel/history reader tests; `pnpm test:transcript`;
  `pnpm test:transcript-browser` (Chromium); repository lint and diff checks.
- `go test ./...` passed the session, control, and other reported packages except
  `internal/cli`: three WSL clipboard fixture subprocesses exceeded their 5-second
  deadlines. The exact clipboard suite passed independently on both checkouts.
  The aggregate run is therefore not reported as green; no timeout or test was
  relaxed for this change.

两个代码基线应用补丁后的定向回归、Go 竞态测试、Desktop 会话切换/取消/历史读取、
前端 transcript、Chromium 浏览器测试及仓库静态检查均通过。全仓 Go 检查中，
`internal/cli` 的三个 WSL 剪贴板子进程超过 5 秒限制；该组测试在两个基线独立
运行均通过，因此保留全仓首次检查未全绿的记录，未放宽超时或测试。
