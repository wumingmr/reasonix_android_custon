# Native historical session storage / 历史会话原格式存储

Opening, resuming, and rebuilding a historical conversation no longer require
conversion to the current session store. The backend is selected from its
durable identity and format; new conversations use the current store.

打开、继续和重建历史会话不再以迁移为前提。后端按持久化身份和文件格式选择；
新建会话使用当前存储。该策略不改变模型请求内容，也不按应用版本号或文件时间猜测格式。

## Routing / 路由规则

- JSONL and schema-1 event logs retain their existing writer. Reading a bare
  JSONL does not bootstrap a duplicate event log. Schema-2 DAG sessions retain
  their selected head and writer.
- Linear v3, prototype v3, v3.1, and v4 directories open under a shared service
  for their original root. Appends preserve the source codec and schema.
- A completed canonical cutover remains authoritative. A paired older event
  store is selected only after a read-only prefix comparison proves it is at
  least as complete as the JSONL; divergent histories fail closed.
- New creates use the current service even when the visible conversation is
  backed by a historical root. Desktop workspace membership commits before
  publication of the new runtime.
- Startup and ordinary navigation do not resume unfinished imports. Explicit
  import/recovery and existing management operations that request an ownership
  transition retain their journaled conversion flow. This includes historical
  source management through `PrepareSession`; it is not called by ordinary
  navigation. Archive, move, copy and fork behavior is not redesigned here.

对应规则：JSONL/schema-1/schema-2 保持原格式；旧目录由原目录的共享服务持有写锁；
已完成迁移的身份优先，旧配套副本通过前缀比较选择，分歧时报错保留原件。
从旧会话点击“新建”写入当前存储，并先完成工作区登记。
启动、普通打开和续聊不恢复中断的导入任务。显式导入、恢复，以及既有管理操作的
所有权转换流程仍保留；本次未重新设计归档、移动、复制、分叉的存储事务。

## One ordinary lifecycle / 统一的普通会话生命周期

The ordinary sidebar combines usable native and historical conversations without
requiring an import or recovery choice. Once source **a** has been adopted as
**b**, only **b** is listed; archive and purge follow the same durable adoption
and tombstone. A changed retained source does not automatically become another
ordinary conversation. Purge removes exclusively owned, unchanged canonical
source artifacts; shared JSONL/head data remains protected by the tombstone.

普通侧栏统一展示可用的新旧会话，不要求用户选择导入或恢复。旧会话 **a** 已由 **b**
接管后只展示 **b**，归档和删除沿用同一份归属记录与删除标记。保留的旧源即使变化，
也不会自动变成另一条普通会话。彻底删除会清理独占且未变化的旧目录；共享 JSONL
和分支数据通过删除标记隐藏，不为删除一个会话而误删其他分支。

Background discovery repairs missing adoption receipts from matching source
content and existing lifecycle evidence. Retired duplicate receipts cannot
obscure a unique live owner; competing live owners are never arbitrarily chosen.
An absent destination can be restored from a verified retained source under its
original ID, with journaled restart recovery and generation fences. Existing
destination content, title, pin and lifecycle are not replaced. The catalog's
completion also schedules receipt recovery if it finished after startup discovery.
This does not resume unrelated unfinished imports or convert ordinary unmigrated
sessions merely to display them.

后台发现流程结合源内容与生命周期证据补齐丢失的关联；已删除的重复记录不再阻挡
唯一有效归属，但多个存活目标之间不会随意选一个。目标目录缺失时，可由验证过的
保留源恢复原 ID，并通过事务日志支持中断重启、代际校验防止覆盖并发归档或删除。
已有目标的正文、标题、置顶和生命周期保持不变。索引晚于首轮发现完成时，也会触发
关联恢复；这不意味着启动时重试所有旧导入，也不会为了展示而转换未迁移会话。

Confirmed unreadable or missing entries are omitted from ordinary lists, but
their files are retained for later repair. Hiding is not deletion authority.
Navigation reports the classified operation error, not a guessed missing-project
error for every failed project conversation. A global legacy file with stale
metadata pointing to an absent project uses its actual global storage owner;
a file physically stored in a project is not silently reassigned elsewhere.

确认不可读或缺失的条目不展示在普通列表，原文件仍保留以供后续恢复；隐藏不代表
授权删除。打开失败按实际操作错误分类提示，不再一律误报项目不存在。全局旧文件
若仍记录已经不存在的项目，会使用其实际全局存储归属；项目内文件不会静默转移。

No persisted schema or RPC field is added by this reconciliation. Existing
source mappings, migration receipts and import journal phases are reused; older
readers can still parse these records, but do not gain the new recovery behavior.
Regressions cover lost mappings, retired aliases, lost native/JSONL targets,
continued target content, source changes during repair, interruption/replay,
archive/purge/restart, corrupt-row filtering and stale registry generations.

