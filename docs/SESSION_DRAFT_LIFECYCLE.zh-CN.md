# 本地新会话草稿生命周期

Reasonix Desktop 将本地手动新建的会话作为编辑草稿，直到第一次执行被持久化接收。打开草稿不会创建 Session、Topic、Controller、会话租约、MCP 运行时或左侧会话列表条目。首次提交创建正式 Session 后，尚未生成标题的过渡条目使用本地化的“新建会话”（之后进入正常的预览/标题投影）；`session-id:...` 这类内部 canonical route 永远不能作为展示名称。

## 身份与存储

当前会话页面要么是草稿目标（`workspaceId`、`draftId`），要么是正式 Session 目标（`SessionRef`、`tabId`）。草稿不会伪装成 tab，也不会借用 TopicID。

草稿保存在 Reasonix 用户状态目录下的 `desktop/drafts-v1.sqlite`。schema v4 包含活动草稿、带版本的冻结提交操作、冲突副本和上次可恢复的草稿页面。每个本地 Workspace 只有一个活动草稿；全局 Workspace 另有一个独立草稿。

持久化内容包括输入文字、结构化 invocation、附件与工作区引用、粘贴块、选中文本引用、历史 Session 引用、模型来源及兼容镜像、effort、模式、审批、质量下限、Goal 及 MCP 选择。Controller、凭据、预览 URL、待处理粘贴和提交中 UI 状态不写入草稿库。

编辑同步更新应用级 store，并以 250ms 防抖和按 DraftID 独立串行的 revision CAS 循环保存。切换页面不会等待失败或冲突的保存；源草稿继续在后台保留，并在项目树显示未保存、错误或冲突状态。提交必须等待捕获的源编辑版本确认保存；Electron 正常退出会等待所有已加载草稿、附件任务和恢复目标写入。失败或冲突会取消正常关闭；崩溃恢复边界是后端最后确认的 revision。

打开草稿数据与提交页面恢复目标是两个独立步骤。每次导航在第一个 await 前领取共享 navigation intent，因此迟到的草稿打开不能安装页面，也不能覆盖下次启动恢复目标。

## 首次执行

renderer 在展开历史引用等异步工作前捕获 DraftID、Workspace、generation、内容、设置和 navigation intent。后端先持久化这份带版本的不可变快照，再创建运行时：

```text
活动草稿
  -> reserved（OperationID + SessionID + TopicID + SubmissionID）
  -> starting（创建或打开唯一 Session，并绑定 Workspace）
  -> dispatching | dispatching_shell
  -> accepted（持久化 submission receipt）
  -> 草稿 converted
```

该流程不调用 `EnsureBlankTab`。重试复用预留的 Session 与 Topic。终态失败后的新操作会生成新的 OperationID 和 SubmissionID，但继续使用原 SessionID 和 TopicID。Workspace 的 pending-create 清理带 OperationID 条件，迟到的旧操作不能删除新操作的预留。

普通消息、结构化 invocation、初始 Goal 和 shell 命令共用此流程。新 shell 提交在命令派发前持久化接收回执；旧派发标记无法证明是否启动时仍保持未知。无法确认结果的 shell 或模型提交都不会自动重放。

未显式选择模型的新草稿实时继承设置中的当前默认模型。持久化的
`modelSource: "default"` 用来区分继承与模型选择器或 `/model` 的显式选择；此时
具体 `model` 字段只是供旧版本读取的兼容镜像，不参与可编辑草稿的语义摘要。模型
目录或设置刷新会更新页面展示的有效模型，但不会把草稿标脏。显式选择会写入
`modelSource: "explicit"`，此后模型保持固定。

前后端的草稿比较都忽略继承模式下的具体模型镜像，包括保存回执恢复和发送前校验。
模型刷新、保存和重新打开共享读取版本保护，迟到响应不能覆盖更新的模型投影。

首次提交会再次解析当前有效默认模型，完成校验和规范化，并在预留 Session 前将其
冻结到 snapshot v5。已冻结或显式选择的模型都是严格执行选择：模型失效会在预留
Session 前报错，绝不回退到另一模型。插件模型引用交由运行时解析，不可用时在原
引用上失败。重试会把操作中冻结的设置应用到同一个预留 Session 身份。
请求指纹在模型别名规范化前计算，原请求重试仍能命中相同操作。恢复待继续的操作时，
输入框和模型列表展示冻结模型；取消或终态失败重新开放编辑后，才恢复继承实时默认值。

