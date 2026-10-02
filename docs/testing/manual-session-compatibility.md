# Manual session rollback: historical data compatibility

本报告记录 #10469 行为回滚的增补验证。所有数据均为隔离目录内的合成数据；没有读取或
修改用户真实会话，没有安装旧客户端到系统应用目录，也没有发布版本。

代码基线为 `0d7a103fdd6f`；另在隔离 worktree 将回滚补丁叠加到已抓取的
`origin/main-v2`（`f26283afb2f5`）验证。主工作区没有合并或重置到该分支。
[机器可读版本与 PR 清单](manual-session-compatibility-sources.json) 固定了 9 个发布标签
及 115 条影响相关所有者的第一父链变更。清单用于定位测试边界，不代表逐个 PR 的所有功能均做了全平台验收。
[已执行结果与最终包摘要](manual-session-compatibility-results.json) 记录通过的测试组、耗时、
日志摘要以及最终 Electron、Go host 和 app.asar 的 SHA-256。
后续[提交竞态检查](manual-session-bug-review.md) 修复了三类前端问题；该轮重新构建与原生检查
单独记录于结果中的 `submissionRaceReview`，不与本轮历史矩阵的构建摘要混用。

## 真实版本边界

“Desktop Session v5”、注册表 schema、会话逻辑 schema、物理 storage revision 是不同维度，
不能仅凭版本号或“都是 v3”判定可降级。

| 发布版本 | 精确提交 | 历史会话数据 | 注册表 / 持久化草稿 |
| --- | --- | --- | --- |
| 1.38.3 | fa018e410926 | JSONL + schema-2 事件日志 | 无新注册表 / 无草稿库 |
| 1.38.4 | 680e00e5baaf | JSONL + schema-2 事件日志 | 无新注册表 / 无草稿库 |
| 1.38.5 | 95d02e52bd99 | JSONL + schema-2 事件日志 | 无新注册表 / 无草稿库 |
| 1.38.6 | e2145b031dee | JSONL + schema-2 事件日志 | 无新注册表 / 无草稿库 |
| 1.38.7 | 036c7c50c5c1 | JSONL + schema-2 事件日志 | 无新注册表 / 无草稿库 |
| 1.38.8 | 7278072720a2 | 旧历史 + framed storage revision 1；没有 recovery storage identity | 无新注册表 / 无草稿库 |
| 1.38.9 | dc915ab97bfd | 旧历史 + revision 1 + Desktop 独立身份 header | 注册表 v1 / 无草稿库 |
| 1.38.10 | 366e0eb6b86b | 旧历史 + revision 2 + Desktop header | 注册表 v3 / 无草稿库 |
| 1.38.11 | 11f9705f0838 | 旧历史 + revision 3 + Desktop header | 注册表 v3 / 草稿 schema 4、snapshot 5 |

版本依据标签的真实代码与编码器，不能把 main-v2 上的提交日期当作正式标签已经包含该功能。
#10469 的合并提交 `e8762acc9` 和 #10572 的实现提交 `54826e46a` 另有独立草稿夹具，
覆盖只使用过 main-v2 中间构建的用户；前者冻结 snapshot 4，后者为 snapshot 5。
schema 1–3 没有对应上述正式标签，使用按既有迁移定义构造的结构夹具，明确不冒充历史发布产物。

### 回退边界的实测修正

| 数据 | 旧数据升级 | 新写入后的旧读取器 | 结论 |
| --- | --- | --- | --- |
| 注册表 v1 → v3 | 保留身份、预留操作及未知字段 | 1.38.9 拒绝 v3 | 不支持回退到 1.38.9 |
| 注册表 v3 | 1.38.10/11 可读写，未知字段往返保留 | 真实旧读取器成功追加，再升级仍存在 | 仅证明注册表兼容 |
| 会话 revision 1/2 → 3 | 旧源不改写；新目标可读并继续写入 | 1.38.10 读取器只支持到 revision 2 | 不支持新版会话整体回退到 1.38.10 |
| 会话 revision 3 | 1.38.11 与当前版可读 | 真实 1.38.11 程序读取新版数据并追加消息 | 再升级保留两侧历史 |
| session-ui-v1.sqlite | 独立输入库 | 真实旧程序不改动文件 | 旧版继续聊天后，再升级必须核实过时输入 |
| 未知未来 schema/revision | 明确错误，原文件保留 | 不写旧 schema 覆盖 | 不静默降级 |

