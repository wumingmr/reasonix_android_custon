# Desktop Session v5

Desktop Session v5 makes `SessionRef{HostID: "local", SessionID}` the only
runtime identity for local conversations. Workspace membership is presentation
state, not a storage locator, and a tab is only a disposable view over that
identity.

## Authoritative state

- `desktop-sessions-v5/by-id/<session-id>` owns immutable headers, manifests,
  events, and content references.
- `desktop/workspace-state-v1.json` owns Workspace and Session ordering,
  visibility, archives, and in-flight create reservations.
- Canonical title, model, and turn metadata remain events. Query indexes are
  disposable projections and cannot add or remove Workspace members.
- Local Desktop uses one `SessionService` with host ID `local`. A Workspace
  path is recorded in a new session header but is never needed to open it.
- Workspace attachment validates the candidate SessionID against its immutable
  header `cwd`; the ordered registry is the membership authority, while the
  header prevents one Session from being attached to the wrong Workspace.

Creation and rotation reserve a SessionID in `pendingCreates`, seed and flush
the canonical session, attach the ID to its Workspace, and only then expose a
ready tab. Startup completes an interrupted attach when the session exists and
drops the reservation when it does not. Tab pruning is refused if snapshot,
metadata, or registry publication has not completed.

## Opening and recovery

The Workspace browser lists registry IDs even while their projections are
being rebuilt. `OpenSession` first reads history without a controller and never
creates a replacement for a missing or damaged ID. A navigation sequence fences
late results so only the newest selection can replace the visible runtime.
Frontend transcript, draft, operation, paging, and hydration fences use the
complete SessionRef first; legacy paths are only a compatibility fallback.

When a saved model no longer exists, Desktop retries the same SessionID with
the configured default model, appends the replacement `session/config` event,
and reports a non-blocking warning. History remains independent of projection
and controller startup.

## Upgrade boundary

The v5 migrator reads, but never modifies, canonical v4 and legacy JSONL data.
Imports are staged, validated, atomically published, and attached to a Workspace
before their ledger record becomes complete. A verified unchanged source is
idempotent even after the destination advances. Different sources are never
merged merely because their IDs or content match; collisions receive a stable `migr-<hash>` ID.
Failures remain retryable and do not stop other sources from migrating.

Canonical v4 migration enumerates durable session identities independently of
display metadata. A missing or stale catalog cache must never make a conversation
look empty and exclude it from migration; no open tab or legacy JSONL equivalent
is required. Source read failures are recorded in the migration ledger and exposed
by session architecture diagnostics. Startup retries them and finishes workspace
attachment for targets published before an interruption, without duplicating them.

Older session inputs in global, known-project and saved-tab directories are
discovered during the same startup pass, including historical `sessions-v3`
directories alongside `sessions-v4`. Schema-1
transcripts are imported even when their display sidecars are absent. Schema-2
DAG transcripts enumerate every live head and import each head independently;
the first adopted head retains its source key across selection changes and
retirement. Older migration receipts are reconciled against source content so
upgrading from an older migrator does not duplicate continued targets. Retired
heads remain excluded. Prototype
v3 and linear v3/v3.1 stores use their explicit read-only adapters and are staged
into the v5 codec after validation. Paired transcript/event sources are compared
before publication; divergent histories remain readable but are refused rather
than silently choosing one.

Automatic conflict-recovery artifacts are excluded from the ordinary v5 list.
Discovery recognizes `recovered`, `version_kind=recovery`, a recovery digest,
and the historical `-recovery-<16 hex>` filename convention. The exclusion
follows paired v3/v4 stores and conversion provenance, including archived
metadata when originals are absent. Original files remain untouched. An
ordinary user fork, a shared title, or an arbitrary filename containing
"recovery" does not qualify for exclusion.

