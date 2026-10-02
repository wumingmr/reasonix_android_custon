# Windows / Linux Desktop 崩溃诊断运行手册

<a href="./DESKTOP_CRASH_DIAGNOSTICS_RUNBOOK.md">English</a>

本手册用于跨平台 Desktop 诊断链路的发布、隐私、性能和根因闭环。Windows build
`17763` 是重点实验环境，不是代码白名单；发布诊断版本本身不代表问题已经解决。

## 本地 transcript 初始化失败

创建、打开会话或迁移旧会话时，如果 transcript 初始化失败，可在 Electron 壳的
`service.log`（以及轮转文件 `service.log.1`）中搜索
`session transcript initialization failed`。服务向 stderr 输出结构化的 `diagnostic`
字段组，由壳现有日志链路落盘。该诊断的 `version=1`、
`code=transcript_initialization_failed`；不改变迁移账本格式。下文另行说明不含内容的线上
报告。

| 字段 | 含义 |
| --- | --- |
| `diagnostic.session_key` | 会话 ID 的 SHA-256，用于关联该运行时会话的错误 |
| `diagnostic.covered_sequence` | 初始化 transcript 覆盖到的事件序号 |
| `diagnostic.baseline_message_count` | 本次选取的输入消息数，最多 96 条 |
| `diagnostic.baseline_total_message_count` | 选取尾部窗口前可用的候选消息数，不一定是完整历史总数 |
| `diagnostic.baseline.record_count` | 这些输入消息转换生成的 transcript 记录数 |
| `diagnostic.baseline.record_index` / `previous_record_index` | 当前基线内从 0 开始的出错记录位置 / 首次冲突位置，仅在已知时记录 |
| `diagnostic.baseline.role` | 白名单内的角色，其他值统一为 `other` |
| `diagnostic.baseline.record_key` / `message_key` / `tool_call_key` | 标识的 SHA-256 摘要，原标识缺失时为空 |

`diagnostic.baseline.code` 区分 `duplicate_record_identity`（重复标识）、
`missing_record_identity`（缺少标识）、`baseline_encode_failed`（编码失败）和
`baseline_decode_failed`（解码失败）。编码、解码失败不记录位置。
位置针对本次基线内的 transcript 记录，不是整个会话的消息偏移。

旧会话迁移还会输出 `desktop session migration transcript initialization failed`，
包含同一份诊断、`stage=legacy_import` 和 `source_key`。使用 `source_key` 对照
Reasonix 配置目录下 `desktop/session-migration-v5.json` 中已有的 `sourceKey`。
即使重试时生成了不同的目标会话 ID，也能据此关联同一迁移来源。每次失败输出一条运行时
诊断；在该迁移路径中再输出一条迁移诊断。失败来源保留，正常会话可继续迁移。

这些新增记录不包含聊天正文、思考正文、工具参数、原始路径、原始标识或任意错误原文。
摘要用于关联，本身不能证明用户原始数据的具体触发原因。旧 `1.38.10` 日志无法补回这些
字段，需要使用包含此改动的构建复现后重新收集日志。

同一个已处理的迁移失败还会进入本地待上传队列，作为 `exception` 上报；其
`source=desktop.session_migration`、`label=transcript.initialization`，fingerprint hint
只包含无正文的错误分类。报告投递到 `https://crash.reasonix.io/v1/report`，并在
`/stats/diagnostics` 中展示，继续遵守现有 Desktop telemetry 同意开关、失败重试和按版本
去重规则。会话/迁移来源摘要以及记录标识摘要只保留在本地。旧版本未捕获的 panic 继续走
已有的 `go.runtime` / `go.fatal` 上报链路，并按高等级崩溃展示。

## 发布顺序

1. Firebase 项目保持 Spark 且不关联 Cloud Billing；只在
   `asia-southeast1` 创建 Realtime Database，并部署
   `workers/crash-report/firebase/database.rules.json`，确认客户端读写均被拒绝。不得启用
   Functions、Firestore、BigQuery、Hosting、Storage 或 Secret Manager。
2. 配置仓库 Secret：`FIREBASE_DATABASE_URL`、`FIREBASE_CLIENT_EMAIL` 和
   `FIREBASE_PRIVATE_KEY`。服务账号必须专用于 crash 投递且仅授予 Realtime Database
   权限。不得使用 Web Firebase 配置，也不得在 Desktop 产物中包含 Firebase SDK 或配置。
