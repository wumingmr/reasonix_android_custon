# 向 DSH 运行机制对齐

本文件固定新版会话的可执行契约；旧字段只用于历史展示。英文版
[DSH_RUNTIME_ALIGNMENT.md](DSH_RUNTIME_ALIGNMENT.md) 给出同一契约及回归矩阵。

## Todo

`todo_write` 只接收必填的 `todos` 数组。每项只能有 `content` 和 `status`；
内容 trim 后非空且列表内唯一，状态只能为 `pending`、`in_progress`、
`completed`。每次提交整表替换，空数组合法；允许多个进行中项、仅 pending、
乱序完成、删除、重排和重新规划。成功结果是含规范化完整列表和计数的 JSON，
失败不改变旧状态，展示压缩和结果去重不得替换这份状态事实。

todo 的生命周期是一个宿主真实回合。顶层 `turn/start` 成功提交后清空；模型
工具轮次、压缩、补充消息、批准和 Ask 回答不清空。回合完成、失败或取消后保留
最后一次成功列表用于展示，下一回合开始时再清空。Goal 续跑属于新回合。

新版执行路径退役层级、`activeForm`、`step_id`、`complete_step`、宿主自动推进、
Plan 自动播种、Goal 恢复 todo、完成前缀保护及完成门禁。隐藏的
`complete_step` 墓碑只返回指向 `todo_write` 的退役错误；历史记录仍可查看。

## 运行与交互所有权

会话级 turn-loop 是执行权威：唯一持有前台准入、取消、当前回合身份、FIFO
待处理输入和 level-triggered wake。`session.Runtime` 是持久化权威：会话身份、
单写者租约、内存接收和后台落盘。持久日志只记录已发生事实，进程重启后不得从
旧 running 记录恢复出并不存在的执行器。运行阶段为 `idle`、`executing`、
`cancelling`、`finishing`、`recovery_required`、`closed`，兼容布尔字段由这些
状态和当前所有者派生。不再保留 Activity 许可，也不再在 Runtime 上设置第二套
准入门。

Stop 以 session 为目标，UI turn id 只能作为诊断信息，不能成为取消前提。取消
信号先到达已绑定的 turn-loop，再做磁盘、通知和回调清理；每一轮使用全新取消
上下文，Stop 不得污染下一轮。已有 15 秒宽限统一覆盖所属工作；无法收敛时保持
`recovery_required`，迟到结果不得恢复旧回合或写入新代际。Runtime 以精确
generation 绑定 turn-loop，旧 Controller 解绑不得清除新代际的控制权。

Ask、批准、Plan、恢复和 MCP 决策共用 `PendingPromptOwner`。身份绑定 request、
类型、turn 和 runtime epoch；回答只有一个胜者，过期回答明确拒绝；回调不在
注册表锁内执行。运行快照从该注册表派生 pending 状态，不再只看批准弹窗。
回答事件记录 `answered`、`rejected` 或 `cancelled`；Plan 回答与对应状态在同一
逻辑批次提交。回合终结会把仍未终结的请求一并关闭，重启后不会重新出现可回答授权。

## 能力目录

`skill.Store.Snapshot` 是发现边界。每个代际提供不可变稳定顺序候选和 O(1) 名称
索引；并发冷请求共享一次扫描，单个等待者取消不影响其他等待者。失效时保留上
一个完整快照，新快照只在完整发现后发布，最多跨两个代际重试。创建、编辑、
删除及根目录变化会使快照失效。

`use_capability search` 每次固定使用一份目录和 MCP schema 快照；skill 参数契约
经索引读取，不会按结果逐项重扫目录。list 默认 50、最大 100，游标绑定目录
指纹，失效后要求从头开始。发现不得连接 MCP 或调用 `tools/list`。
Windows 使用可取消的有界轮询代际，避免 `ReadDirectoryChangesW` 注册阻塞关闭；
其他平台继续使用 fsnotify。两条路径都覆盖外部修改和缺失根目录的后续创建。

## 兼容与缓存

todo schema 和能力分页形成一次明确的稳定前缀升级。同版本保持工具顺序、schema
字节及 delivery marker 稳定；运行状态、时间戳、目录代际和 todo 内容不得进入
系统提示。旧 todo、签收、Goal todo 和 dismiss 记录只读保留，继续工作时不激活；
新版 Goal 不保存 todo，Plan 不生成 todo，前端 dismiss 仅为当前挂载期展示偏好。

## v3 会话边界

`internal/session` 定义所有权切换后的 codec `reasonix.session.linear/v3.1`。
旧原型 `reasonix.session.events/v3` 和预览格式 `reasonix.session.linear/v3` 不能直接用于
执行，只能通过受限导入器转换；
未知必需事件、完整损坏记录或无法解释的历史替换都会保留原件并拒绝继续执行。
一个会话只有一个活动写句柄；分叉和编辑重发创建独立子会话，不在同一日志维护多
head。一个物理 JSONL 记录保存一个完整逻辑批次，事件获得连续序号。未换行的尾
记录不会被冷读部分重放；写句柄取得独占租约后先逐字节保留该尾部，再截断回最后
一个完整批次并继续恢复。完整损坏记录、序号缺口、未知 codec 和未知必需事件都按
失败关闭处理。