物理 revision 3 来自 [#10545](https://github.com/esengine/DeepSeek-Reasonix/pull/10545)
中的 `e7a5b149e`，并非本次回滚引入。1.38.10 的负向测试要求返回真实
`ErrUnsupportedVersion`，且会话权威文件和独立输入库均不改变；不能把拒绝读取计为可用降级。

## 新增测试与夹具

`scripts/generate-session-history-fixtures.py` 在临时目录提取每个标签的真实 Go 模块，
编译原版 agent/session 写入器并生成数据。持久化编码、事件校验、framing、内容引用均由原代码完成。
仅将进程 writer identity 与 writer announcement hostname 合成化；hostname 替换保持字节长度，
不破坏事件索引偏移。每套 `SOURCE.json` 保存标签提交及全部文件 SHA-256。

| 测试 | 数据和断言 |
| --- | --- |
| `TestTaggedHistory138*`（四个分组，共享矩阵实现） | 9 版本 × 全局/项目；39 个源会话 × 2 个作用域；每组重复打开 3 次。逐字段比较全部 provider 消息、图片、reasoning、工具调用/结果及大内容；17 条一页遍历，验证全部消息可达且无重复；身份和新输入不因重启变化，旧源不改写 |
| 同上：复杂上下文 | 145 条消息、70 次后续问答、长工具结果、Unicode/空格/#/% 路径、独立分支、未完成的本地回复、canonical 未结束 turn、暂停 Goal 和未知 Goal 字段；压缩后模型摘要与完整展示历史分别保留 |
| `TestTaggedFormalSessionsKeepHeaderAndProviderHistory` | 1.38.9/10/11 原版 Service 创建的正式 header 会话；原身份、CWD、全部内容和新版追加消息在重启后保留 |
| `TestTaggedPreviousWriterRoundtrip` | 新版写入 → 真正编译的 1.38.10/11 程序读取/追加 → 新版读取；1.38.10 安全拒绝，1.38.11 保留历史并将旧输入标记为待核实，禁止直接提交 |
| `TestTaggedDraftFixturePreservesAllRecoveryIdentities` | #10469、#10572、1.38.11 各 13 类记录；冻结设置、未知快照字段、仅设置草稿、缺失附件、冲突、已转换及所有提交阶段身份保持 |
| `TestTaggedDraftRestartReconcilesWithoutReplay` | 每套旧草稿连续 3 次启动核对；reserved/starting → resume_required；dispatching/shell/failed → dispatch_unknown；accepted 只转换；取消释放保持原身份；无新 Session、无运行时、无自动执行 |
| `TestEveryPreviousDraftSchemaPreservesWALAndOpaqueRecords` | schema 1、2、3 的内容、设置、冲突、恢复选择、操作、未知表，以及仅存在已提交 WAL 中的数据，迁移及一致性备份均保留；重复启动不替换旧备份 |
| `TestUnrecognizedDraftDatabaseIsNeverInitializedOrRewritten` | 负版本、已有表但版本 0、未来版本；重复打开都报结构化不支持，文件逐字节不变 |
| `TestTaggedDamagedHistoryIsolatesFailureAndRetainsRecoveryEvidence` | 1.38.8–11 的未来 revision、截断 commit；坏记录不发布，正常兄弟会话继续可用；修复后不复制身份、原源不变；旧预留操作的源指纹变化保持冲突证据 |
| `TestImportWithoutRecoveryIdentityPublishesReadableHistory` | 导入缺少 recovery identity 的原身份/改名身份两条路径；未启动运行时即可构建分页历史，源归档不被补写 |

截断文件修复改变了已预留操作的源指纹时，旧操作仍报告生命周期冲突；测试明确要求保留这一证据，
不能把旧预留操作强行认证为对修复后字节的成功执行。正常兄弟会话及新完成的导入内容继续可访问。

## main-v2 的保留边界与回归组

| PR 组 | 保护内容 | 验证入口 |
| --- | --- | --- |
| #9988、#10120、#10241、#10257、#10267、#10326 | Electron 更换、旧事件日志、framed 存储、恢复加速、SessionID | 标签生成器、版本矩阵、原生启动、root Session 测试 |
| #10320、#10395、#10440、#10459、#10549、#10566、#10594 | 身份、Windows 路径、按需历史迁移、分支/转换谱系 | 81 个已有迁移/历史测试；DAG 多代转换、源变化隔离、重复启动、历史先行加载 |
| #10396、#10409、#10519、#10560、#10623、#10631、#10634 | 归档、恢复、永久删除、最后会话、关闭协调 | workspacestate 全包 race，生命周期回归，输入归档/删除测试，原生最后归档重启 |
| #10469、#10551、#10572 | 旧草稿冻结和恢复，欢迎页 | 三套真实草稿库、schema 迁移、恢复状态机、空页/导航测试 |
| #10392、#10430、#10545、#10547、#10598、#10626、#10628 | 回执、压缩、附件闭包、导出、inbox、管理命令 | 131 个 root 定向 race 测试；真实旧写入器；Goal/shell/管理命令提交回归 |
| #10550、#10590、#10603、#10613、#10619、#10621 | 消息身份、历史分页、只读快照、资源限制 | transcript suite、固定快照/索引回归、标签历史分页 |
| 比回滚基线新增的 #10639、#10641、#10642、#10644 | 用户行与输出顺序、Dock 图标、压缩反馈、图形故障恢复 | 最新 main-v2 上三方干净应用回滚补丁；前端双类型检查、生命周期、transcript、输入恢复、38 项顺序/交接断言、Electron 测试和生产构建 |

main-v2 叠加验证还运行了 283 个 agent/control/transcript 定向测试，以及 13 个 Desktop
创建/输入/草稿/停用清理 race 测试。各组有交叉，不能相加作为不重复测试总数。
生产构建使用 main-v2 自带的预算，没有为本次改动放宽预算；该叠加构建初始资源为
2053.6 KiB / 2053.8 KiB。

## 本次测试发现并修复

1. **未知草稿库被初始化**：版本 0 但已有表的数据库会进入初始化路径；负版本还可能先改 journal mode。
   现在在任何写入前拒绝未知布局，兼顾另一个进程刚完成首次初始化的情况。
2. **1.38.8 导入后分页失败**：原版归档没有 `storage.identity.json`，旧导入仅为改名目标生成 identity。
   现在每次导入在验证完整事件后、原子发布前补齐目标 identity；已有合法 identity 保持，源数据不改动。
3. **收紧降级说明**：真实读取器确认 1.38.10 的会话 revision 边界。保留负向测试，未为迎合旧版降低当前格式。

原生初测曾发现 rebuildable `display-index.json` 的 `updated_at` 会刷新。这不是会话内容写入；
原生源文件不变断言排除可重建 display/event index，仍逐字节核对原 JSONL、事件日志、metadata 和内容对象。

## 原生应用证据与限制

使用隔离的 macOS arm64 **完整打包应用**（Electron 与 Go host 同一构建），没有开发服务器或 service override。
逐版本执行正常启动 → 列出旧历史但不隐式导入 → 显式导入 → 读取历史 → 保存正式会话输入 → 正常退出 →
重启 → 核对身份/输入/无新草稿/无替代会话 → 核对原源。9 个版本共 39 个样本。
另有正式新建、重复操作、图片、正常退出、杀死壳后的异常恢复、归档最后会话测试。

**未覆盖的边界**：Windows/Linux 的实际打包运行仍需对应系统；没有安装九个完整旧版客户端逐一执行 UI 升级，
旧版本证据来自其真实编码器/读取器和当前打包应用。没有穷举 9×9 所有客户端来回组合；数据迁移覆盖逐版直接升级、
既有多代格式链、1.38.11 回退再升级及 1.38.10/1.38.9 的拒绝边界。真实 provider 联网执行不在这些合成测试范围内。
不能用这些测试声称未来所有 main-v2 PR 或未来格式也兼容。

## 复现

```sh
python3 scripts/generate-session-history-fixtures.py --writers-dir /tmp/reasonix-tagged-writers
python3 scripts/generate-manual-session-fixtures.py
python3 scripts/session-compatibility-evidence.py

cd desktop
node ../scripts/desktop-windows-go-tests.mjs history-3-5
node ../scripts/desktop-windows-go-tests.mjs history-6-7
node ../scripts/desktop-windows-go-tests.mjs history-8-9 --race
node ../scripts/desktop-windows-go-tests.mjs history-10-11
REASONIX_TAGGED_WRITERS=/tmp/reasonix-tagged-writers go test . -run 'TestTagged(Formal|Previous|Draft|Damaged)' -count=1
go test -race ./internal/workspacestate ./internal/draftstate ./internal/sessionui ./internal/legacycleanup ./internal/upgradefixture
```

版本矩阵按不重叠版本分组，避免完整 Desktop 测试包的聚合超时；没有放宽单例断言时间。
普通 Desktop CI 使用同一分组逐进程执行，race 与 Windows CI 增加四个历史分组。
分组校验从当前平台 Go 测试清单验证每个测试恰好归属一个执行组，保留原生 ConPTY 的单独执行。
27 项 CI/分组契约测试通过；没有通过跳过历史矩阵或扩大超时来解决聚合耗时。
最终通过同一 CI 入口补跑 `history-8-9 --race`，Desktop 包耗时 176.148 秒。
旧写入器往返是显式集成检查，需要先生成对应可执行文件并设置 `REASONIX_TAGGED_WRITERS`；
本次已实际执行并通过，普通 CI 不设置该变量时会报告跳过这一项。
`tagged-history-smoke.mjs` 接收最终打包可执行文件路径，默认运行所有 9 个版本。

## English summary

The fixtures use actual writers from all nine release tags, plus durable-draft snapshots from the
#10469 and #10572 development milestones. Tests cover complete provider history, paged reachability,
compaction, branches, interrupted work, opaque fields, SQLite WAL backup, receipts, lifecycle ownership,
and stale composer recovery. The current macOS package also imports each release's synthetic histories
and survives a normal restart without adding identities or modifying authoritative sources.

The real 1.38.10 reader rejects storage revision 3 introduced by #10545; registry compatibility does
not imply session downgrade compatibility. The real 1.38.11 writer can continue a current-format
session; reupgrade preserves the independent input database and requires review of stale input.
Unknown draft layouts are now rejected before any write. Imports of pre-recovery archives now publish
a target storage identity before history reads. Windows/Linux native execution and full old-client UI
roundtrips remain unverified.
