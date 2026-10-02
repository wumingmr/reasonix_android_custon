# 会话归档方案：main-v2 适配审查 / Archive integration review

审查日期 / Reviewed: 2026-09-16.

This document records the pre-integration assessment. The implementation now
uses `a4b83056` as its base; current results are in
[qualification](session-archive-upgrade-validation.md). Statements below about
draft code and merge probes describe that earlier assessment only.

本文保留集成前审查记录。当前实现已以 `a4b83056` 为基线，最新结果见验收文档；
下文关于草稿代码和合并试算的描述仅代表当时评估，不代表当前完成状态。

## 基线与结论 / Baseline and verdict

- 当前工作基线 / Working base: `738e9b956efecc65c9bdb34af39d490081379af5`.
- 本次获取的 main-v2 / Reviewed upstream: `a4b83056a99ff1da1700a585e8b9da71fb872838`.
- #10392: `8b426dc870f8887a5e89067617e9a91380213ed5`.
- #10395: `a4b83056a99ff1da1700a585e8b9da71fb872838`.

需要实质适配。产品方向仍是「正常会话 → 归档进入回收站 → 恢复或彻底删除」。不恢复三个生命周期分页；历史待恢复是独立辅助入口，不参加清空回收站。最新主线没有替代完整的注册表生命周期与回收站闭环，但已经替代本地方案的部分迁移实现。

Substantial adaptation is required. Keep the single trash flow: active → archived in trash → restore or permanent deletion. Historical recovery is an auxiliary view, excluded from empty-trash. Upstream replaces parts of the migration implementation, not the entire registry lifecycle or trash feature.

审查仅对比代码并在临时文件执行三方合并试算，未合并、重置或改写现有实现。15 个已修改的跟踪文件与上游重叠，其中 7 个文件产生文本冲突。无文本冲突不代表语义兼容；新增本地文件同样需要按上游调用边界审查。

This review compares code and performs temporary three-way merge probes; it does not merge, reset, or rewrite implementation. Fifteen modified tracked files overlap upstream; seven have textual conflicts. Clean merges and local new files still require semantic review.

## 保留、替换与适配 / Keep, replace, adapt

| 范围 / Area | 决策与原因 / Decision and reason |
| --- | --- |
| 注册表 schema 2 / Registry | 保留生命周期、generation、pending、备份、未知字段与跨进程锁；对接上游迁移结果。Keep these extensions; integrate upstream results. |
| 历史发现与格式转换 / Discovery and conversion | 以 #10395 的 checkpoint、legacy、lineage、recovery 分层为唯一迁移入口；删除被替代的本地并行扫描和转换调度。Use upstream as the migration owner; remove superseded parallel discovery/conversion orchestration. |
| 本地 preview 升级 / Preview upgrade | `session_preview_upgrade.go` 的预览能力可保留，但格式识别和转换调用复用上游；不要保留第二套导入调度。Keep preview capability using shared upstream adapters. |
| 配对来源判定 / Paired sources | 删除“一有配对存储就 source_identity_unverified”的宽泛规则；使用来源路径、head、manifest 与转换祖先证明。真正冲突仍隔离。Replace blanket quarantine with upstream provenance validation. |
| 来源映射 / Source mappings | 接纳上游所有存活 head 和稳定 primary head key；多个被证明的祖先来源可映射同一目标，不得折叠无关同文会话。Support proven many-source-to-one-target adoption, never content-only identity. |
| 归档、恢复、补登记 / Lifecycle | 保留统一服务、原子注册、故障重放与只读损坏占位；将上游 AttachSession 发布点接入该事务边界。Keep lifecycle guarantees and integrate upstream publication. |
| 提交身份 / Submission identity | 完整保留 #10392 的 knownSubmission、SubmitIdentified、持久化 receipt、冲突检查与不确定结果处理；在其 admission 内加入本地登记失败处理。Preserve durable admission and integrate registry errors inside its boundary. |
| 运行时绑定与任务 / Runtime and tasks | 保留旧别名清理、SessionRef 校验和任务隔离；适配 submission admission、generation、epoch 以及持久 shell 的 reset 边界。Keep identity fixes; integrate new admission and shell lifecycle. |
| AI 标题 / AI titles | 保留 #10389 canonical 历史读取、条件写入及旧控制器结果拒绝；presentation 不能覆盖新的 canonical 标题。Preserve canonical conditional title updates. |
| UI / UI | 保留单一回收站模型；保留 #10390 侧栏底部搜索及 #10388 最后标签关闭时收起工作区。Keep the trash model and newer sidebar/dock interactions. |
| 桥接契约 / Bridge | 从最终 Go DTO 和接口重新生成，不能手工拼接旧生成物；本地协议 10 仍需同步 shell、mock、握手测试，远端 Serve 不变。Regenerate contracts from integrated sources; keep Serve unchanged. |

