# 新建会话删除与草稿生命周期：实施与验证

## English overview

This change gives each removable new conversation an explicit action based on
its persisted identity. Empty drafts are discarded immediately; drafts with
content require confirmation; drafts with an unresolved first submission remain
protected. Formal sessions, including zero-message sessions, use the existing
archive and restore path. A legacy metadata-only topic is discarded when it has
no user organization, or archived with recoverable metadata when it has a
manual title, pin, group, or unknown title origin.

`InspectTopicRemoval` returns a disposition and state token. `RemoveTopic`
rechecks identity and token under the storage locks and records an idempotent
operation before changing indexes or metadata. A separate optional
`topicRemovals` collection preserves compatibility with older workspace-state
readers. Interrupted operations replay only while their identity and state
still match; restore preserves the original topic ID and rejects an occupied ID.

Post-implementation review found and fixed four further defects: supported v1
organization files were rejected; cross-workspace topic ID reuse could damage
another workspace on retry or restore; a formal session could be renamed after
confirmation but before archive publication; and an unrelated edit could block
an interrupted restore indefinitely. Deterministic regression tests cover the
failure orderings and startup replay.

Focused Desktop Go and storage tests passed under `-race`. Frontend tests,
typecheck, production build, generated-contract checks, Electron shell build,
lint, and an isolated macOS native UI smoke passed. The native smoke covered
draft actions, project navigation, legacy placeholder removal, sidebar archive,
restart, restore, and a delayed duplicate request. These results do not prove
the root cause in the reporter's original 1.38.11 installation, nor validate
Windows or Linux packages or a real provider's first submission. This work is
source, integration, and macOS development-shell evidence.

基线：实施前同步 `main-v2`，由分析基线 `c84b4389bf14` 前进至 `ec0df71ec129`。
PR 集成时继续同步至 `ea64efc8e`，重新生成 Desktop 契约和命令 inventory。
交付包括代码、生成契约、测试及本记录；合并与发布状态以 GitHub 实时记录为准。

## 行为

| 对象 | 操作及结果 |
| --- | --- |
| 无内容草稿 | 直接丢弃，不进回收站 |
| 含文字、附件、引用、调用内容或粘贴块的草稿 | 确认不可恢复后丢弃；取消保留内容 |
| 提交中、结果未知、取消尚未完成 | 保留提交所有权，阻止丢弃并解释原因 |
| 正式会话，含零消息会话 | 使用原正式归档与恢复流程 |
| 自动默认标题、未置顶、未分组的旧空主题 | 移除占位，不进回收站 |
| 已命名、置顶、分组或标题来源不明的旧空主题 | 元数据进入回收站，可恢复、彻底删除，无聊天预览 |
| 状态改变、归属不唯一或数据无法可靠读取 | 拒绝移除；显示错误并刷新实际状态 |

关闭界面、切换工作区和会话保持草稿的保存／隐藏语义。草稿继续使用
`DiscardSessionDraft(draftID, revision)`，不接入正式会话归档。
不新增正式空会话，不删除用户原始附件，不改变草稿保存期限或聊天正文格式。

## 实现边界

- `useSessionDraftSurface` 继续作为草稿 owner。补齐重复点击保护、丢弃中的
  状态投影和错误展示；保留 revision、编辑版本、提交所有权及导航意图校验。
- `InspectTopicRemoval` 返回处理方式、可操作性、阻止原因及状态 token；
  `RemoveTopic` 接收操作 ID 与原 token，只有明确提交后前端才安装防回跳标记。
  有部分写入时保留原请求身份，重试不重新猜测删除方式。
- 旧 `TrashTopic` 保留运行时快照、租约及正式归档兼容处理；元数据分支共享
  新分类与事务实现。无索引但有历史文件的对象仍归档，跨工作区歧义被拒绝。
- `topicRemovals` 是工作区状态的可选独立集合，不修改旧 PendingOperations
  的严格类型枚举。保存 prepared 意图和完整主题元数据后才移除索引／元数据；
  完成记录写失败不得返回成功。
- 重启只恢复既有操作，复核身份、版本、会话关联和运行状态。
  恢复使用原 ID，恢复排序、置顶、标题来源及仍存在的分组；缺失工作区报错，
  同 ID 新数据冲突不覆盖。清除恢复记录不会再次删除活动主题。
- 已完成的默认占位移除保留幂等收据；可恢复记录使用
  `topic-removal:<operationId>`，移除／恢复／清除推进 generation。
- 完整聊天文件索引不包含无文件主题。普通列表现在补入经归属和数据源检查的
  空占位，避免恢复接口成功但恢复对象在侧栏不可见；不复活已归档正式会话
  或被目录隐藏的历史恢复副本。
- RPC 类型、bridge、mock、英文／简体／繁体文案同步更新。mock 生命周期
  按需加载，保持既有首屏资源预算。

## 本地验证

全部持久化及原生桌面验证使用隔离数据目录；不操作个人会话。

