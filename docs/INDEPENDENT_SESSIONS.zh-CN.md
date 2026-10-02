# 独立会话身份与修复验收

[English](INDEPENDENT_SESSIONS.md)

实施基线：`4c8cec3b9f69f979c34769eb197908b7f549c578` 加原有工作区修改。验证日期：2026-09-17。以下记录实施阶段的本地验证；交付分支基于 `81fe2f1fd`，独立提取本次改动，排除消息展示 PR #10438 的改动。交付时的 CI 与合并状态以 PR 记录为准。

## 产品约束

普通分叉是平级的独立会话。行身份、选中、未读、标题、菜单、运行状态和通知必须指向同一目标。同 TopicID 不代表同一会话；子 agent 和自动恢复副本保留专用语义。

已有 SessionID 和 transcript 保留。新分叉记录 `ParentSessionID`，继承父会话分组，默认不置顶；只有人工排序启用时才插到父会话后面。活动排序工作区继续按活动排序。

本次不处理全文导航、消息窗口淘汰和另外的 purge/log 竞态；保留工作区中这些领域已有的其他任务修改。

## 身份与操作归属

| 对象 | 稳定身份 | 请求隔离 |
| --- | --- | --- |
| 标准会话 | HostID + SessionID | generation/请求版本另行隔离异步结果 |
| 未接管来源 | HostID + 规范化 SourceKey；多 head 包含 HeadID | 所属 owner 验证具体来源 |
| 未保存界面 | HostID + TabID | 通过已验证别名替换为持久化身份 |
| 仅 TopicID 的兼容入口 | 历史关联 | 多候选返回 `ambiguous_target` |

`sessionIdentityBaseKey` 不改变原来包含 generation 的历史请求身份。`projectSessionIdentity` 仅适配已有 ref/source。只有 owner 验证过的别名能合并身份；相同标题、TopicID 或不同主机的相同路径都不能作为同一会话的依据。

明确 ref 失效时返回错误，不回退选取兄弟会话。本地 resolver 拒绝远程 ref。远程单会话写操作走远程 owner，使用协议支持的明确身份；不支持或目标歧义时，不退回主题批量操作。读取远程组织设置不会启动 Serve。

激活在来源接管前捕获导航意图，内部打开继续使用同一次意图。内部调用不能重新获取更大版本来抢占用户的新点击。选择明确旧来源时，也不会借用同 TopicID 的其他运行实例。

## Registry v3、组织状态和兼容

workspace registry 的 `Organization` 是组织顺序的权威，保存 revision、人工排序开关、明确目标顺序、分组、迁移版本和已导入身份。`Workspace.SessionIDs` 保留 canonical 成员关系，其兼容顺序在同一事务中生成。本地置顶仍属于会话 presentation。

`GetSessionOrganization` 返回快照；`UpdateSessionOrganization(workspace, expectedRevision, mutation)` 接受移到目标前后、移入移出分组等语义操作。前端不把当前可见页当成完整工作区顺序提交。CAS 冲突返回最新快照，前端最多重放原操作两次；仍冲突时重读并提示。

迁移只给尚未导入成员补充旧主题的置顶、分组和排序。明确会话设置优先，包括用户明确移出所有分组的状态。旧主题位置展开为相邻会话区间。来源接管通过已有 migration journal，在同一提交中替换分组、顺序中的来源身份；同一日志其他 head 不被隐藏。

读取兼容 v1/v2/v3。升级 v2 前保存原始 `.v2.bak`，保留现有 v1 备份流程。未知字段继续往返保留，覆盖新增嵌套组织结构和远程项目配置。旧项目文件与组织 sidecar 保留为导入及兼容资料，迁移后不能覆盖 registry。

**降级策略：**受验证的前一版本 reader/writer 明确拒绝 v3，原文件字节不变。不支持旧客户端继续编辑已升级工作区。不要删除 registry 绕过检查，也不要将备份静默覆盖到新用户设置上。此改造不升级或删除 transcript。兼容脚本提取真实基线源码测试旧版行为，不用模拟 reader 代替。

分叉历史生成与组织发布是可恢复的步骤：操作保存 child ID、parent ID 和 pending 状态，重启恢复仍接入同一个子会话并原子继承分组/顺序。相同 operation 重试不重置用户后来修改的标题、置顶或分组；发布失败继续返回错误。

## 投影、归档、分页和未读

