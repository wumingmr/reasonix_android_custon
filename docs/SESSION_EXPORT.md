# Complete session export / 会话完整导出

## Ownership and snapshot / 数据归属与快照

Desktop captures an explicit host/session identity and accepted commit boundary
before opening the save dialog. `FlushThrough` waits only for that boundary;
cancelling its caller does not cancel shared persistence or execution. The Query
owner traverses the existing versioned display index forward in batches of at
most 100 records and hydrates content in ranges of at most 1 MiB. Provider working
sets and React resident items are never complete-export inputs. The default chat
window remains three pages of 32 records.

Desktop 在保存对话框出现前固定主机、会话和已接受提交边界。
`FlushThrough` 只等待该边界；取消等待不取消共享写盘或任务执行。
Query 使用既有版本化展示索引，每批最多 100 条、内容块最多 1 MiB。
完整导出不读取模型工作集或 React 当前分页；聊天窗口仍默认保留 3×32 条。

Markdown, JSON, clipboard and visual blocks share `internal/sessionexport`.
Tool results are associated across the entire snapshot by call ID using disk
staging. Markdown preserves output whitespace and uses collision-safe fences.
JSON retains `title`, `exportedAt`, `mcpList`, and `items`, adding snapshot metadata.
Persisted MCP display notices contribute attribution; observations absent from
older authoritative logs cannot be reconstructed and are explicitly scoped out.
The optional `diagnostic` event extension does not alter model input or require
an authoritative storage migration. The derived history index version is 10.

Markdown、JSON、复制全部和视觉语义块共享 `internal/sessionexport`。
工具结果通过 call ID 在整个快照内匹配，临时内容写盘；Markdown 保留原始空白，
围栏长度避开正文反引号。JSON 保留已有顶层字段并增加快照信息。
新增 MCP 展示通知通过可选诊断事件持久化并参与完整归因统计；旧日志从未保存的
通知无法恢复，导出元数据明确说明此范围。不改变模型输入，不迁移权威存储；
仅派生历史索引升级至版本 10。

## Host and remote API / 宿主与远程接口

`BeginSessionExportForTarget`, `ReadSessionExportChunk`,
`AppendSessionExportPage`, `FinishSessionExport`, and `CancelSessionExport`
operate on a Desktop-owned handle. Exports survive component disposal. Source
validation rejects deletion/replacement rather than rebinding to an active tab.
Remote peers advertise `session-export-v1`; snapshot/document/validation/diagnostic
requests retain the expected-session fence. Remote snapshots are stateless: each
response releases its temporary files, so there is no remote lease to abandon.
Old peers fail explicitly without a last-page fallback. Existing Markdown and
goal-diagnostic entry points remain compatibility paths.

上述五个宿主接口仅按导出句柄操作，切换或卸载会话组件不取消导出。
来源被删除或替换时返回错误，不重新绑定活动标签。远程以
`session-export-v1` 协商能力，每个请求保留会话身份栅栏；远程快照无服务器租约，
临时文件随响应结束释放。旧服务明确提示升级，不退回导出尾页。
旧 Markdown 和目标诊断入口保留兼容路径。

## Rendering and publication / 渲染与发布

PDF/PNG rendering consumes semantic blocks with backpressure, retaining one
surface and one encoded page at a time. The host validates image encoding,
dimensions, upload order and complete pages. PDF image objects are streamed to
a temporary file before its page tree and cross-reference table are completed.
Single files are synced and atomically replaced. PNG batches use exclusive
hard links and a sibling publication journal; the next batch in that destination
recovers unfinished publications using inode witnesses, preserving user
replacements. Filesystems lacking hard-link publication fail safely. No external
image fetch or attachment archive is introduced.

PDF/PNG 按语义块逐页生成，宿主确认后释放当前画布和页面，不累计全会话 DOM
或图片数组。宿主验证图像格式、尺寸、上传顺序与页面完整性；PDF 逐页写图像对象，
最后补齐页树和索引。单文件同步后原子替换；多 PNG 使用独占硬链接发布和同目录
事务清单，下次向该目录导出时凭文件身份清理未完成输出，保留用户替换文件。
不支持硬链接的文件系统会明确失败。不新增外部图片下载或附件打包。

## State and diagnostics / 状态与诊断