三层各自拥有明确事实：内存 `Session` 拥有类型化事件日志、序号分配、操作幂等表和投影；
`PersistenceBinding` 拥有待写队列、durable 水位和唯一排空链；物理 `Store` 只拥有 JSONL
字节、写者租约和可重建偏移索引。句柄不再保存投影、操作表或已接受提交列表，因此无法
从磁盘结构反推业务状态。`Session.PrepareBatch` 在取得提交锁之前完成载荷复制、
schema 校验和操作摘要；摘要只覆盖调用方提供的字段，因此重试同一逻辑批次保持幂等。
`Session.CommitPrepared` 随后在一把短内存锁内完成幂等检查、序号分配、整批追加和投影
替换。批次在提交锁释放后才进入绑定队列，入队过程不做任何文件 I/O。

新建 API 只在内存 Session、写句柄和不可变 session ID 已经发布后返回。标题与模型
选择（包括连接 revision）分别由 `session/title`、`session/config` 事件维护；Agent
重建只替换模型上下文并追加配置事件，不创建第二个 Controller 与同一写者竞争。目录
索引和旧 model sidecar 都不是新版模型选择的事实来源。

每个已发布 Runtime 由宿主持有，且只有 `RuntimeOwner` 能终止准确实例。`Service.Open`
以 `ClientBinding` 附着客户端，不交出关闭权限，因此附着失败只能撤销调用方自己的绑定。
Controller 只取得可发送和观察的 `ClientBinding`；
关闭标签页或连接只解除自身绑定，不能关闭共享写者或取消活动回合。最后一个绑定离开
后，空闲 Runtime 由宿主回收；仍在活动的 Runtime 继续收敛，结束后再回收。准备失败
和迟到清理回调只能丢弃自己持有的准确候选或实例。取消直接触达已绑定的
turn-loop，不先等待 Runtime、持久化或 UI 锁，因此回执不会被提交或磁盘操作阻塞。
会话事件只要求当前 Controller 仍持有写租约；Stop 不会撤销 `history/replace`、
`turn/end`、交互收尾或诊断写入。Session 先接受，兼容 ledger 和前端投影只在
Session 接收成功后推进。

`Append` 表示事实已被实时会话接受：先校验完整批次，再分配序号、保存不可变副本、
更新内存投影并通知观察者。它不表示已经落盘。第一份待写事件启动固定 200ms 批处理
窗口，后续追加不延长窗口；同一写句柄只有一条排空链。后台写入失败保留原批次并
暂停自动重试，下一次显式 `Flush` 才安全重试。写入或 fsync 结果无法确认时进入明确
的 uncertain 状态，不能重跑工具。

模型适配器调用前和顶层工具 body 前必须执行语义检查点 `Flush`；失败时下游调用次数
必须为零。Todo、批准、助手消息和 `turn/end` 只做内存提交，交给批处理和下一检查点。
idle 不代表 durable；导出、冷盘校验、写者交接和正常关闭必须显式等待 flush。实时
快照同时返回 event sequence 和 durable sequence，前者可以更大。

新版根目录是 `sessions-v4`。继续旧会话时把 transcript 与配对的预览事件目录作为一次
导入决策，并且**先分类、后发布**：先在 transcript 写租约下冻结源文件，再在目录锁和
writer 锁下冻结预览事件，两边都只解析冻结副本。预览中的结构化消息等于或严格包含
legacy 消息时采用预览，legacy 严格包含预览时采用 transcript；两边存在无法解释的新
工作时保持只读且不产生任何目标。只有来源判定最终确定后，才在同一文件系统的临时目录
构造并校验唯一 v3 会话，最后原子 rename 发布。迁移 ID 由源规范路径、head、摘要和目标 codec 确定；相同输入幂等复用，源变化生成
另一个目标。原文件逐字节复制到目标的 `legacy/`，未知内容不经结构体重编码；Goal
只导入目标、状态、预算等字段，不导入 todo 或自动续跑。旧未结束运行只作为历史，
不能恢复批准或活动执行器。

旧 schema-2 日志的每个可达 head 分别迁移。`legacyHeadId` 进入目标 ID 和迁移映射
键，因此两个旧 head 不会共享后续写入。目录列表只读取 manifest 头部、日志 revision（stat 加有界首尾采样）和可重建的标量
元数据，从不构建偏移索引。缓存条目与精确日志 revision 绑定，日志追加一个字节即使其
失效，无需重放。缺失元数据明确显示 pending，并由最多两个并发的流式归约任务重建。
暖列表不读取事件正文。冷历史分页采用流式校验读取，到达页上限即停止，不先把整份日志载入内存。

最终整体切换时 Desktop host RPC 提升到协议版本 5；Electron 壳直接使用嵌入 command
contract 的版本发起并校验握手，避免壳与服务各维护一份易漂移常量。Serve 同时声明
`execution-v2`、`session-history-v1`、`session-identity-v1` 和 `session-ownership-v1`。新版 Desktop 拒绝把运行
和取消命令发给缺少任一能力的远端，避免用路径身份或旧 RPC 模拟新版状态机。