3. 冻结唯一候选 SHA，已发布 tag 不得移动或重建。
4. 备份 D1，并运行 `npm run migrate:diagnostics-v2`。命令会先检查完整 schema，写入前
   记录新的 Time Travel bookmark。已退休的 `metric_users` 和 `cli_metric_users` 不再
   是必需表；活跃 diagnostics 表出现任何 partial 状态时仍然 fail closed。
5. 运行 `npm run migrate:firebase-crash`。命令先记录 D1 Time Travel bookmark，再依次
   应用第一阶段 `migrate-firebase-crash.sql` 与第二阶段
   `migrate-firebase-crash-capacity.sql`；任一阶段部分完成时 fail closed。验证 outbox、
   receipt、兼容 lease 表、`firebase_crash_group_state` 及全部投递/生命周期索引。旧 lease
   表只用于滚动部署兼容。
6. 验证 `report_daily`、`report_installations`、
   `report_event_dimensions`、`diagnostics_meta`、fingerprint/date 索引、ping
   窗口索引，以及 `installation_linked_since`。
7. 在 **Actions > Deploy crash worker > Run workflow** 中选择 `main-v2`，并将
   **Firebase crash history operation** 设为 `dry-run`。该任务使用现有仓库 Secret，经过
   `canary` environment 审批，不会部署 Worker；脚本按每页 200 个 fingerprint 的 keyset
   分页，预计预留必须不超过 700 MiB。确认结果后选择 `apply`，并输入精确确认短语
   `APPLY_FIREBASE_CRASH_DATA`；任务会在同一 runner 内依次执行 `--apply` 和
   `--verify-only`。后续独立核验可选择 `verify-only`。已认证的运维人员仍可在本机运行
   `npm run migrate:firebase-data`、`npm run migrate:firebase-data -- --apply` 和
   `npm run migrate:firebase-data -- --verify-only`。默认 checkpoint 为权限 `0600` 且已
   gitignore 的 `.firebase-crash-migration-state.json`；可用 `--checkpoint=<path>` 改路径，
   只有明确重跑时才用 `--reset-checkpoint`。日志只输出计数、fingerprint 前缀和摘要。
8. Worker 先使用 `dual` 模式；用旧 Report/Ping/Metrics、legacy `webview2`、Windows/Linux
   `webRuntime` payload 做 `channel=test` smoke。
9. 连续比较 7 个完整 UTC 日；fingerprint、计数、样本和脱敏结果一致后，才将
   `CRASH_STORAGE_MODE` 从 `dual` 切换为 `firebase`。Firebase 模式下 D1 只保留聚合、
   索引和有界 outbox，不再写入新 `reports` 原文。
10. 用同一 SHA 生成签名 Windows/Linux 构建；能力矩阵和性能门禁通过后才发布 feature
   release。
11. 再稳定观察 7 天后归档 D1 旧原始样本。保留 `d1`、`dual`、`firebase` 三种回滚
   模式；Worker 回滚不要求客户端升级。
12. 通过管理界面保留审计地整理历史数据：忽略 `[go panic] safe` / `v9.9.9`，将
   `72daba81` 标记为在 `desktop-v1.19.3` 解决，忽略旧
   `desktop.abnormal_exit` replay 分组。

## Spark 容量、生命周期与回滚

Worker 固定执行 700 MiB 预留上限：active 每组 640 KiB、compacted 128 KiB、
archiving 32 KiB、archived 为 0。达到 80% 时复用现有 webhook 告警并在后台提示；新组或
扩容会越过上限时，必须在创建 outbox 前返回 `503`。不得把该上限改为可配置项。

只有 resolved/ignored 分组参与生命周期：30 天无新事件后，把最近 5 个样本替换为带
fencing 的 marker，并保留当前周期首个样本；60 天后 tombstone 全部样本路径，24 小时后
条件删除 Firebase group。D1 的计数、状态、备注、聚合和审计继续保留。archived
fingerprint 再出现时进入新 sample epoch，累计 count 与 lifetime first-seen 不重置。管理员
删除复用同一 tombstone 窗口，并原子删除对应 D1 分组数据。

回滚只改配置：设置 `CRASH_STORAGE_MODE=d1` 并重新部署。回滚时不要删除 outbox、receipt、
group-state 或 Firebase 数据。修复 migration/容量/ETag 问题后，重新执行 dry-run 与
`--verify-only`，再切回 `dual`；Desktop/CLI 无需升级。

## 隐私与兼容 smoke

旧 payload 可以缺失所有新增字段；legacy `webview2` 必须归一化为 `webRuntime`。
同一 engine/kind/reason/exit code 的恢复成功与失败必须属于同一 fingerprint。还需验证：