Cross-page tool observations carry execution evidence and a lazy result locator.
Missing or unreadable content never implies cancellation. Subscription disposal
remains separate from execution cancellation. Diagnostics retain the goal schema
and add source identity, snapshot boundaries, frontend binding/read observations
and bounded lifecycle traces (256 entries per session). Unknown cancellation
provenance remains unknown. Runtime observations and export boundaries have
separate timestamps/watermarks. Independent diagnostic read failures are recorded
as unavailable; destination write failures still fail the export.

跨页工具观察提供执行证据和按需读取的结果位置。正文未加载或读取失败不代表
执行被取消；订阅释放与任务取消入口分离。会话诊断保留旧目标诊断字段，增加来源、
快照、前端绑定/读取观察和每会话最多 256 条生命周期轨迹。无法核实的取消来源
保留 unknown。运行时观察与导出边界各自记录时间和水位；独立读取失败记为
unavailable，目标文件写入失败仍判定导出失败。

### Provider failures / 模型请求失败

Failed/interrupted turns with diagnostic evidence save an optional
`diagnostic/provider` event in the same atomic commit as `turn/end`.
Successful turns do not persist these request observations or peer addresses.
If generating the optional diagnostic fails, it is omitted with a warning in
the log; required closure events and `turn/end` still commit.
The event contains the structured failure classification and up to 128 recent
request observations belonging to that turn, without free-form transport error
text or API response bodies. Both live and cold
diagnostic exports retain these events in `commits`; the top-level
`providerDiagnostics` still describes only the current controller lifetime.
An empty live buffer does not mean the historical request was never sent.

有诊断证据的失败／中断轮次会在与 `turn/end` 相同的原子提交中保存可选事件
`diagnostic/provider`，包含结构化的失败分类和该轮最近最多 128 次请求的观测，
不额外保存自由文本形式的传输异常或 API 响应正文。可选诊断生成失败时会记录警告并
省略该记录，必需的收尾事件和 `turn/end` 仍正常提交。运行中或冷会话的诊断导出均在 `commits`
保留这些事件；顶层 `providerDiagnostics` 仍仅代表当前控制器生命周期。
实时缓冲为空不代表历史请求没有发出。成功轮次不持久化这些请求观测或对端地址。

`dropped` counts observations evicted for this turn, including when all its
requests have been evicted. `truncated` marks incomplete evidence. Accounting
retains at most 128 turn summaries separately from the shared 128-request ring.
If accounting is no longer available, `dropped` is omitted and `truncated` is
true; older events missing these fields have unknown completeness. A zero
per-turn count does not include evictions belonging to other turns.

`dropped` 是本轮被淘汰的请求观测数，即使本轮请求全部被淘汰也会保留计数。
`truncated` 标记证据不完整。计数独立于共享的 128 条请求缓冲区，最多保留
128 个轮次摘要；计数信息已淘汰时省略 `dropped` 并设置 `truncated: true`。
旧事件缺少这些字段时完整性未知。本轮计数为零不代表其他轮次没有丢弃记录。

Observations include the request host/path, method and byte count, last observed
phase, connection reuse, negotiated HTTP protocol, dial/connected addresses,
timestamps, response status/body byte count and a recognized HTTP/2 error code.
`dialAddress` is the last dial attempt, while `remoteAddress` is the acquired
connection's peer; neither proves which upstream hop caused a failure.
URL userinfo/query/fragment, headers and request/response bodies are excluded.
Missing fields on older observations remain unknown. `requestBytes = -1` means
the request body length was unknown. These records do not change model input,
cache prefixes, transport selection or the single-attempt request policy.

观测记录请求主机/路径、方法和字节数、最后观察阶段、连接是否复用、协商的 HTTP
协议、拨号/已连接地址、时间戳、响应状态/正文字节数，以及已识别的 HTTP/2 错误码。
`dialAddress` 是最后一次拨号尝试，`remoteAddress` 是已取得连接的对端；两者均不能
单独证明故障来自哪一跳。不保存 URL 用户信息/查询参数/片段、请求头或请求/响应正文。
旧记录缺少的字段保持未知，`requestBytes = -1` 表示未知正文长度。
这些诊断不改变模型输入、缓存前缀、传输协议选择或单次请求策略。

Failure cleanup preserves `terminalStatus` and `failureDiagnostic` on local-only
recovery records. HTTP/2 transport errors use `kind: transport_protocol` and an
optional `transportCode`; reopened history retains the failure notice rather
than relabelling it as a cancelled turn.