## 必须修正的语义边界 / Required semantic changes

### 1. 迁移完成与工作区登记分离 / Adoption versus registration

#10395 的 `checkpoint.unchanged()`、已有 receipt 与 lineage adoption 分支可以直接跳过导入。这能保护已续写、归档或删除的目标，但不等价于 schema 2 的来源映射和工作区登记已经齐全。需要独立、幂等的 receipt 对账：校验目标头、工作区、生命周期和 pending，再补齐明确缺失的登记；目标缺失或已有删除证据时不得重新导入或自动正常化。

Upstream fast paths correctly skip adopted sources even when targets changed or disappeared. Add idempotent receipt reconciliation for missing registry metadata, without treating a missing target as permission to reimport or resurrect it.

迁移台账负责来源版本与转换证据；注册表负责用户生命周期、成员和操作提交。`sourceMappings` 引用验证后的 receipt，不能成为第二套独立选择目标 ID 的算法。跨文件失败由 pending 操作对账，不能要求两份文件永远同步写成功。

The ledger owns source revisions and adoption evidence; the registry owns lifecycle, membership and operation commits. Source mappings reference validated receipts instead of independently choosing identities. Pending operations reconcile interrupted cross-file updates.

### 2. 历史来源再次变化 / Changed adopted sources

上游具备来源变化后再次导入的流程；原方案要求升级后旧来源变化先待校验。集成时保留上游识别与 staging，但对已有 adoption 的新版本进入待恢复审查，禁止无条件 AttachSession 为正常会话。首次发现、身份与归属明确的历史仍可自动迁移。不得退回“当前目标摘要必须等于旧来源摘要”的逻辑。

For a changed source with an existing adoption, reuse upstream detection/staging but apply the requested recovery-review policy before active membership. Initial unambiguous imports remain automatic. Never compare a continued target against its old source snapshot to decide adoption.

### 3. 迁移去重与附件保留 / Deduplication and retained material

上游按已证明谱系选择最大历史，保留不可比较的延续；不是跨来源按标题或正文合并。沿用此规则，并验证每个被覆盖祖先的附件、工具资料和旧格式附属文件仍有来源定位。自动恢复副本被排除出普通发现后，本地补登记和待恢复扫描不能再次把它们批量显露为正常会话；显式用户分支仍保留。

Keep maximal histories only within proven lineages and preserve incomparable continuations. Retain source locations for auxiliary artifacts. Registry reconciliation must respect automatic-recovery exclusions without hiding intentional user forks.

### 4. 未知字段与锁 / Unknown fields and locks

上游增加 `sourceRevision`、`previousCompletion`、`legacyHeads`、`legacyPrimaryHead`、`legacyAdoption`、`legacyConversions` 等字段。本地旧 JSON marshaler 与上游 `marshalDesktopMigrationRecord` 不应并存为两个写入入口；统一后覆盖这些嵌套对象的未知字段往返测试。将所有台账写入口纳入同一跨进程锁，明确台账与注册表锁顺序，磁盘 I/O 不进入 App.mu。