Persisted `manifest.source` provenance also links already-converted generations,
including v2 -> v3, v2 -> v3 -> v4, and native v3 -> v4. All inputs of a linked
head are staged separately so an ancestor's source digest cannot accidentally
read a continued descendant that has the same identity. Equal histories and
strict prefixes share one v5 target containing the complete history. Independent
continuations become separate targets; unrelated sessions are never coalesced
just because their text or title matches. An omitted legacy head is resolved from
the conversion's immutable original snapshot, not the old app's current selection.
Stored-only chains are resolved even after the original JSONL is removed.
If a native v3 source directory is removed after multiple conversions, its
persisted source path still identifies the shared origin across archived copies.

Each source receives its own adoption receipt, including covered ancestors.
Already-completed receipts and v5 continuations remain authoritative; migration
does not delete existing v5 conversations or rewrite their content to force a
merge. Source changes with additional independent work are imported separately.

Each startup still enumerates source directories, but completed migrations use a
lightweight file revision (file presence, mode, size and nanosecond modification
time). Unchanged sources do not replay history, construct temporary imports,
compare target history, reattach membership or rewrite the ledger. Legacy checks
include the same sidecar set as the importer's freeze operation. This revision is
a change detector for normal writes, not an integrity checksum for edits that
deliberately preserve both file size and modification time.

The ledger records that a source was adopted. Continuing, archiving or deleting
the v5 target does not make that source eligible again. Missing revision markers
in older completed records are filled after a one-time comparison with the
recorded **source** content digest, never with the target's current history.
Changed source content is imported separately without overwriting continued v5
work. Failed attempts retain the previous completion receipt; a source changed
during import is not certified as complete. Pending and failed imports remain
retryable on the next startup.

| Ledger field | Older data | New reader/writer | Previous reader/writer |
| --- | --- | --- | --- |
| `sourceRevision` (optional) | Absent | Reconcile source digest once, then skip unchanged revisions | Ignores the field; may drop it on write, requiring reconciliation again |
| `previousCompletion` (optional) | Absent | Retains prior adoption proof across failed updates | Ignores the field; old releases retain their old migration behavior |
| `legacyHeads`, `legacyHeadsRevision` (optional) | Absent | Enumerates frozen heads once; skips replay while the source revision is unchanged | Ignores these fields; may drop them on write |
| `legacyPrimaryHead`, `legacySelectedHead` (optional) | Absent | Keeps per-head identity stable while routing the selected head to its paired store | Ignores these fields; old migration behavior remains |
| `legacyAdoption` (optional) | Absent | Preserves a pre-head-migration receipt when selection changes or migration is interrupted | Ignores this field; old migration behavior remains |
| `legacyConversions` (optional) | Absent | Caches resolved conversion ancestry/head IDs under the source revision | Ignores this field; discovery is repeated if it is dropped |

| Source format | Old data | New reader | Previous reader | Compatibility |
| --- | --- | --- | --- | --- |
| JSONL / schema-1 | Preserved byte-for-byte | Imported without display metadata; event history takes precedence | Still reads original source | Read-only upgrade |
| schema-2 DAG | Preserved byte-for-byte | Every live head becomes a resumable v5 session; retired heads stay excluded | Original selection and branches remain intact | Read-only upgrade |
| Prototype v3 / linear v3 / v3.1 | Preserved byte-for-byte | Explicit adapter validates and stages into v5 | Still reads original source | Read-only upgrade |
| v5 output | Separate directory | Native v5 reader/writer | Not visible to older versions | Existing isolation boundary |

The ledger stays at version 1. Writes preserve unknown root and record fields;
unreadable or future-version ledgers are refused rather than replaced. Session
formats and provider-visible message bytes are unchanged.

Optional `submission/accepted` events added by #10392 survive canonical export
and migration unchanged. Receipts retain their original session scope: a
same-identity migration exposes them in history, while a conflict mapped to an
independent identity does not adopt the old session's admission keys. This
matches fork isolation and keeps host metadata out of provider messages. The
canonical title reader introduced by #10389 reads migrated history directly.

