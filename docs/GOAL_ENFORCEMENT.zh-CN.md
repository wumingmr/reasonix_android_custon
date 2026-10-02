# Goal 模式：统一运行时生命周期与空闲续轮

Goal 模式按三个职责边界实现：版本化目标状态、模型目标工具、运行时空闲驱动器。它复用
`reasonix.session.linear/v3`、Session Service、Runtime、Activity 和统一顶层回合接纳；没有第二个
执行循环，也不再用每轮 `continue` 报告维持运行。

## 状态模型

每个会话最多有一个当前目标。持久状态由 `goal/state` 事件保存：

| 字段 | 含义 |
| --- | --- |
| `id` | 当前目标身份；替换目标时生成新 ID |
| `revision` | 生命周期 CAS 版本；创建从 1 开始 |
| `objective` | 完整长期目标文本 |
| `phase` | `active`、`paused`、`blocked` 或 `complete` |
| `maxGoalRounds` | 正整数上限；`null` 表示不限轮数 |
| `roundsStarted` | 已被统一入口接纳的自动目标轮数 |
| `blockedReason` | blocked 的机器原因码和说明 |
| `createdAt` / `updatedAt` | 创建和生命周期修改时间 |

`activation` 只有 `armed` / `disarmed`，属于当前进程的运行授权，不写入会话，也不随冷启动、
导入或 fork 恢复。轮次计数和资源统计不会增加生命周期 revision。

目标的唯一事实来源是 v3 投影。旧 Goal sidecar 与 AutoResearch 文件只在显式导入／兼容升级时
读取，运行时不会再写它们，也不会从它们恢复旧执行器。

## 模型工具

模型可见的稳定工具协议为：

- `get_goal()`：读取统一 GoalView；没有当前目标时返回 `goal: null`。
- `create_goal(objective, max_goal_rounds?)`：在直接人类回合创建并激活长期目标。省略或传
  `null` 表示不限轮数；不会覆盖未完成目标。
- `update_goal(goal_id, revision, action, ...)`：对读取所得的确切版本执行 `edit`、`pause`、
  `resume`、`complete` 或 `blocked`。

`continue` 已删除。目标保持 `active + armed` 就会在运行时空闲后自动进入下一轮；阶段总结、
普通 final 或未调用 `update_goal` 都不会让未完成目标自然停跑。旧
`update_goal(status=continue)` 会返回明确的协议退役错误。

编辑轮数上限时，字段省略表示不修改，`null` 表示取消上限，正整数表示新上限；零、负数、
非整数以及小于已接纳轮数的值都会被拒绝。编辑目标不会清零累计轮数。

## 权限与恢复

目标工具权限由宿主签发的执行身份决定，不从消息文本、历史 `user` 消息、导入文档或工具输出
推断：

- `create` / `edit` / `pause` / `resume` 只允许当前直接人类顶层回合。
- `complete` / `blocked` 额外允许当前目标的确切自动轮次。
- 自动轮权限同时绑定会话、runtime epoch、Activity revision、目标 ID/revision 和轮次。
- Planner、子 Agent 和迟到的旧 Activity 均拿不到父目标的修改权限。

冷恢复后的 active 目标和 blocked 目标可以在用户提出继续请求后，由模型
`get_goal → resume` 恢复。用户明确暂停产生的 paused 目标只能通过 UI 或命令恢复，模型不能自行
解除。complete 不能恢复；新长期任务应创建新目标。

## 自动续轮

Goal Round Driver 是空闲状态的轻量调度器，不持有跨轮 Activity。每次自动轮都走正常顶层回合
入口并拥有自己的 Activity：

1. 检查目标 `active + armed`、无待处理用户输入／交互、Controller 与 Runtime 均真正空闲。
2. 对当前会话、runtime epoch、Activity revision 和目标版本建立至多一个进程内预留。
3. 先执行 v3 `Flush` 检查点；失败时不调用模型并解除自动激活。
4. Flush 后重新检查身份、用户输入、取消和目标状态。
5. 通过统一接纳入口，把 `turn/start` 与新的 `goal/state` 写入同一逻辑 Batch。
6. 只有接纳成功才增加 `roundsStarted`。
7. 本轮正常收尾；目标仍 active + armed 时，新的空闲通知再驱动下一轮。