| 验证层 | 结果与范围 |
| --- | --- |
| Desktop Go 回归及 race | TopicRemoval、TrashTopic、空正式会话、旧空主题、完整目录列表、历史恢复链、生成契约检查通过；相关组使用 `-race` |
| 草稿与工作区存储 | `go test -race ./internal/draftstate ./internal/workspacestate` 通过 |
| 明确故障次序 | 用 checkpoint 中断索引／元数据／最终收据各阶段，并用真实写失败验证不得成功；恢复中断、冲突、重复请求、工作区离线、分组消失有回归 |
| 跨进程 | 独立进程重命名后旧 token 被拒绝；存储层已有跨进程 writer／worker 锁测试保持通过 |
| 草稿所有权 | 固定丢弃与提交事务先后，验证提交结果未知和取消请求未完成仍阻止丢弃；迟到保存与旧界面响应、确认期间编辑、重复点击、失败保留内容有回归 |
| 前端 | 草稿呈现／所有权／导航、主题删除 controller、mock、归档竞态、回收站确认及重试通过；远程项目树 28 项通过 |
| 类型及构建 | 前端 typecheck、test:typecheck、完整生产构建及资源预算通过；Electron shell 构建通过 |
| macOS 原生开发版 | 全局快捷新建、项目新建按钮键盘激活、切换返回保留草稿、取消／确认丢弃、侧栏右键归档、关闭重启后恢复、迟到重复删除均通过；无 renderer 错误 |
| 规范及契约 | repolint、golangci-lint、生成契约一致性、`git diff --check` 通过 |
| 降级写入 | 使用基线的真实 v3 workspacestate 源码，读取含 topicRemovals 的状态并执行 RenameWorkspace，确认未知集合及嵌套字段仍完整；新版未知字段往返及深拷贝有单元测试 |

主要可复现命令：

```sh
# 仓库根目录
go run ./tools/repolint

# desktop Go 模块
go test -race ./internal/draftstate ./internal/workspacestate
go test -race . -run 'Test(TopicRemoval|NamedEmptyTopic|EmptyTopicArchive|NewBlank|TrashTopic|HostContractGeneratedFilesAreCurrent|ListProjectTopics|UpgradeLegacyRecoveryChain)' -count=1
golangci-lint run --timeout=3m --new-from-rev=HEAD .

# desktop/frontend
pnpm test:typecheck
pnpm build
pnpm exec tsx src/__tests__/topic-removal-controller.test.tsx
pnpm exec tsx src/__tests__/topic-removal-mock.test.ts
pnpm exec tsx src/__tests__/session-draft-ownership.test.tsx

# 先构建 frontend/dist、desktop/build/bin/reasonix-desktop-service 和 Electron shell
# desktop/electron
node scripts/topic-removal-smoke.mjs
```

原生冒烟脚本使用真实 Electron renderer、IPC 和 Go 服务。确认对话框的
接受／取消由测试控制；持久化 RPC 条件在 Node 侧逐次 await，不使用
`waitForFunction(async ...)` 作为持久化完成证据。脚本输出独立目录中的
`results.json`、截图和 shell 日志，包括关闭重启后的状态断言。

## 实施后缺陷复查

以下四类问题均先以回归测试复现失败，再修复并通过相同测试：

| 问题 | 影响与修复 |
| --- | --- |
| 旧分组文件版本兼容遗漏 | 原读取器支持 v1，但删除检查只接受 v2，导致旧数据无法操作；现沿用受支持的版本范围 |
| 中断重试及恢复遗漏其他工作区归属 | 相同主题 ID 被其他工作区占用后，重试可能移除新索引，恢复可能制造重复归属；现持有跨进程索引写锁检查所有工作区的列表、置顶和分组引用，冲突时保留新数据 |
| 正式归档提交缺少确认状态校验 | 用户确认后发生重命名，归档仍可能提交，启动重放也可能绕过检查；现持久化正式会话状态 token，在工作区状态事务提交点重新验证，启动重放执行同一校验 |
| 恢复中断被无关编辑永久阻塞 | 恢复尚未写入目标元数据时，其他主题编辑会使预留 revision 失效；现仅在目标行及原索引均不存在时重新持久化预留 revision，已有同 ID 数据仍严格校验 |

回归文件为 `desktop/topic_removal_review_test.go`。并发窗口使用明确 checkpoint
固定顺序，覆盖确认后与正式归档提交前两处重命名，以及持久化操作重放；
不以 race 检查代替生命周期断言。

复查后 Desktop 相关回归及 `-race`、workspacestate/draftstate 的 `-race`、
golangci-lint、repolint 和生成契约一致性检查通过。此次复查未改变前端代码，
前端类型、构建及资源预算结果沿用前述验证。
重新构建 Go 服务后，macOS 原生冒烟的七项断言全部通过，renderer 错误列表为空；
覆盖草稿取消／丢弃、项目切换、侧栏归档、关闭重启恢复和迟到重复请求。

## 证据边界

PR preflight now covers both generated RPC contracts and the root Desktop
inventory. Production payload validation uses the Electron shell and a full
40-character `REASONIX_COMMIT`, matching CI rather than the local short-SHA
default. Topic-removal mock commands share the lazy lifecycle module, keeping
their implementation off the startup path without changing the budget.

PR 预检同时覆盖 RPC 契约和根模块 Desktop inventory。生产资源验证使用
Electron shell 和完整 40 位 `REASONIX_COMMIT`，与 CI 输入一致。
主题移除 mock 接口复用按需加载的生命周期模块，未提高本 PR 的资源预算。

- 已确认源码／集成层旧空主题删除问题，并完成 macOS 开发版原生 UI 回归。
- 未取得反馈用户的 1.38.11 原始环境、日志或同安装包复现，不能认定
  `no sessions to archive` 就是原反馈的唯一根因。
- 原生冒烟不调用真实模型服务；首条提交争用、未知结果和取消语义由
  生命周期／存储测试覆盖，不宣称完成真实提供商首发端到端验证。
- Windows、Linux 原生安装包、签名／发布、远程服务协议升级均不属于本次
  已验证证据。远程协议未调整，只做了前端防误路由回归。
- 本次复盘沿用既有“真实 RPC 完成判据”和“恢复后验证列表投影”的规则，
  未添加重复的技能条目。