只有接收回执与草稿转换提交后，renderer 才切换到正式 Session。随后由 transcript snapshot/follow 补齐 RPC 返回前已经产生的事件。后台项目完成创建只更新项目状态，不会抢占当前可见页面。

## 恢复与取消

发送时同步锁定源草稿，先保存捕获版本，再展开历史引用。请求身份和源快照摘要将内容、设置与持久操作绑定。Begin 响应丢失时先读取状态核实，不直接解锁或分配新提交。恢复草稿同时恢复原操作，提供明确的“继续创建会话”入口；读取不会创建 Controller 或执行任务，结果未知时继续查询。

取消先记录 `cancel_requested`，worker 停止前保持编辑锁。跨进程执行锁防止另一个进程将活跃 worker 当作已中断；短时发布锁协调取消与 Controller 发布。重试设置在执行准入保护下应用到真实 Controller，派发时再次校验运行时身份。

附件完成始终结算任务登记，即使 generation 已变化；只有当前代结果可以写回内容。丢弃确认捕获所属项目和版本，导航不会改变删除目标。正常退出还会等待本地提交准备获得持久操作记录。

| 持久化阶段或中断点 | 恢复行为 |
| --- | --- |
| 尚无操作记录 | 保留可编辑草稿，正常重新提交。 |
| 重启时为 `reserved` 或 `starting` | 标记为 `resume_required`；用户继续后复用原身份。 |
| Session 已存在但尚未绑定 | 将同一 Session 绑定到记录的 Workspace。 |
| Controller 启动失败 | 保留 Session 身份；终态重试继续复用。 |
| `dispatching` 且找到 receipt | 原子完成 accepted 与草稿转换。 |
| `dispatching` 且找不到 receipt | 标记 `dispatch_unknown`，不重放。 |
| `dispatching_shell` 被中断 | 核实持久接收回执；已接收则转换为正式会话。缺乏证明的旧派发标记保持未知，不自动重新运行命令。 |
| 已 `accepted` 但草稿仍 active | 仅补齐转换，不再次提交。 |
| Session 被外部归档或删除 | 停止恢复并报告生命周期冲突。 |

取消以 OperationID 为目标。dispatch 前只取消该操作及其匹配的 Workspace 预留；持久接收后转为取消正式 Session，不会删除 receipt 来伪装“从未执行”。

## 命令、附件与能力

- 草稿中的 `/new`、`/clear`、`/compact` 等依赖历史的管理命令不可用，也不会创建 Session。
- `/model`、`/effort` 修改草稿设置；`/theme` 直接修改外观。Goal、技能、自定义命令和普通输入进入统一首次执行流程。
- 已配置 MCP 从 Workspace 配置投影，不创建 Controller 或启动进程。选择结果按草稿持久化；Controller 启动时再次校验实际能力。
- 附件操作显式携带 composer target 和 Workspace root；即使切到另一项目，异步完成也直接写回捕获的 DraftID。保存未完成时禁止提交。读取会拒绝路径逃逸、符号链接文件和符号链接附件目录；缺失文件继续作为可修复引用显示。

## 兼容性

### 一次性历史空会话整理

升级后的首次启动会冻结一批已经属于本地项目或全局 Workspace 的默认名称
Session 与旧 Topic 占位。后台 worker 只在 Session 迁移、草稿操作核对和 Tab
恢复完成后运行。冻结之后新增的身份不会进入该批次，尤其不会包含草稿预留或
重试创建的 Session。

只有完整核实 canonical 或 legacy 全部附属数据后，才能把候选移入回收站。
以下任一信息都会保留会话：user、assistant 或 tool 消息，模型或 shell 的已接收
提交，Goal、固定上下文、inbox、后台任务、检查点、恢复状态、派生关系、活动草稿
操作、运行时，或恢复页面中的未发送输入。即使消息正文为空也算使用记录；仅有
system 初始化和空的派生容器不算内容。数据损坏、目录不可访问、格式不受支持或
来自未来版本时统一标记为 `unknown` 并保留，绝不把读取失败当成空。