失败收尾会保留本地恢复记录的 `terminalStatus` 与 `failureDiagnostic`。
HTTP/2 传输错误使用 `kind: transport_protocol` 和可选的 `transportCode`；
重新打开历史时仍显示失败信息，不再因收尾丢失字段而退回取消轮次提示。

| Contract / 契约 | Old data / 旧数据 | Current reader / 新读取器 | Previous reader / 旧读取器 |
| --- | --- | --- | --- |
| `diagnostic/provider` | No record; do not infer evidence / 无记录，不推断 | Export raw optional events / 导出可选事件 | Skip unknown optional event; retain terminal / 跳过未知可选事件，保留结束状态 |
| `transportCode`, observation fields / 观测字段 | Missing is unknown / 缺省为未知 | Decode optional fields / 读取可选字段 | Ignore additive JSON fields / 忽略新增 JSON 字段 |
| Recovery metadata / 恢复元数据 | Existing fields remain readable / 原字段可读 | Preserve through cleanup / 收尾保留 | Existing format; an older writer may still strip it / 格式不变，旧写入器仍可能丢弃 |

## Verification / 验证

Relevant commands:

```sh
go test ./...
(cd desktop && go test ./...)
go test -race ./internal/session ./internal/control -run 'Test(FlushThrough|ExportSnapshot|StreamExportCommits|ToolObservation|SessionLifecycle|MCPAttribution|SessionDiagnostics|.*GoalDiagnostic|.*CancelSession)'
(cd desktop && go run . -emit-contract frontend/src/generated)
go run ./tools/desktopinventory
pnpm --dir desktop/frontend test:typecheck
pnpm --dir desktop/frontend test:transcript
pnpm --dir desktop/frontend test:app-lifecycle
pnpm --dir desktop/frontend test:transcript-browser
pnpm --dir desktop/frontend test:session-export-browser
pnpm --dir desktop/frontend build
```

Fixtures cover >96 records, 15 samplings/23 tools, large Chinese payloads,
whitespace/fences, fixed cuts, version updates/retractions, dialog-time source
switching, cancellation isolation, detached results, unsupported peers, corrupt
pages, collisions, publication recovery and bounded lifecycle buffers.
The visual probe produces 22 PDF raster pages and 6 PNGs while retaining one
surface. The 240/1000-turn Chromium reader/switch suite passes. Same-configuration
startup assets measure 2444.2 KiB versus 2442.2 KiB at the baseline; only the raw
aggregate budget is adjusted to 2444.9 KiB. Renderer/execution and detached tool-content readers stay lazy;
gzip, CSS, individual chunk and interaction budgets are unchanged.

合成用例覆盖超过 96 条记录、15 次采样与 23 次工具、大段中文、空白和嵌套围栏、
固定边界、消息更新/撤回、保存框期间切换、取消隔离、跨页读取、旧远程能力、损坏页面、
冲突与发布恢复。视觉探针生成 22 页 PDF 栅格页和 6 张 PNG，只保留一个排版区域；
240/1000 轮 Chromium 滚动和切换套件通过。同构建配置首屏资源由 2442.2 KiB
增至 2444.2 KiB，原始总量预算调整为 2444.9 KiB；导出执行和渲染仍懒加载，
gzip、CSS、单块和交互预算不变。

The isolated Electron renderer-to-Go export round trip is verified. The native
macOS save sheet was exercised through the accessibility controller: Cancel
returned no export handle; Save published a Markdown snapshot in a temporary
directory, whose contents were checked. This native smoke used an empty canonical
session; long-history rendering and dialog-time rebinding have separate browser
and deterministic host tests. Manual workspace-switch/notification acceptance
and Windows native dialog/filesystem behavior remain external verification gaps.
Browser checks do not stand in for those native gates.

隔离 Electron 环境的渲染器到 Go 导出调用已验证。通过原生辅助功能通道验证了
macOS 保存框：取消不留下导出句柄，保存成功写入临时目录并核对 Markdown 内容。
原生冒烟使用空的权威会话；长历史渲染和保存期间来源切换分别由浏览器及确定性
宿主测试覆盖。原生工作区切换/完成通知的人工验收，以及 Windows 保存框和
文件系统行为，仍需相应实机或 CI 验证；浏览器结果不替代这些验收。
