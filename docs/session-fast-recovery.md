# Fast session recovery and independent history

Reasonix v4 event frames and ContentStore remain the only authoritative
session data. Normal open paths use bounded, rebuildable projections and do
not validate or materialize the complete transcript.

## Stored projections

Each session publishes the following independent local artifacts:

| Artifact | Purpose | Failure effect |
| --- | --- | --- |
| `storage.identity.json` | Binds cursors and projections to one physical log generation | Invalidates all derived data |
| `.recovery-cache/<session>/recovery-v1.bolt` | Current/previous recovery checkpoint and durable operation idempotency directory | Execution waits for a streaming rebuild |
| `.recovery-cache/<session>/recent-v1.json` | At most 100 recent messages, bounded to 512 KiB inline | Recent baseline reports `preparing` |
| `.query-cache/<session>/history-locator-v1.sqlite` | Message versions, positions, turns, transaction provenance, and ContentStore references | Historical paging reports `preparing` or `failed` |
| `.query-cache/<session>/search-v1.sqlite` | Search documents and FTS5 trigram index | Search alone reports `preparing` or `failed` |

The locator stores no message body or search document text. Small page bodies
are read from ContentStore under the 2 MiB response budget; larger bodies use
a session and storage-generation-bound range grant. Reads are limited to 1
MiB and grants expire after 15 minutes.

## Open and execution ownership

`OpenSession` is observation-only. It reads the recent publication and reports
recovery, history, and search readiness without taking the writer lease.
`EnsureExecution` coalesces concurrent recovery and returns a client binding to
the single published runtime. Releasing a client binding cannot stop another
client's work. An idle unbound runtime is retained for 60 seconds so normal tab
switches reuse the live projection.

Protocol 7 Desktop, Serve, and Remote clients use the same bounded operations:

- `SessionOpenForTab`
- `SessionHistoryPageForTab`
- `LocateSessionMessageForTab`
- `SearchSessionHistoryForTab`
- `SessionHistoryContentForTab`

Old transcript snapshot methods remain available only for hosts without the
protocol 7 reader capability. Production protocol 7 hydration does not call
them.

## Snapshot and cursor rules

History and search cursors contain the storage revision, derived projection
version, fixed durable sequence, projection generation, and position. Appends
do not invalidate a fixed snapshot. A storage replacement, semantic rebuild,
or query mismatch returns `stale_cursor`; an uncovered requested cut returns
`preparing`.

Recovery checkpoints publish only after the corresponding log and external
content are durable. The checkpoint records the final transaction anchor and
log offset. Open validates the anchor and scans only the tail. The previous
checkpoint is retained for fallback. Full prefix verification is an explicit
`InspectSession` operation.

## Performance invariants

- Session listing reads catalog metadata and no event body.
- Recent hydration is bounded by 100 messages and 512 KiB inline data.
- History pages are bounded by 500 messages and 2 MiB.
- Locator and search construction are independently cancellable and globally
  limited to two concurrent jobs.
- Normal append advances derived databases from their durable sequence rather
  than rebuilding the complete database.
- Production reader paths are guarded against calls to full `Snapshot`,
  `History`, and compatibility materialization helpers.

# 会话快速恢复与独立历史

Reasonix v4 事件帧与 ContentStore 仍是唯一权威的会话数据。普通打开路径使用
有界、可重建的投影，不校验或物化完整会话记录。

## 持久化投影

每个会话独立发布以下本地派生产物：

| 产物 | 用途 | 故障影响 |
| --- | --- | --- |
| `storage.identity.json` | 将游标和投影绑定到物理日志代际 | 使全部派生数据失效 |
| `.recovery-cache/<session>/recovery-v1.bolt` | 当前及上一份恢复检查点和持久操作幂等目录 | 执行等待流式重建完成 |
| `.recovery-cache/<session>/recent-v1.json` | 最多 100 条最近消息，内联数据不超过 512 KiB | 最近消息基线报告 `preparing` |
| `.query-cache/<session>/history-locator-v1.sqlite` | 消息版本、位置、回合、事务来源和 ContentStore 引用 | 历史分页报告 `preparing` 或 `failed` |
| `.query-cache/<session>/search-v1.sqlite` | 搜索文档和 FTS5 trigram 索引 | 仅搜索报告 `preparing` 或 `failed` |

Locator 不保存消息正文或搜索文档文本。小型页面正文在 2 MiB 响应预算内从
ContentStore 读取；大型正文使用绑定会话与存储代际的范围凭据。单次读取上限为
1 MiB，凭据在 15 分钟后过期。

## 打开与执行所有权

`OpenSession` 只用于观察。它读取最近快照并报告恢复、历史和搜索准备状态，不获取
写者租约。`EnsureExecution` 合并并发恢复请求，并返回绑定到唯一已发布运行时的
客户端句柄。释放一个客户端句柄不能停止其他客户端的工作。空闲且没有绑定的运行时
保留 60 秒，使常规标签页切换可以复用 live 投影。

协议 7 的 Desktop、Serve 和 Remote 客户端使用相同的有界操作：

- `SessionOpenForTab`
- `SessionHistoryPageForTab`
- `LocateSessionMessageForTab`
- `SearchSessionHistoryForTab`
- `SessionHistoryContentForTab`

旧 transcript snapshot 方法仅供不支持协议 7 reader 能力的 host 使用。协议 7 的
生产 hydration 路径不会调用这些方法。

## 快照与游标规则

历史和搜索游标包含存储 revision、派生投影版本、固定 durable sequence、投影代际
和位置。追加事件不会使固定快照失效。存储替换、语义重建或查询不匹配会返回
`stale_cursor`；请求的固定点尚未被覆盖时返回 `preparing`。

恢复检查点仅在对应日志和外部内容完成 durable 写入后发布。检查点记录最后一个事务
锚点和日志偏移。打开时只校验锚点并扫描尾部，同时保留上一份检查点作为回退。
完整前缀校验通过显式的 `InspectSession` 操作执行。

## 性能约束

- 会话列表只读取目录元数据，不读取事件正文。
- 最近消息 hydration 最多包含 100 条消息和 512 KiB 内联数据。
- 历史页面最多包含 500 条消息和 2 MiB 数据。
- Locator 与搜索构建相互独立、可取消，全局最多并发运行两个任务。
- 普通追加从 durable sequence 增量推进派生数据库，不重建完整数据库。
- 静态守卫禁止生产 reader 路径调用完整 `Snapshot`、`History` 和兼容物化助手。
