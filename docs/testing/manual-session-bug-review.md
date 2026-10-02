# Manual session rollback: submission race review

2026-09-22 的补充检查聚焦正式会话输入持久化与发送、导航、重启核对之间的边界。
没有修改数据库 schema、旧草稿格式、会话事件或 provider 请求。

## 发现与修复

1. **登记提交时仍可修改输入。** `BeginSessionComposerSubmission` 已在 host 登记，但 renderer
   等待 RPC 返回期间还未锁定编辑器。后续成功结算会清掉该窗口内的新输入。
   现在发送前保存完捕获版本后，立即锁定登记阶段；登记响应丢失时必须读取持久状态核对。
2. **迟到结算覆盖新输入。** 持久回执核对可能先于原发送 RPC 返回，编辑器已允许输入下一条，
   旧回调仍按“accepted”无条件清空。另一个窗口恢复出的新版本也会受到同样影响。
   现在结算只更新不早于当前 revision 的状态，只替换该次捕获的输入版本，并采用 host 返回的实际内容。
   accepted 结算响应丢失后再次核对，也不会把已接收输入当作可再次发送的内容恢复出来。
3. **离开再返回后旧发送意图仍可执行。** 原校验只比较 TabID 与 SessionRef，相同页面再次挂载后
   无法区别旧操作。现在为每次挂载分配独立 owner，离开时撤销，返回不会重新授权旧操作。
   已经派发的工作仍按原会话结算；这一校验只阻止尚未派发的过时操作。

最初怀疑普通编辑会覆盖 Goal 标记；沿实际 `onPatch` 路径核实后未复现，未对此增加补丁。
新增回归同时检查 Goal 标记在编辑及断线恢复后保留。

## 验证

`session-composer-races.test.tsx` 使用可控 Promise 强制六种顺序，覆盖登记等待、回执先行、
页面离开再返回、登记响应丢失、结算响应丢失和跨窗口迟到快照。前三项在修复前稳定失败，
六项在修复后全部通过。原有输入隔离、退出屏障、CAS 冲突和未知提交测试也通过。

新测试由现有前端发现式测试入口自动收录，不需要手工注册，也没有延长时间限制或跳过断言。
额外验证包括生产/测试 TypeScript 检查、App 生命周期回归、仓库静态检查和生产构建。
main-v2 叠加验证固定在 `f26283afb2f5`，与历史兼容测试报告相同。

重建后的 macOS arm64 `v1.38.12-preview.1` 完整打包应用通过了三次独立新建、请求重试、
正常退出、图片恢复、强制结束壳后的输入恢复、归档和最后会话空白欢迎页验证。
打包应用验证使用隔离的合成数据目录；Windows/Linux 原生运行仍不在本机的已验证范围内。
本轮没有重复执行九版本完整历史矩阵；没有历史格式代码变更，格式覆盖及升级边界仍以
[历史兼容报告](manual-session-compatibility.md) 为准。两轮构建的摘要分开记录在
[验证结果](manual-session-compatibility-results.json)，其中 `submissionRaceReview` 对应此次修复。

## 模型列表目标回归

测试版反馈显示正式新会话的当前模型名称正常，但弹出列表为空。`Composer` 将统一输入
持久化目标中的 `draftId` 传给了模型选择器；正式会话在该字段保存的是输入缓存身份，
并不是旧草稿 ID。因此调用了 `ModelsForDraft`，查询不到记录后返回空列表。

模型查询现在与附件等操作共用显式的 `bridgeTarget.kind`，正式会话查询 `ModelsForTab`，
仅旧草稿恢复页查询 `ModelsForDraft`。不修改供应商访问权限、凭据、会话模型或存储格式。

`composer-model-target.test.tsx` 挂载真实 Composer 并启用正式输入持久化，修复前稳定捕获
错误的草稿查询，修复后覆盖列表显示、选择模型、会话切换和旧草稿列表。模型刷新与乱序
测试、输入持久化/提交竞态测试、旧草稿展示测试及 TypeScript 检查均通过。

macOS arm64 完整打包及严格签名验证通过。使用原测试数据目录正常退出并启动修复包后，
实际新会话的模型菜单显示已配置模型及“当前模型”标记；配置和数据均未迁移或重写。

## 回滚接线审计 / Rollback wiring audit

沿新建入口、显式目标、编辑保存、附件准备、发送接收、历史恢复和退出屏障逐项检查。
以下问题已修复；普通发送、旧草稿恢复、远程与后台导航仍由原有 owner 管理。