- 原始 install ID 不进入样本、HTML、应用/审计日志、导出和 pending 文件；
- 模块只保留 basename；不包含内容、密钥、账号、hostname、完整路径、GPU 型号和驱动；
- 重复事件正确累加 daily/install/event-dimension，且早期环境组合不被覆盖；
- 删除测试分组会删除三张诊断聚合表的对应数据；
- 诊断事实、ping、metric user 按 30 天分块清理；
- `channel=test` 始终位于 development namespace。
- 重复 `eventId` 返回 `202` 且不重复增加聚合；
- Firebase timeout、401、429 或 5xx 会保留 projected outbox，交给每 6 小时重试；
  outbox 满时返回 `503`，客户端必须保留 pending；
- Desktop 自动报告按版本和 dedup key 只成功上传一次，失败不进入 512 条/180 天账本；
  用户主动提交的 Desktop/CLI 报告不受本地 fingerprint 去重限制。

## 正常体验门禁

候选版本在壳启动前只允许一次本地配置读取、一次非阻塞归属锁和一次小型原子生命周期
写入。Runtime 探测及报告/指标落盘必须在壳启动后或有界后台消费者中执行；COM/GTK
回调只能非阻塞入队或递增原子丢弃计数。任何诊断失败都必须 fail-open。

使用同一 SHA 与关闭诊断的基线比较：诊断初始化 p95 不超过 10 ms、p99 不超过 25
ms；DOM-ready p95 回归不超过 `max(20 ms, 2%)`；shutdown p95 回归不超过 20 ms；
空闲 CPU 增幅小于 0.1 个百分点；RSS 增幅不超过 2 MiB。30 分钟正常使用期间必须是：
0 次诊断 reload、0 个轮询 timer、0 个新增弹窗，除已有 ping/metrics 外 0 个额外请求。

## 能力认证矩阵

全过程使用同一候选 SHA。Runtime、GPU 与驱动信息只记录在私密实验表，客户端不采集驱动。

| 平台 | 必须覆盖 |
| --- | --- |
| Windows 10 LTSC 2019 `17763` | VM + 实体 GPU；系统及最新 Evergreen WebView2；GPU 开/关 |
| Windows 10 `19045` | x64 对照；系统及 Evergreen WebView2 |
| Windows 11 稳定版 | x64 实机及当前稳定 Runtime |
| Windows arm64 | 正式交叉构建 + 一台设备 smoke |
| Ubuntu 22.04 | WebKitGTK 4.0、X11 |
| Ubuntu 24.04 | WebKitGTK 4.1、X11、Wayland |
| Debian 12、Fedora stable、Arch rolling | 能力 smoke；本地会话；Intel/AMD/NVIDIA 代表性覆盖 |
| 远程会话 | Windows RDP 与 Linux remote/xrdp |

每个环境执行 20 次冷启动/正常退出、10 次更新重启、60 分钟工作负载、50 次最小化/
恢复，以及休眠、显示器/DPI、远程连接切换。测试构建可定向终止 renderer/web process，
验证只恢复一次。Windows 收集 WER/可靠性监视器，Linux 收集 journal/coredump 元数据。
dump/core 仅在用户明确授权后私密传输，并在分析后删除。

## 根因和观察闭环

环境关联至少满足一项：两台同类实验节点复现且对照不复现；或三个不同线上安装命中同一
fingerprint，同时该环境至少有 30 个活跃安装，影响率达到对照 3 倍。GPU workaround
要求每台 GPU-on 至少 `2/20`、两台 GPU-off 合计 `0/40`，且每台两小时长测为 0。
workaround 必须按已有能力/Runtime 证据限定，不能只按发行版名称或 Windows build。

Integrity failure 转签名、注入和安全软件调查；OOM 转内存与会话资源调查；Runtime
聚集才支持后续最低版本或升级策略。renderer 恢复成功不算应用崩溃；只有 lifecycle
abnormal exit 时，必须拿到 WER、journal、dump 或 core 之一才能结案。

上线后观察七个完整 UTC 日：身份覆盖率目标 95%；低于 90% 不展示精确影响率。每天检查
legacy replay、fatal/recovered/degraded 数量关系、recovery failure、平台/Runtime/GPU
影响率、D1 增长、retention 和查询耗时。证据不足就保持 open 并延长至 30 天。只有根因
被证实后才发布定向补丁，修复后实验室要求 `0/40` 复现。