默认标题只在去除首尾空白后精确匹配。有内容的会话保持标题、排序、身份和全部
数据，不执行编号改名。确认空的正式 Session 复用现有可恢复归档生命周期，且不
打开 fallback Controller。Topic-only 占位在 schema v1 整理记录中保留原 scope、
标题、顺序、pin 和分组位置，并在回收站中以无 SessionRef 的条目展示。恢复任一
类型都会保持原身份，并永久退出本批次，避免下次启动再次消失。
正在使用或暂时无法读取的候选继续保留在原项目中。回收站显示待处理数量和
“重新核实”操作；该操作只重试已经冻结的批次，不会吸收升级边界之后新增的身份。

独立的 `desktop/legacy-empty-session-cleanup-v1.json` 使用原子写、跨进程 worker
所有权、稳定归档 OperationID 和未知版本保护；旧版本会忽略该文件。旧 JSONL
迁移源即使对应的 canonical Session 被归档也继续保留，不做永久删除。

| 格式或接口 | 新版本行为 | 旧版本行为 | 结论 |
| --- | --- | --- | --- |
| Session v5 | 身份、事件和 transcript 协议不变。 | 不变。 | 兼容。 |
| Workspace registry v3 | 只登记正式 Session；创建操作仍可恢复。 | 现有 Session 可继续读取。 | 兼容。 |
| Draft SQLite v1 | 先迁移到 v2 增加预留 TopicID，再迁移到 v3。 | 无草稿版本不涉及。 | 已覆盖向前迁移。 |
| Draft SQLite v2 | 事务性升级至 v3；旧未完成操作只有在草稿 revision 能证明设置一致时才允许恢复。 | v2 实现拒绝写入 v3。 | 不推断执行设置。 |
| Draft SQLite v3 | `request_json` 保存带版本的冻结请求与设置；所有保存严格 CAS。 | 不支持草稿的旧版本忽略独立文件。 | 回滚保留草稿，重新升级可恢复。 |
| Draft SQLite v4 | 事务迁移增加请求身份、源摘要、操作版本和独立的展开后执行字节；快照版本独立于 schema 版本。 | v3 拒绝写入 v4。 | 保留原指纹、SessionID 和 SubmissionID。 |
| 草稿模型来源 / snapshot v5 | 新草稿以可选字段 `modelSource` 记录来源；继承默认值时保留具体模型兼容镜像，但只在提交时冻结当前有效模型。只对从未保存过的旧 revision-1 草稿安全标记为继承；已编辑旧草稿因来源不明而保留具体模型。已有 v3/v4 操作快照仍可恢复。 | 旧版本忽略 `modelSource`，继续使用有效的具体模型镜像；遇到未来 v5 操作快照会拒绝恢复，不会按旧语义执行。若旧版本重新编辑并写回草稿，之后升级会保守地把具体模型视为已固定。 | 不会静默替换用户的显式选择，也不会用推断模型恢复操作。 |
| 未知未来草稿 schema | 拒绝读写并保留文件。 | 不涉及。 | 不会静默降级覆盖。 |
| 升级时既有空 Session | 只把冻结批次中默认标题、可确认完全未使用的本地候选一次性移入回收站；有内容或无法核实的会话完全不动。 | 旧版本忽略整理 sidecar，仍可读取正式 Session 与回收站。 | 使用可恢复生命周期，兼容回滚。 |
| 历史整理 sidecar v1 | 保存冻结身份、判定、稳定归档操作、占位恢复元数据及恢复保护；未知版本停止整理并保留文件。 | 忽略。 | 不会降级覆盖或重新扩大候选范围。 |
| 旧运行时 API | `EnsureBlankSurface` 等保持原语义。 | 不变。 | 远程、IM、自动化、恢复和 worktree 流程保持隔离。 |

DraftID 和 SubmissionID 只属于宿主元数据，不进入 provider 请求；原有 prompt 与工具排序不变。

## 运维说明

诊断只记录内部 ID、阶段、复用/恢复次数，以及草稿打开、保存、Session 创建、运行时就绪和接收耗时；不记录正文、附件内容或凭据。

首期丢弃草稿不会自动删除附件文件。MCP prompt 展示使用现有配置或缓存信息，实际能力发现与校验在执行阶段完成。
