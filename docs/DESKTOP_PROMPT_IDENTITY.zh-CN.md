# Desktop 交互卡片身份

Desktop 决策卡片由创建它的 controller 所有。每个新的交互请求都携带所属
`hostId + sessionId`、会话 generation、prompt ID、turn ID、runtime epoch 和
请求类型。`kind` 标识 `ask`、`approval`、`plan`、`recovery` 或 `mcp`。前端还会
使用完整身份生成不可变的请求实例 key；审批请求会额外包含 approval generation
和 permission revision。组件局部状态及 Ask 草稿均按该实例 key 保存。

前端通过 `ResolvePromptForSession` 提交完整目标。Desktop 在 App 锁内校验
标签的 host、session 和 generation 并固定 controller，释放锁后再执行 exact
resolve。controller 校验 runtime epoch、active turn、prompt owner 和 pending
状态，然后持久化 `PromptAnswered` 并唤醒原始等待方。旧绑定、旧 turn 或旧
runtime 的提交会被拒绝，不会被路由到替换后的 controller。持久化失败时只把
原请求恢复为 pending，用户可以重试。

prompt 请求和生命周期事件会暴露 `promptId`、`promptKind` 和 `turnId`。
Desktop 事件 envelope 会携带 tab 的 runtime epoch。缺少 turn identity 的旧
事件会标记为 `promptLegacy`，只能通过兼容路径处理。

`AnswerQuestionForTab`、`ApproveTab` 和 `ResolveRecoveryTab` 等旧 host 方法
仍为旧客户端保留。新前端统一使用 `ResolvePromptForSession`，不会静默降级到
没有 fence 的方法。收到 stale 响应后，旧卡片会从当前决策面移除，并按 tab
请求一次 prompt replay；只有新的 pending identity 才能重新显示卡片。

扩展表单遵循同一原则，使用 `SubmitExtensionFormExact`。host 为每次发布生成
`formInstanceId`，在 hub 锁内固定通过校验的 sidecar client，释放锁后才调用
sidecar；完成结果只能修改该表单实例。相同插件 surface 再次发布会获得不同
实例，因此旧完成回调不能关闭或恢复替换后的表单。

远程 Serve 分别声明 `interaction-target-v1` 和
`extension-form-instance-v1`。缺少对应能力时，当前客户端仍允许查看会话，但会
禁用相关卡片或表单并提示升级，不会发送不安全的旧提交。这些字段只用于传输和
界面状态归属，不需要迁移持久化会话数据。
