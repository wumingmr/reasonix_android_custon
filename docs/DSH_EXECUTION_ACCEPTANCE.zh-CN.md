# Harness 机制改造验收报告

验收基线：Reasonix `main-v2@986a6bc967`；参考行为：DeepSeek Harness `master@c291e7961a`。验收日期：2026-09-13。

## 结论

新机制已默认启用，没有新旧执行语义开关。结构化文件修改由宿主管理的实时文件观察和版本检查保护；读取范围、证明回执、操作结算、Auto Guard 和未知副作用恢复状态不再参与工具准入或完成判断。旧字段和旧恢复终态只用于兼容读取与历史展示。

## 七项问题行为

| 问题 | 验收操作 | 结果 | 覆盖层 |
| --- | --- | --- | --- |
| #9994 | `read → edit → bash → edit` 连续执行三轮 | 通过；成功编辑刷新观察，bash 不消耗或冻结文件观察 | Agent 集成、文件工具、macOS、Windows 11 |
| #9995 | 编辑后执行只读 git/bash，再次编辑 | 通过；命令不建立文件证据债务 | Agent 集成、macOS、Windows 11 |
| #10067 | 中文路径和 CRLF 文件读改跑再改 | 通过；路径和编码路由保持一致 | 文件工具、macOS、Windows 11 |
| #10103 | 文件移动和删除穿插在连续编辑流程 | 通过；移动不覆盖目标，成功后更新源/目标观察 | 文件工具、Agent 集成 |
| #10053 | 大文件只读一个窗口后运行命令并结束 | 通过；没有全文债务、强制补读或 final gate | Agent 集成、窗口读取 |
| #10085 | 同批 `read → edit → edit → bash` | 通过；按实际执行顺序同步更新观察 | Agent 批次集成、macOS、Windows 11 |
| #10153 | 模拟副作用已经发生但结果丢失，随后检查状态并继续调用 | 通过；记录 `unknown`，终态为普通 `interrupted`，不要求用户确认、不禁用网络或相同调用，也不自动重放 | 控制器崩溃测试、turn ledger、Desktop 兼容 API |

## 文件新鲜度与并发

- 未观察覆盖返回 `FS_NOT_OBSERVED`；确认不存在时只允许不可覆盖的原子创建。
- 任意成功文本窗口可观察当前版本；连续成功写入直接推进版本。
- 同大小改写、恢复 mtime、权限变化、文件替换和别名访问会使旧观察失效。
- 两个写者基于同一版本竞争时只有一个能提交；并发创建不会覆盖先创建者。
- 本地写入保留临时文件、原子替换、权限和编码；缓冲区与磁盘使用不同目标身份。
- `edit_file`、`write_file`、`multi_edit`、`notebook_edit`、`delete_range`、`delete_symbol`、`move_file` 和 `read_file` 已接入统一观察状态或提交后状态更新。

## 中断、完成与兼容

- 工具进入主体前持久化启动事实；取消后已启动调用收束，未启动调用获得明确结果。
- 重启发现缺少可靠结果时补全一次 `unknown` 工具结果；当前运行不生成 `recovery_required` 或 `requires_user_decision`。旧值仍可解码和只读显示。
- 恢复上下文只提供一次有界事实和检查建议；文件当前状态不会被当作旧调用成功证明。
- `complete_step`、`review_report` 和读取策略回执不再发现；旧调用返回普通 `tool_retired`。
- 恢复动作端点返回 `tool_recovery_retired`，不会确认、拒绝、检查隐藏参数或重放操作。
- `todo_write` 只校验字段、状态、层级和稳定 ID；完成状态由模型显式更新。
- 连续重复调用仅在第 3、5、8 次提醒，不拒绝调用。

## 平台与产品验证

下表记录 `3f7350f6e` 的初始验收，并未证明所有发布交错都正确。
PR 评审发现并修复了下一节列出的遗漏；这些发现修正此前对文件新鲜度和
原子发布过于绝对的结论。

| 环境 | 验证 | 结果 |
| --- | --- | --- |
| macOS | 根模块完整 Go 测试与 vet；Desktop、SDK 独立 Go 模块；契约、golden、缓存和仓库静态检查 | 通过 |
| Desktop 前端 | 完整前端测试和生产构建 | 通过 |
| Desktop 壳 | shell 测试 206 项；Electron 布局场景 57 项 | 通过 |
| Windows 11 虚拟机 | 原生 volume serial/file index/handle identity；同大小变化；真实 ACL `ChangeTime`；文件观察、并发创建/编辑和七项 harness 场景 | 通过 |
| Desktop 历史兼容 | 旧恢复卡只读、无动作按钮；当前工具卡保留未执行/失败/中断/未知事实；继续输入不受阻 | 通过 |