- 目录与 runtime 使用同一稳定身份，runtime 只覆盖运行字段。空 runtime 不删除持久化行；置顶区和不完整页也按来源别名去重。
- 归档成功 receipt 安装应用级生命周期屏障，包含已验证别名；目录、runtime、resident cache 共用。归档失败不安装成功屏障；刷新开始与组件重挂载都不清除屏障。只有更高生命周期的活动目录行能证明显式恢复，runtime revision 不能释放屏障。当前采取保守保留策略，直到恢复或应用退出；未实现基于两类 owner 同时确认缺席的自动回收。
- 列表先归一化/去重、筛选和排序，再冻结读取结果。成员、生命周期、展示和分组来自同一份 registry 投影；canonical 元数据只观察一次。续页绑定冻结结果、查询和序号，标题/结果/分组写入不会使其失效。相关生命周期或源变化、过期和回收返回 `stale_cursor`；前端最多整窗重建一次，失败保留旧显示，后台事件合并而不反复取消正在读取的请求。旧历史搜索、资源预算与验收要求见[读取快照实施说明](READ_SNAPSHOT_PAGINATION.zh-CN.md)。
- 未读 v3 用一个 localStorage 对象原子保存指标、基线、记录版本及核验/修复证据。正常读取只增加同指标基线；只有导入且尚未核验的 result 记录，才能凭完整、正值的 owner 观察降低一次。捕获版本与实际读取下限拒绝迟到修复。来源时间戳不转换为结果序号；跨窗口合并保留较新的实际读取。核验去重且最多并发两项；冷元数据未完成时延后修复。

## 七项缺陷验收对应

| 原缺陷 | 修复及正式回归 | 界面证据 |
| --- | --- | --- |
| 历史/顶部重命名波及兄弟 | command owner 捕获明确 selector；canonical runtime 精确标题投影；AI 期间新增兄弟不被迟到标题影响。`independent-session-rename.test.tsx`、`TestAIRenameDoesNotRenameSiblingCreatedDuringGeneration` | 浏览器顶部/侧栏/历史写入都只指向 B；原生重命名不改 A 标题与历史 |
| 归档后迟到 runtime 复活 B | 应用级身份/生命周期屏障。`session-lifecycle-fences.test.ts`、`project-tree-archive-race.test.ts`、`independent-session-boundaries.test.ts` | 浏览器旧目录/runtime + 重挂载；原生归档重启、恢复再重启 |
| 旧分支共用选中/未读 | 来源身份和不同计量类型的未读记录。`independent-session-boundaries.test.ts`、`session-read-activity.test.ts` | 浏览器 B10→20 独立未读，读 A 不清 B、读 B 清除；原生 A/B/A。具体旧来源覆盖属于机制测试 |
| 接管回退分组/重复行 | journal 原子替换别名/分组/顺序；首屏及不完整页按别名合并。`TestOrganizationSourceJournalCommitPreservesUngroupedPlacement`、`TestIndependentLegacyHeadsAdoptOneWithoutHidingSibling`、`independent-session-boundaries.test.ts` | 浏览器 B 移出后重挂载仍未分组；实际多 head 接管由 Go 夹具验证 |
| 写入或过期打断分页 | 绑定快照的统一分页及 stale 重置，另验证持续写入时列表可读。`project_tree_organization_test.go`、`project_topic_group_filter_test.go`、`session_topic_snapshot_test.go` | 浏览器发出 old:5 后收到 stale_cursor，下一次从空游标开始，重排后七行无遗漏和重复 |
| 未读修复降低合法基线 | owner 核验、一次性导入修复、记录 CAS/read floor、跨窗口合并。`session-read-activity.test.ts`、`session_activity_baseline_test.go` | 浏览器 baseline20 在 ready10/20 + 重挂载后仍为20；机制测试另覆盖污染100/10和修复期间读取 |
| 分叉未排在父后 | registry 权威顺序及可恢复发布。`TestOrganizationForkAttachmentInheritsGroupAndFollowsParent`、`TestOrganizationForkPreservesActivitySortWithoutEnablingManualOrder`、`TestIndependentForkResumesPublicationWithSameOperationWithoutResettingChoices` | 原生真实分叉并继续独立对话；中断/重启/顺序由确定性 Go 测试验证 |

额外机制测试覆盖远程主机隔离及被动读取、远程配置未知字段、v2 备份及未知字段、组织 CAS 与失败事务、多 head、canonical 用户设置优先、原生标题绑定/runtime 身份，以及过期导航意图。