Protocol 9 is a hard Desktop shell/host boundary. Older releases retain their
original data but do not see sessions created only in v5. Remote and Serve
session protocols are unchanged.

## 中文说明

Desktop Session v5 将 `SessionRef{HostID: "local", SessionID}` 设为本地会话
唯一运行时身份。Workspace 只管理展示归属与顺序，tab 只是可淘汰的视图，
Topic 和项目路径都不再参与打开 canonical 会话。

- `desktop-sessions-v5/by-id/<session-id>` 保存不可变 Header、Manifest、事件
  和内容引用；canonical 事件仍是标题、模型及轮次 metadata 的唯一真相。
- `desktop/workspace-state-v1.json` 保存 Workspace/Session 顺序、可见性、归档
  状态和新建事务；查询索引损坏或重建时不得删除其中的 SessionID。
- Registry attach 会用不可变 Header 的 `cwd` 校验 Workspace 归属；Registry
  仍是成员与顺序真相，但错误 Workspace 不能收录该 SessionID。
- 新建与轮换必须依次完成 pending 预留、canonical seed/flush、Registry attach，
  然后才允许 tab 进入 Ready；淘汰 tab 前会重新验证持久化结果。
- 打开会话先进行与 controller 无关的历史读取，缺失或损坏的 ID 不会生成
  空白替代会话；navigation sequence 保证快速连续点击仅最后一次生效。
- 前端 transcript、草稿、操作、分页与 hydration 防线优先比较完整
  SessionRef；legacy path 只作为旧会话兼容回退。
- 原模型失效时，在同一 SessionID 上使用 Desktop 默认模型恢复，并追加新的
  `session/config` 事件；不改变已有历史和 provider-visible prompt/tool bytes。

v5 迁移器只读保留 canonical v4 与 legacy JSONL。每个导入都先在临时目录完成
校验和原子发布，再写入 Workspace；已验证且未变化的来源幂等复用，不因为
ID 或正文相同合并不同来源，冲突稳定映射为 `migr-<hash>`。
Protocol 10 是 Desktop shell/host 的硬边界；远端与 Serve
协议保持不变，旧版只能看到升级前保留的数据。

canonical v4 迁移按持久化会话身份枚举，不依赖标题、轮次、预览等显示缓存。
缓存缺失或过期不能成为跳过会话的依据，也不要求会话有打开的 tab 或对应的
legacy JSONL。源数据读取失败会记入迁移台账并通过会话架构诊断接口报告；
下次启动会重试，并为中断前已发布的目标补齐 Workspace 归属，不重复创建。

启动扫描同时覆盖全局、已登记项目和保存的 tab 所指向的旧目录，包括历史
`sessions-v3` 与现有 `sessions-v4`。v1 JSONL / schema-1 日志不再依赖显示
元数据；通过统一文件分类排除事件、运行记录等辅助 JSONL，避免误导入为对话。
v2 schema-2 DAG 从冻结副本枚举所有未删除分支，分别恢复为可续聊的 v5 会话；
在旧版本中切换或删除主分支不会改变其他分支的迁移身份。已删除分支不会复活。
v3 原型、线性 v3、v3.1 均使用显式只读转换器。旧快照与配对事件存储先比较
历史，选择包含完整较新历史的一方；存在真正分歧时保留源文件并报告失败。

自动生成的冲突恢复副本不迁入 v5 普通会话列表。通过 `recovered`、
`version_kind=recovery`、恢复摘要以及历史 `-recovery-<16位十六进制>`
文件名识别，排除规则沿配对的 v3/v4 存储和转换来源链继承；原文件缺失时也检查
保留的归档元数据。原始文件保留不动。普通用户分支、相同标题，以及仅包含
“recovery”文字的普通文件名不受影响。

