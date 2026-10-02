# Purged topic retirement / 永久删除后的条目退役

After archive, the canonical session still owns its sidebar topic. Previously,
emptying the trash removed that membership and presentation but left the topic
metadata behind. The sidebar then treated each leftover topic as a new empty
conversation. Opening it could create another session.

归档后，正式会话仍持有侧栏条目的归属信息。此前清空回收站会移除成员关系和展示信息，
却留下条目索引，导致侧栏将每个残留条目当成新的空会话；再次点击还可能创建新会话。

Purge completion now retains the topic ID and workspace ID in its existing
operation receipt before removing live presentation. Sidebar metadata reads
exclude topics owned only by deleted sessions; opening a stale placeholder also
rejects that identity. Surviving sessions and pending creations sharing the topic
keep it available. No conversation content is retained by this change.

现在永久删除会在现有操作记录中保留条目 ID 和工作区 ID，再移除正式展示信息。
侧栏排除仅属于已删除会话的残留条目，打开残留空条目也会被拒绝。
共享该条目的存活会话或待完成创建仍可正常显示。本次修改不会额外保留会话内容。

## Compatibility / 兼容性

| Field or format / 字段或格式 | Old-data behavior / 旧数据 | New-reader behavior / 新版读取 | Previous-reader behavior / 旧版读取 | Conclusion / 结论 |
| --- | --- | --- | --- | --- |
| Registry v3 `Operation.WorkspaceID`, `Operation.Presentation.TopicID` | Fields already exist; old purge receipts may omit them / 字段已存在，旧删除记录可能缺失 | Retains minimal ownership on completion; reads exact committed operation evidence / 完成时保留最小归属，读取已提交记录中的精确证据 | Previous v3 code understands these fields but lacks the sidebar fix / 原 v3 能解析字段，但不具备侧栏修复 | No schema or RPC change; downgrade can reproduce the old UI bug / 不变更结构版本或 RPC，降级仍可能复现旧问题 |
| Deleted `desktop-manual-<hash>` session IDs | Manual creation persisted the same 32-hex suffix in `manual-<hash>` topic IDs / 手动创建的会话与条目使用相同的 32 位十六进制后缀 | Recovers exact ownership when old purge omitted presentation / 旧删除记录丢失展示信息时恢复精确归属 | Existing ID format unchanged / 不改变已有 ID | Old leftovers are hidden without rewriting user data / 无需重写用户数据即可隐藏旧残留 |
| Registry versions before v3 | Existing v3 migration boundary applies / 沿用已有 v3 迁移边界 | Existing upgrade and unknown-field preservation remain in force / 沿用升级与未知字段保留 | Existing older writers reject v3 / 更旧的写入方按原约定拒绝 v3 | No new downgrade boundary / 未新增降级边界 |

Old records without exact topic ownership are not guessed from titles or age.
Real sessions already created by earlier stale clicks are not automatically
deleted. This prevents removal of unrelated user-created empty conversations.

缺乏精确归属证据的旧记录不会按标题或时间猜测处理；此前点击残留条目已经创建出的
真实会话不会自动删除，以免误删用户主动创建的空会话。

## Verification / 验证

- Reproduce four archived sessions followed by bulk trash purge in global and
  project workspaces; assert no replacement rows or sessions after repeated
  stale clicks, catalog warm-up, and cold restart with previous-writer records.
  在全局及项目工作区复现四个会话归档后批量清空，验证反复点击、索引就绪、旧记录重启
  均不会重新产生条目或会话。
- Preserve a surviving session sharing the topic and unrelated empty topics;
  retire the topic after its final owner is purged.
  保留共享条目的存活会话和无关空条目，最后一个所属会话删除后再退役。
- Verify purge receipt persistence, existing interrupted-purge recovery,
  compatibility, and ordinary activation/listing with focused Go race tests.
  验证删除记录持久化、中断恢复、兼容性，并对相关列表与打开流程执行 Go 竞态检测测试。