| 接线错误 / Broken boundary | 修复 / Repair | 回归 / Regression |
| --- | --- | --- |
| 显式 SessionRef 的文件浏览丢失 Controller，外部目录不可见 | 同时验证 TabID/SessionID，保留对应 Controller；无标签恢复仍读 canonical 路径 | `composer_target_wiring_test.go` |
| 附件捕获忽略 SessionRef | 捕获时核对正式身份，拒绝错配目标 | 同上 |
| 新图片只保存 RAM 草稿凭证，重启无法恢复 | 保存原始图片副本；独立输入记录只保存 durable path；发送前取得当前运行时凭证，沿用 typed image admission | `attachment-restart-wiring.test.ts`、`session-composer-wiring.test.tsx` |
| 外部目录 token 重启后没有重新登记 | 按已保存的原目录重新登记，要求返回相同 token；目录改变或不可读时报错保留输入 | `attachment-restart-wiring.test.ts` |
| 明确拒绝误判成未知提交，输入锁死 | 共用结构化拒绝分类；容量、只读、启动未就绪、不可读图片及发送前检查保留可编辑输入；传输丢失仍保持未知 | `session-composer-wiring.test.tsx` |
| 追问补查回执未结算正式输入 | 补查成功同时结算原 SubmissionID；已结算的重复补查不能清除新输入 | 同上及 `session-composer-races.test.tsx` |
| 图片明确拒绝后复用已结算 ID | 仅明确拒绝释放本地待提交身份；未知结果继续保留原 ID | 附件身份、图片拒绝及持久化拒绝回归 |
| Goal 恢复绕过待核实状态或覆盖活动 Goal；停止未保存取消状态 | 恢复前检查历史、提交及活动 Goal；停止同步保存标记；Goal-only 输入也参与历史推进检查 | `composer-goal-recovery.test.tsx`、`session_draft_content_test.go` |
| `/model` 返回 false 仍清空输入 | 未实际切换视为明确未提交，成功后才结算 | `composer-router-rejection.test.tsx` |
| 恢复的技能 ID 与新标签计数器重复 | 分配 ID 时排除当前输入中的已有身份 | `rich-composer-restored-identity.test.tsx` |

旧启动测试仍要求自动恢复或创建草稿，已改为验证“不抢占启动、不隐式创建、显式恢复可用”。
该调整保留了用户数据恢复能力，并与回滚后的产品约定一致。

### 数据边界 / Data compatibility

| 字段/格式 | 旧数据 | 新写入 | 旧版本读取 |
| --- | --- | --- | --- |
| Session v5、注册表 v3、旧草稿库 | 沿原读取及迁移链 | 无格式调整 | 不改变此前声明的兼容边界 |
| 独立 session-ui 输入附件 `recoveryPath` | 缺省沿用已有路径，保留原记录 | 可选字段；有 durable source 时不序列化 RAM 凭证及预览 URL | 1.38.10/11 不读取独立输入库；再次升级可恢复 |
| `goalDraft` | 缺省为 false | 原有字段，只修复消费者与内容判断 | 无新 schema 或迁移 |
| 外部目录引用 | 沿用 token 与 displayPath | 无 token 格式或输入文本变化 | 无共享文件改写 |

旧测试预览中已经只留下 RAM 图片凭证、且原进程已结束的记录，无法凭空还原图片字节；
记录保持原位，需要重新附加图片。新修复阻止后续产生这类记录。目录已删除或权限失效时同样
保留原引用并显示错误，不发送不完整内容。

The audit preserves canonical session/history formats and provider submission routes. Image recovery
stores original bytes in the existing attachment directory, then reconstructs a runtime credential;
it does not persist a controller handle or replay a model request. Unknown submissions remain locked
until reconciled. External-folder restoration validates the original token, and repeated receipt
checks cannot erase subsequent edits. Historical-version evidence remains in the compatibility report;
native Windows/Linux qualification is still unavailable on this macOS host.

### 验证记录 / Verification evidence

- 生产与测试 TypeScript、React hooks、host 契约、仓库静态检查及生产包预算均通过。
- 新 JSX 回归通过 type-contract 入口纳入现有测试类型检查；共享 Composer 测试 harness 的
  12 个消费者与最终接线回归均通过。没有把 JSX 测试运行通过误当作类型检查通过。
- 真实 Composer/附件/Goal/旧草稿/导航/发送/远程相邻路径回归通过；App 生命周期套件通过。
- `go test -race . -run 'TestFormalComposerTarget|TestManualCreation|TestSessionComposer' -count=1` 通过。
- Desktop 全模块使用仓库已有的 11 个互斥分组全部通过，清单校验覆盖 2929 个本平台顶层测试，
  包括 1.38.3–1.38.11 历史写入器数据。单进程运行在 10 分钟总期限处超时（当时子用例仅运行
  5 秒、无断言失败），因此采用现有完整分组；没有增加超时或跳过测试。此前固定 main-v2
  `f26283afb2f5` 的叠加证据继续保留，本轮未重新抓取或验证之后的新 main-v2 头。
- 完整 macOS arm64 ZIP 严格签名验证通过，SHA-256：
  `a637ecb0674953be4baf8e798906e1e317af11e02efb0aa5385b49c72cd11379`。
- `composer-wiring-smoke.mjs` 从真实新建按钮与图片粘贴事件开始，验证 host 确认后的重复读取、
  正常退出、再次启动、文字/图片预览恢复及重新取得 typed image 凭证。它不调用模型服务。
- `manual-session-smoke.mjs` 再次通过独立新建、同请求幂等、SQLite 重开、壳异常退出、图片引用、
  归档及最后一个会话关闭后无替代身份的完整打包应用检查。
- 使用原测试数据目录正常退出旧包并打开修复包，原项目、历史及已配置模型菜单可见。

The packaged smoke tests use disposable data and no provider credentials. The user's preview was
updated through its existing launcher and data directory after a normal save-and-exit cycle.