本次协调修复不新增持久化 schema 或 RPC 字段，复用现有来源关联、迁移回执和导入
事务阶段；旧版仍能解析，但不因此具备新版恢复逻辑。回归覆盖关联丢失、已删除别名、
新旧目标缺失、目标续聊、恢复中源变化、中断重放、归档/删除/重启、损坏条目过滤和
过期注册表代际。

## History reads / 历史分页

Follow negotiates `storageBackend: "legacy"` for path-backed sessions. The
frontend then uses the native transcript snapshot, page, outline and content
APIs. A failed local request never selects a remote backend. Cursors pin a
snapshot, page sizes remain bounded, and old asynchronous replies cannot
publish into a replacement binding. Forward paging handles both byte limits
and overlap at the final page. Message and turn lookup operate on the same cut.

旧会话通过 Follow 明确声明后端。前端使用有界双向分页，游标固定快照；本地读取失败
不会切换到远程副本。过期回复不能污染新会话；尾页和大消息边界不能导致重复或漏消息。

The change removes full-transcript conversion and its duplicate writes from
the open path. It does not make every old format constant-time: old JSONL and
linear logs can still require a scan to reconstruct execution state. Derived
indices, recovery metadata, and runtime envelopes remain permitted; they are
not a second authoritative transcript.

本次移除打开路径上的全文转换和重复写入，不承诺所有旧格式都能常数时间打开：
JSONL/线性日志仍可能扫描以恢复执行状态。允许维护派生索引、恢复元数据和运行事件，
但不因此创建第二份权威会话正文。

## Compatibility / 兼容与降级

| Format or field / 格式或字段 | Old data / 旧数据 | New reader and writer / 新版行为 | Previous reader / 旧版行为 |
| --- | --- | --- | --- |
| Bare JSONL | Read existing messages / 读取原消息 | Save as JSONL, no event-log bootstrap / 原格式保存 | JSONL syntax preserved / 保留 JSONL 语法 |
| Schema 1 | Replay existing log / 回放原日志 | No automatic DAG upgrade / 不自动升级 DAG | Schema unchanged / schema 不变 |
| Schema 2 DAG | Preserve head identity / 保留分支身份 | Continue selected head / 续写所选分支 | DAG schema unchanged / DAG schema 不变 |
| Linear/prototype v3, v3.1 | Read original log / 读取原日志 | Append original codec; preserve complete bytes during tail repair / 原 codec 追加，修复保留完整前缀 | Codec preserved; support for newer event kinds depends on the older binary / codec 保留，新事件类型支持取决于旧程序 |
| V4 and existing cutovers | Remain authoritative / 继续作为权威数据 | Native v4; no reverse migration / 原生读取，不反向迁移 | Requires a reader supporting that revision / 需支持对应 revision |
| New session | No inherited history / 不继承旧正文 | Current schema, current root / 当前格式和目录 | Existing current-format boundary applies / 遵循现有格式兼容边界 |
| Follow `storageBackend`, page `messageId`, snapshot `notFound`, saved `sessionHeadId` | Missing fields retain defaults / 缺失时默认行为 | Additive optional fields / 可选增量字段 | Unknown fields can be ignored; native paging requires the updated frontend / 可忽略未知字段，原格式分页需新版前端 |

Codec preservation is not a blanket guarantee that an arbitrarily old binary
understands newly emitted domain events. Do not delete source originals or
rewrite completed migrations as part of a downgrade.

保留 codec 不等于任意旧版本都理解新版业务事件。降级时不能删除历史原件，
也不能把已完成迁移的会话强制回退成旧副本。

## Regression coverage / 回归覆盖

The regression fixtures cover unchanged source bytes on open, native append
and reopen, torn-tail repair, completed-cutover precedence, conflicting paired
histories, creation in the current root, selected DAG heads across restart and
model rebuild, and repeated admission without redundant controller rebuilds.
Paging tests cover both directions, byte-limited pages, old fixed snapshots,
message/turn lookup, remote routing, and replies arriving after a tab switches.

回归用例覆盖打开时原件不变、原格式续写和重开、日志尾部修复、已完成迁移的优先级、
配套副本冲突、新建会话的目录和工作区归属、分支选择跨重启及模型重建保留，以及
再次发送不重复重建控制器。分页覆盖双向翻页、字节上限、旧快照、消息和轮次定位、
远程路由，以及切换后的过期回复。

Browser fixtures validate loading feedback and transcript interaction. They do
not measure an end-to-end native import of a user's real history, and do not
replace packaged-app or cross-platform release qualification.

浏览器夹具验证加载反馈和会话交互，不代表用户真实历史的原生端到端耗时，
也不替代安装包及跨平台发布验收。