Consolidate ledger serialization and writes, preserve unknown nested fields in new receipts/conversions, and cover all writers with the same interprocess lock and documented lock order. Keep disk I/O outside App.mu.

### 5. 提交与轮换 / Admission and rotation

发送入口不能退回旧 `ctrl.Submit*` 直调。轮换应同时满足：旧提交完成归属确定、新正文 flush、注册表持久提交、发布新绑定、清理旧别名、推进 generation/epoch。注意 #10392 已有 submission 锁：管理命令触发清空时不可再次获取同一非重入锁。需要确定性交错测试验证重试、清空、旧事件晚到及两个会话相同 submission/task ID。

Do not restore direct legacy submission paths. Coordinate durable submission identity, rotation persistence, binding publication and event generations. Avoid recursively taking the submission lock when a management command rotates a session. Test forced interleavings and identical IDs across sessions.

### 6. 彻底删除是新增功能 / Permanent deletion is new work

当前 `session_purge.go` / `PurgeWithTombstone` 是未验证草稿，不能作为已完成修复。Deleted 是防复活墓碑，不是 UI 中第二种可恢复状态。删除中断后条目必须有可重试的操作入口，不能因先写墓碑而从回收站消失。检查租约、目录约束、暂存目录冲突、查询缓存、Windows 文件句柄和共享内容引用；清空只处理当次已确认集合。

The purge implementation remains an unverified draft. Deleted is an anti-resurrection tombstone, not another recoverable UI state. Interrupted purges must remain actionable. Validate ownership, path confinement, staging collisions, caches, Windows handles and shared content references.

旧来源保留策略与彻底删除必须明确区分：应用会话和当前存储被删除，升级原件/独立备份按兼容约定保留，不承诺安全擦除。所有旧 head、祖先和转换 receipt 都必须尊重墓碑；否则重扫可能创建不同 ID 的替身。不能只对单一主 source key 加保护。

Permanent deletion removes the application session/current store; retained upgrade originals and independent backups are not secure-erased. Tombstone checks must cover all proven source/head/conversion aliases, preventing alternate-ID resurrection.

## 实施与验证次序 / Integration and validation order

1. 保存当前完整改动，在最新 main-v2 的独立工作树集成；原工作树保留。Preserve local edits and integrate in a separate current-main worktree.
2. 先保留 #10392/#10395 的实现和回归，再接入 schema 2 与 receipt 对账，移除重复转换入口。Keep upstream regressions first, then integrate the registry and reconciliation.
3. 接入生命周期、运行时绑定、标题和任务；覆盖旧路径、所有 head、隐藏/离线工作区及补登记。Integrate lifecycle/runtime/title/task behavior across legacy and canonical identities.
4. 完成回收站 UI、翻译、样式和 purge 事务；改掉旧“三分区”测试与文档。Finish the single-trash UI and purge transaction; replace obsolete three-section expectations.
5. 重新生成协议、host owner 和 inventory；运行静态检查、相关用例，再执行 Desktop、根模块、前端完整门禁。Regenerate artifacts and run the validation ladder.
6. 增加组合回归：迁移→续写→归档→重启→恢复→重启；多 head/转换谱系→删除→重扫；发布正文后中断→重启对账；提交重试与清空交错。Add integration regressions spanning upstream and local features.
7. 重新打包并验证真实侧栏、回收站、只读预览、恢复与删除、shell/service 握手及退出。跨平台未实际执行的项目单独列出。Rebuild and validate the production package; distinguish cross-compilation from runtime verification.

## 当前证据边界 / Current evidence boundary

本次完成静态审查和三方冲突试算，未声称新基线的组合测试通过。旧 `v1.38.9-test.20260916.archive-v2.5` 包基于旧 HEAD，不包含本次两个 PR，也不包含尚未完成的回收站/purge 改动；已有通过记录只能作为旧实现基线，不能用于当前发布验收。

This assessment provides static review and merge-probe evidence, not passing integrated tests. The earlier archive-v2.5 package predates these PRs and unfinished trash/purge changes; its passing checks do not qualify the new integration.