重复 idle 通知会合并。若用户消息在自动轮接纳前进入队列，自动预留失效并优先处理用户消息；
自动轮已开始则沿用现有 steer／cancel 行为。finishing、cancelling、recovery、Ask／审批等待阶段
都不能启动下一轮。

默认没有隐藏轮数上限，并用超过 256 轮的测试固定该差异。显式达到 `maxGoalRounds` 时进入
`blocked`，原因码 `round-limit`；必须提高或取消上限后才能恢复，累计轮数不重置。

自动轮的模型 `blocked` 至少要求已经接纳 3 个目标轮次；模型负责判断是否为同一持续阻碍，
宿主只执行轮数和权限硬校验。模型可在第一轮 `complete`，不依赖 todo 比例、额外评审模型或
readiness 门禁。

## 停止与资源边界

- 用户暂停会先 disarm，再取消当前自动目标 Activity；已接纳的自动轮取消后目标进入 paused。
- 模型／Provider 错误、持久化错误或结果不确定会停止自动调度，不伪装成 complete，也不自动
  重复副作用工具。
- 超时未收敛沿用统一 Runtime 的 `recovery_required`；旧代结果不能写入新代。
- 配置的正数 Goal token 预算由宿主统计自动轮真实用量；达到后进入 blocked，原因码
  `resource-budget`。恢复会增加一个配置的预算切片，不清空累计统计。
- 未配置目标轮数或 token 预算时没有对应的隐藏停止阈值。

Todo 是回合内规划工具：每个真正接纳的顶层回合开始时清空，同一回合的工具调用、压缩、steer
和交互回答不会清空；结束后保留最后一份供展示。新目标轮重新规划，长期进度来自目标、会话历史
和工作区成果。

## 持久化、迁移与能力

目标创建、修改、轮次接纳和 clear 都通过当前 Activity 或宿主串行控制 Activity 写入
`goal/state`。Append 表示 live projection 已接纳，Flush 才表示 durable；目标更新和回合结束
不额外逐次 fsync。模型调用与顶层副作用前继续使用统一 Flush 检查点。

旧会话继续工作时导入独立的 `sessions-v4` 副本，原件不变；旧版与新版不双写。未知必需目标
版本或损坏的必要数据会阻止自动运行。历史 activation 仅用于诊断展示，永远不会恢复执行授权。

RPC 能力目录使用 `goal-lifecycle-v2`。缺少该能力的远端明确拒绝目标操作，不回退旧 Goal API。
CLI、ACP 和 Bot 的运行观察器等待目标完成、阻塞、暂停、解除激活或宿主错误，不会把第一轮
`TurnDone` 当作整个目标完成。

## UI 与诊断

目标面板直接读取 GoalView，区分正在执行、已激活等待下一轮、未完成等待恢复、用户暂停、阻塞、
完成和持久化／运行时故障。UI 的创建、替换、编辑、暂停、恢复和清除都走统一目标服务；清除保留
历史墓碑并先收敛运行中的旧 Activity。

用户主动选择“诊断导出”时，桌面端本地读取或远端 `/goal-diagnostics` 都直接冻结并导出 v3
后端事件，而不是只导出前端已加载的列表。导出包含构建和协议信息、GoalView、运行时代次、
accepted／durable 序号、可推导的 activation 变化、续轮停跑原因，以及记录中完整的工具参数、
结果与错误；凭据和其他敏感内容按诊断导出隐私规则脱敏，无法取得的字段会明确标记。即使
Flush 失败，导出仍保留已接纳前缀并分别报告持久化状态和 durable 序号。

## 主要实现

- `internal/goal/domain.go`：版本化领域状态、CAS 转换、恢复与墓碑。
- `internal/goal/prompt.go`：每轮动态、JSON 转义的目标输入；不改变系统 prompt 前缀。
- `internal/tool/goal_lifecycle.go`：宿主签发的目标工具权限。
- `internal/tool/builtin/getgoal.go`、`creategoal.go`、`updategoal.go`：模型协议。
- `internal/control/goal_lifecycle_owner.go`：目标服务与 v3 Activity 提交。
- `internal/control/goal_driver.go`：空闲续轮、Flush、统一接纳与竞争处理。
- `internal/control/goal_diagnostics.go`：v3 诊断导出。

旧 `GoalTurnRecorder`、Controller 内部 continuation 循环和 `continue` 工具协议已经删除。