Windows 验证在本机 Parallels Windows 11 中原生执行，不以交叉编译代替。测试副本和临时产物已清理。

## PR #10223 评审修正

初次评审基线：head `3f7350f6e`，当时的 merge-base 为 `104792af2`。
本次冲突整合以 `main-v2@4daa815be`（#10209）为验证基线。
实现参考 DSH 的 `fs-observation-policy`（会话持有观察、按观察存在状态确定写入意图）
和 `fs-local/src/fsio.ts`（先暂存、再不可覆盖发布），保留 Reasonix 的 Go、编码和缓冲区适配。

| 评审版本中的缺陷 | 修复与回归依据 |
| --- | --- |
| 通用 `read_file.Execute` 未登记观察 | 与 `ExecuteRead` 共用有界读取入口，同时保留外部读取根的显示路径脱敏 |
| 已观察文件被删除后，覆盖操作变成盲目创建 | 保留已观察存在的写入意图并返回版本过期；不存在路径也解析已有符号链接祖先 |
| 磁盘观察可用于缓冲区写入 | 已存在文件只通过实际提供内容的路由提交 |
| 相同内容文件替换绕过摘要复查 | 同一句柄获取内容和原生元数据；写入前同时比较身份、版本、权限和摘要 |
| Linux `Ctim` 字段未被仅匹配 `ctime` 的逻辑识别 | 同时识别 Unix 的两种字段名；增加跨平台纳秒变化测试，并保留 Linux 原生过期编辑测试 |
| 直接创建最终路径暴露半成品；Windows EXDEV 可回退非原子复制 | 完整暂存并 fsync 后不可覆盖创建；覆盖使用禁止复制回退的严格替换 |
| 替换 inode 后修改锁身份改变 | 同时持有原生身份锁和稳定路径锁，按全局顺序加锁；测试持锁期间文件替换 |
| 检查后普通 rename 可覆盖并发创建的移动目标 | macOS/Linux/Windows 使用原生不可覆盖重命名；跨设备复制先暂存再以不可覆盖硬链接发布 |
| 同会话模型运行时重建丢失观察 | 控制器生命周期迁移时复制实时观察；连续编辑之间执行真实 `git --version` 仍可继续 |
| `complete_subtask` 仍有宿主证明裁决 | 删除裁决；保留可选模型报告并与执行事实分开展示；普通最终回答也可结束 |
| 退役门禁留下无调用函数和状态 | 删除闲置 shell 写判断、完成补救、批次结果重写、审查转储及预算/governor 辅助逻辑；保留历史数据字段；golangci-lint 零问题 |

回归测试位于 `internal/tool/builtin/harness_review_test.go`、
`internal/fileops/observation_review_test.go`、`internal/fileutil/atomicwrite_test.go`、
`internal/agent/harness_review_test.go` 和 `internal/agent/complete_subtask_test.go`。
Windows 原生复跑覆盖文件身份/ACL 变化检测、发布、移动、观察隔离、真实 shell 连续执行
及可选子任务报告。退役旧证明/读取策略 schema 并加入当前交付投影，会使 provider 工具
前缀在升级后变化一次；重生成基线与稳定扩展缓存守卫通过，后续启动保持稳定。实时观察
不进入持久化数据。

上述修复不提供通用外部进程 CAS、ACP 原子条件写或 exactly-once 副作用保证。
本轮没有把初始前端与实际壳测试重新标为新 UI 证据，也没有声称完成延迟基准测试。
是否可合并仍需确认推送版本的 GitHub 检查终态。

## 删除与保留边界

已删除全文读取债务、批次证据冻结、source-token 授权、锚点阅读范围影子状态、操作 prepared/applied/settled 状态机、完成证明门禁、Auto Guard reviewer、恢复确认动作、重复/无进展拒绝器、不再被调用的 shell 证明预检代码，以及可能重新激活这些门禁的旧运行时开关。

保留 Goal、Plan 审批、普通权限策略、沙箱、检查点、多代理、工具调用配对和执行事实展示。旧 provider/session 字段、`recovery_required` 枚举及前端识别只承担旧历史兼容，当前运行路径不写入或激活它们。

## 保证范围

窗口读取只表示观察过该版本，不表示全文审阅。bash、MCP 和外部程序不会授予文件观察；它们造成的文件变化由下一次结构化修改发现。ACP 缺少原子条件写接口，本地发布前检查也无法约束不遵守进程内锁的外部写者，因此不承诺通用跨进程 CAS。未知外部副作用没有宿主级 exactly-once 保证。
