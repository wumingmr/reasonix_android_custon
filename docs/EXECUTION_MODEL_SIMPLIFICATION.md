# 执行与完成声明 / Execution and completion reports

现行文件观察、调度和中断恢复语义见
[Harness 风格执行机制迁移](DSH_EXECUTION_MIGRATION.zh-CN.md)。

Reasonix 将任务判断、执行控制和运行记录分开。模型负责规划、执行用户要求的检查并判断任务是否完成。宿主负责权限、可靠执行、持久化、取消、显式预算和已激活 Goal 的续跑。结果面板陈述观察到的事实，不认证代码质量。

## 行为变化

- 普通回合在模型正常结束后结束。未完成待办、修改文件数、鉴权或迁移路径、缺少测试或审查不会触发质量门禁或追加回合。
- 用户的验证要求和项目检查说明仍是任务上下文；它们不再被编译成宿主验收义务。
- Plan 审批前仍禁止写入，包括 Yolo、代理工具和子 agent。批准后按计划及用户意见执行，待办由模型更新，不需要逐步签收。
- Goal 的生命周期由 `get_goal`、`create_goal` 和带精确 ID／revision 的 `update_goal` 管理。目标保持 `active + armed` 时，运行时空闲驱动器自动接纳下一轮；模型无需逐轮提交继续回执。`complete` 结束目标，`blocked` 停止续跑。无独立 evaluator，不从自然语言猜测完成。
- 完成声明不会覆盖测试失败，也不会将未完成待办批量改为完成。取消、权限等待、错误和显式预算边界不会被当作正常完成。
- `complete_step`、`review_report` 和读取策略回执已退役。旧调用返回普通 `tool_retired` 结果，不改变待办或恢复状态。

## 记录与兼容

新结果使用可选字段 `assessmentKind: "facts"`，旧 `verdict` 字段为 `unknown`。命令退出码、失败、中断及检查后又发生修改等事实独立展示。`unknown` 不代表失败或待验收。模型的完成和验证声明不改变实际执行记录。

历史回执和质量评估保留为历史资料，旧检查点不再阻塞当前任务。`/continue-checks` 仍是显式检查请求；有可用检查点时只消费一次，没有时正常发起检查。恢复和分叉只加载 Goal，显式启动或恢复后才允许自动续跑。

旧配置和线路字段保持兼容。数据可读不代表降级后的旧程序会遵守新执行语义。连接旧远端时仍展示远端实际状态，升级服务才能移除其旧策略；客户端不后台切换远端策略。

必要的工具发现、`update_goal` 与 `todo_write` 说明及 Plan／Goal 指导文字变更会带来一次升级缓存前缀变化。`todo_write` 不再要求串行推进或先签收当前项。同版本保持工具顺序和序列化稳定；不迁移或重写模型可见历史，不改变压缩策略。

## English

The model plans work, performs requested checks, and judges completion. The host enforces action permissions, executes reliably, persists state, honors cancellation and explicit budgets, and continues an activated Goal. Result panels show observations and model declarations rather than a host quality certification.

Ordinary turns end when the model ends normally. Pending todos, changed-file counts, sensitive paths, missing tests, and missing reviews no longer create quality requirements or extra turns. User and project verification instructions remain in task context.

Plan retains its preapproval write boundary, including Yolo, proxy tools, and subagents. After approval, execution follows the plan and feedback without sequential evidence signoff. The model updates todos.

Goal lifecycle is managed through `get_goal`, `create_goal`, and exact-ID/revision `update_goal` actions. While a Goal remains `active + armed`, the runtime-idle driver admits the next round without a per-turn continuation receipt. `complete` ends the Goal and `blocked` stops continuation. There is no separate completion evaluator or prose-based completion detection. Lifecycle actions cannot overwrite actual failed checks or unfinished todos. Cancellation, permission waits, errors, and explicit resource pauses remain execution boundaries.

`complete_step` is retired from discovery. Compatible old calls receive a
normal `tool_retired` result and never modify todos.

New result records carry optional `assessmentKind: "facts"` and legacy `verdict: "unknown"`. This is not a failure verdict. Actual command outcomes, interruptions, and changes after checks remain factual. Historical quality assessments and receipts remain historical; old checkpoints no longer impose quality gates. Old `/continue-checks` recovery actions return a stable retirement error and do not replay checks.

Restored and forked Goals require explicit activation. Legacy wire formats remain readable; downgrading does not guarantee the old executable follows the new semantics. Old remote services retain their actual state and require a server upgrade; clients do not silently change remote policies.

The necessary discovery, `update_goal`/`todo_write` descriptions, and Plan/Goal guidance edits cause a one-time prefix change on upgrade. `todo_write` no longer requires serial progression or prior signoff. Tool ordering and serialization remain stable within the version. Provider-visible history and compaction policy are not rewritten. These structural changes do not establish an improvement in model completion rate or defect rate.