收尾新增两项边界回归：`runtime-notification-independent-session.test.ts` 验证两台主机的 turn/prompt/session ID 都相同时，通知不被合并、标题不串号；`TestBlankReuseWaitsForPendingCanonicalCreateInsteadOfCancellingIt` 验证复用启动中的空白会话时等待原创建完成，不取消它并留下可在重启时恢复出的第二个空会话。原生脚本也在首次发送前检查原始/pending 身份。

## 复现命令

默认从仓库根目录运行：

```sh
go test ./...
(cd desktop && go test ./... -timeout 20m)
(cd desktop && go test -race ./internal/workspacestate -count=1)
(cd desktop && go run . -emit-contract frontend/src/generated)
go run ./tools/desktopinventory
git diff --check
(cd desktop/frontend && pnpm build && pnpm test:typecheck)
(cd desktop/frontend && pnpm test)
(cd desktop/frontend && pnpm test:remote && pnpm test:workspace)
(cd desktop/frontend && pnpm exec tsx --test src/__tests__/project-tree*.test.ts src/__tests__/session-identity*.test.ts src/__tests__/independent-session*.test.* src/__tests__/session-read-activity.test.ts src/__tests__/session-lifecycle-fences.test.ts)
(cd desktop/frontend && node bench/independent-sessions.mjs)
python3 desktop/packaging/registry-previous-writer-smoke.py
(cd desktop && go build -o build/bin/reasonix-desktop-service .)
(cd desktop/electron && pnpm build)
node desktop/packaging/independent-session-native-smoke.mjs
```

Desktop 全量测试本身需数分钟，20 分钟是整个进程上限，不是单例重试。此前全量运行发现旧测试依赖“选择同主题的另一个 live 会话”；现改为验证明确来源接管后的映射，同时保留兄弟 runtime。未跳过测试来获得通过。

## 证据记录与限制

| 层次 | 本地证据 |
| --- | --- |
| 根 Go | 全量通过，`reasonix-independent-root-final.log` |
| Desktop Go | 最终全量通过，主包耗时 301.282 秒，`reasonix-independent-desktop-final.log` |
| Race | registry、导航/AI标题/分叉生命周期及空白会话创建复用测试通过，`reasonix-independent-registry-race.log`、`reasonix-independent-lifecycle-race.log`、`reasonix-independent-blank-race.log` |
| 前端 | 50 项会话 focused tests、通知/音效回归、生产/测试 typecheck 及 build 通过。`pnpm test` 的 pretest 通过，随后 discovery runner 发现旧组织 CAS 夹具；将其迁至新语义接口后，394 个 discovery suites 全部通过（`reasonix-frontend-discovery-final.log`）。build 包含 hooks、界面层级、CSS 及当前工作区预算检查 |
| 契约/inventory | 按当前源码重新生成；inventory 新鲜度检查通过，`reasonix-independent-inventory-check.log`；`git diff --check` 通过 |
| Chromium | 真实 ProjectTree、顶部、HistoryPanel、审批卡和 command owners；仅 RPC 边界使用 stub。覆盖键盘、拖拽/分组/排序、审批/停止、归档竞态、分页及未读。`reasonix-independent-browser/result.json`、workbench/creation 明暗四种截图与 `pagination-recovered.png` |
| Electron | 真实开发壳、生产构建 App 前端、真实 Go service、临时 loopback provider；七项场景通过，含启动身份复用、后台完成及两次重启。`reasonix-independent-native/result.json`；`phase-*.json` 记录 canonical 身份和 pending-create 检查点 |
| 旧版写入 | 真实基线拒绝 v3 且保留文件字节，通过。`reasonix-independent-registry-compatibility.json` |
| 远程/工作区 | 协议/owner 隔离夹具及前端 `pnpm test:remote`、`pnpm test:workspace` 通过，`reasonix-independent-remote-front.log`、`reasonix-independent-workspace-front.log`；未进行真实 SSH 主机断线重连验收 |
| CI / 安装包 | 未运行；不能替代跨平台 CI 或生产签名安装包验证 |

表中命名文件为本次本地证据，脚本可重新生成。原生测试使用隔离 home，无外部模型凭据；先创建真实分叉，应用关闭后只在临时 registry 中设为历史同 TopicID 场景，不替换任一 SessionID 或 transcript。

验收过程中保留了两次先前失败：冷启动的未打包 Vite 开发服务器未赶上壳启动期限（最终脚本使用已完成的前端构建，没有延长期限）；生产加载速度则暴露出 pending 空白创建被取消的问题。后者已在空白会话 owner 修复，并有受控 Go 及原生验证。本结果不代表冷开发服务器启动已获验证。