迁移还会根据 `manifest.source` 追溯已经转换的来源关系，覆盖 v2→v3、
v2→v3→v4、原生 v3→v4 等多代目录同时残留的情况。每代源在独立临时目录中
校验，避免同 ID 的较新副本污染较早源的内容摘要。相同历史或只包含较早前缀
的源共用一份完整的 v5 会话；同源但各自续聊后产生不同内容的记录分别保留。
没有来源关系的对话，即使文本或标题相同，也不会被合并。

旧转换记录未填写 head 时，从它保留的原始快照确认当时选中的分支，不拿旧软件
当前选择代替。即使原始 JSONL 已移除，保留的 v3/v4 转换链仍会去重。
原生 v3 目录在多次转换后被移除时，也按保存的原始来源路径识别各归档副本的关系。
被覆盖的较早源也记录各自的迁移完成证明，重启、v5 续聊或删除目标不会使它再次导入。
已完成的旧迁移记录和 v5 中的新工作仍受保护；不会为强行合并而删除已有会话或
覆盖其内容。旧源后来出现额外的独立工作时，按新分支保留。

| 来源格式 | 旧数据 | 新版读取行为 | 旧版读取行为 | 兼容结论 |
| --- | --- | --- | --- | --- |
| JSONL / schema-1 | 原字节保留 | 无元数据也迁移，事件历史优先 | 仍读取原文件 | 只读升级 |
| schema-2 DAG | 原字节保留 | 恢复全部未删除分支 | 原分支与默认选择不变 | 只读升级 |
| v3 原型 / 线性 v3 / v3.1 | 原字节保留 | 校验后转换并导入 v5 | 仍读取原文件 | 只读升级 |
| v5 输出 | 独立目录 | 原生读写 | 旧版不可见 | 沿用隔离边界 |

每次启动仍枚举旧目录，但已完成项只检查文件是否存在、类型、大小与修改时间。
源文件未变化时，不重读历史、不生成临时导入、不比较目标内容、不重新登记归属，
也不重写台账。JSONL 检查包含导入器冻结的全部 sidecar。此标记用于识别正常文件
写入，不是用于检测刻意保持大小和修改时间不变的改写的完整性校验。

台账确认的是旧源已经被接收；在 v5 续聊、归档或删除目标，都不会触发重新导入。
旧台账缺少 `sourceRevision` 时，会与台账保存的源内容摘要核对一次后补齐标记，
不会拿旧源与已续聊的目标比较。旧源内容真正增加或变化时单独导入，保留 v5 中
的新工作。失败更新通过可选字段 `previousCompletion` 保留上次成功记录；迁移中
源数据发生变化则不标为完成，下次启动继续重试。

新增可选字段 `legacyHeads`、`legacyHeadsRevision` 缓存已校验的分支列表，
旧源不变时不重放 DAG。`legacyPrimaryHead` 固定首次迁移的主分支身份，
`legacySelectedHead` 标识应与配对存储比较的当前分支；`legacyAdoption`
保留旧迁移器的成功记录，按源内容识别已经在 v5 续聊的目标。旧台账没有这些
字段时自动补齐，原有成功记录不会因升级或重试而丢失。
`legacyConversions` 缓存已经核对的转换来源及分支身份，参与同一源版本校验，
源数据未变化时无需重新回放原始分支快照。

台账仍为 version 1，旧读取器忽略新增可选字段；旧写入器可能丢弃这些字段，
重新升级后需要再次核对。旧版本本身仍按旧迁移策略运行。新写入保留未知根字段
和记录字段，拒绝覆盖损坏或未来版本的台账；会话格式与模型可见消息内容不变。

#10392 新增的可选 `submission/accepted` 事件通过 canonical 导出与迁移原样保留。
回执继续按原会话 ID 隔离：同 ID 迁移后的历史保留提交标识；因冲突映射为独立 ID
的会话不会接收旧会话的提交幂等键，与 fork 隔离规则一致。回执不进入模型消息。
#10389 新增的 canonical 标题读取接口可直接读取迁移后的历史。
