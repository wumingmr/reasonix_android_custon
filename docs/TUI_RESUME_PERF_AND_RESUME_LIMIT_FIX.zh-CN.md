# TUI resume 性能与配置回归修复

本文记录两处改动：一是 TUI 恢复长会话时的加载性能优化，二是 `ui.resume_list_limit`
在配置重写时被静默抹掉的回归修复。

## 背景：resume 长会话为什么慢

通过在 Termux 上对一个真实长会话（`events.frames` 49 MB、5486 条消息、68594 个
zstd 帧）逐层计时，确认瓶颈不在功能多少，而在加载路径：

- `chatUIDisplayHistory` 对 final-format 会话调用 `Query().History()`，进而走到
  `Query.Snapshot()`：**从 offset 0 开始把整个 `events.frames` 全部解压、逐帧 JSON
  解析、再整体重放 projection**。实测仅这一条路径就要 ~9.2 秒。
- 拿到消息后，`replaySectionsForWithRenderers` 对每条消息做 goldmark markdown 渲染
  和折行，再把整段历史拼成一个巨型 transcript block；`transcriptBlockLineCount`
  又会为计算行数整体重排一次。渲染成本随历史长度线性增长。
- 代码库里**本来就有增量索引**（`.query-cache/<id>/history-locator-v2.sqlite`，
  desktop 端用的就是它的分页 `HistoryPage`／`HistoryWindow`），但 TUI 恢复路径没用。

因此编译期裁功能（去掉 desktop/bot/飞书等）对长会话 resume 几乎无收益：慢的是
I/O、解压和渲染，不是功能开关。

## 改动一：TUI 回放改走 locator 索引的尾部读取

### `internal/session/history_locator.go` — 新增 `Query.HistoryTail`

```go
func (q *Query) HistoryTail(ctx context.Context, ref SessionRef, maxMessages int) ([]provider.Message, error)
```

从 history-locator 索引读取最近 `maxMessages` 条消息，按 oldest-first 返回，
语义与 `History()` 的尾部完全一致（有单测逐字对比）。

两个实现细节值得记录：

- **优先同步建索引**：走 `prepareHistoryIndex`，索引缺失或过期时就地构建/增量更新。
  首次对某个长会话 resume 会有一次性成本，之后走增量。
- **不用 `COUNT(*)` 推算边界**：`position` 来自一个只增计数器，retract 之后的
  重新 upsert 会拿到新的 position 而旧 position 不被回收，理论上存在空洞。
  所以查询是 `ORDER BY position DESC LIMIT ?` 再反转，而不是
  `WHERE position > count-N`。前一种写法在空洞场景下会少读或多读。

### `internal/cli/branch.go` — 拆分回放与全量用途

关键点是**回放只想要尾部，但 `chatUIDisplayHistory` 还被 `/copy N` 和 `/export`
复用**。如果直接在 `chatUIDisplayHistory` 里截断，这两个功能会静默丢失早期历史。
因此拆成三个函数：

- `chatUIDisplayHistory` —— 语义不变，返回**完整**历史。`/copy`、`/export` 继续用它。
- `chatUIReplayHistoryTail` —— 内部用：exclusive 会话优先 `HistoryTail(200)`，
  索引不可用则回退全量路径。
- `chatUIReplayHistory` —— 回放入口：拿尾部后交给 `clipReplayHistory`，把截断边界
  **对齐到用户轮次**（`agent.IsUserAuthoredTurnMessage`），避免回放从一轮对话中间
  开始；返回截断后的切片和省略条数。

`replayActiveBranch` 改用 `chatUIReplayHistory`。

### `internal/cli/transcript.go` — 省略提示

`transcriptSource` 增加 `replayOmitted int`；`renderReplayBundleWithRenderers`
在 banner 之后输出一行 dim 提示（`replayOmittedNotice`），文案走 i18n 的
`ReplayOmittedLabel`，已补 en / zh / zh-TW 三种语言。为 0 时不输出任何内容，
所以短会话的表现与改动前一致。

## 改动二：`ui.resume_list_limit` 被配置重写抹掉

### 根因

`internal/config/render.go` 的 `[ui]` 段渲染**漏掉了 `resume_list_limit`**。
同段的 `theme`、`termux_width_offset`、`show_turn_usage` 等都有输出，唯独这个
字段没有。后果是：任何一次走 `RenderTOMLForScope` 的配置保存（`/model` 切换、
设置项修改等）都会把它从文件里删掉，用户看到的就是"自己写的被抹掉了"。

升级路径本身是安全的：`config_version` 迁移用的是 `rawTOMLSet`（字符串级编辑，
不重排整个文件），所以升级不会直接删字段；真正丢字段的是之后的任何一次全量重写。

### 修复

- `RenderTOMLForScope` 的 `[ui]` 段：值非 0 时输出
  `resume_list_limit = <值>`；为 0（未设置）时输出注释掉的行，保持与
  `termux_width_offset` 等字段一致的可发现性风格。
- `RenderTOMLProjectDelta` 的 `[ui]` 段：补上 `c.UI.ResumeListLimit != d.UI.ResumeListLimit`
  的分支，否则项目级保存会丢掉这个覆盖。

### 测试

`internal/config/render_test.go` 新增两个用例：

- `TestResumeListLimitSurvivesConfigRender` —— 用户级 render 和项目 delta 都必须
  带上 `resume_list_limit = -1`，且能 round-trip 回 `-1`。
- `TestResumeListLimitUnsetRendersCommentedHint` —— 未设置时不得渲染成生效值，
  且要保留注释提示。

已验证这两个测试在**未修复**的 `render.go` 上会失败，修复后通过。

## 审查中发现并修复的两个问题

初版改动在自查时发现两处真实缺陷，已一并修复：

### 1. `/copy N` 与 `/export` 被 tail 截断污染

第一版直接在 `chatUIDisplayHistory` 里改用 `HistoryTail`，但这个函数的调用者不只是
回放：

- `chat_tui.go` 的 `runCopyCommand`（`/copy N` 按序号取消息）
- `runExportCommand`（`/export` 导出整段会话）
- `newChatTUI` 里的 `pasteIDStateForHistory` 粘贴编号扫描

直连会导致这些功能只看到最近 200 条，属于静默功能回归。已改为：`chatUIDisplayHistory`
保持返回完整历史，新增 `chatUIReplayHistory` / `chatUIReplayHistoryTail` /
`clipReplayHistory` 只服务回放入口。

### 2. 无 user turn 的历史被截到只剩 1 条

`clipReplayHistory` 为"边界对齐用户轮次"写的循环是
`for cut > 1 && !IsUserAuthoredTurnMessage(...)`。当整段历史里没有任何用户轮次
（纯 assistant 的恢复记录等场景）时，循环会一路退到 `cut == 1`，回放只剩最后一条
消息。已加兜底：找不到用户轮次时保留完整的 200 条尾部。单测
`TestClipReplayHistoryWithoutUserTurns` 覆盖了这个场景。

顺带补齐：`reasonix --resume <id>` 直接启动走的是 `chat_tui.go` 里首次
`WindowSizeMsg` 的独立 commit 路径，原先拿的是 `m.history`（全量历史），因此
这条启动路径享受不到索引带来的提速。现在构造 `chatTUI` 时就调用
`chatUIReplayHistory`（索引优先），把省略条数存进新的 `historyOmitted` 字段，
首次 commit 同样会显示省略提示。

粘贴编号扫描（`pasteIDStateForHistory`）现在也基于这个尾部切片工作。这是可接受
的：它只用于避免新粘贴编号碰撞，而被省略的那段前缀本来就已经在会话里，不会带来
新的碰撞风险；反过来为了它去读全量历史，会把启动路径的提速完全抵消。

## 实测数据

真实长会话（49 MB `events.frames`，5486 条消息，Android arm64）：

- 全量重放：~9.2 s
- `HistoryTail(200)`：~2.7 s（其中大头是 `prepareHistoryIndex` 的增量校验；
  索引完全命中时数据见下方阶段计时）
- 阶段拆分：`prepareHistoryIndex` ~590 ms，sqlite open ~563 ms，
  metadata+count ~3 ms，200 行读取+解压+JSON 解码 ~302 ms
- 正确性：tail 与 full 的尾部逐字一致（ID + Content）

注意 `prepareHistoryIndex` 的成本与会话是否刚追加过有关：会话每追加一次，增量
索引就要重新扫过新增部分，所以"刚用过这个会话"时首次 resume 仍会付出这段成本。

## 未做/后续可选

- 回放渲染本身仍是"整段拼接 + 整体重排"，本次只是限制条数。若要让超长历史滚动
  到底部再按需加载，需要改 viewport 的 scroll 与 `transcriptSources` 索引语义，
  回归风险集中在滚动定位和选择复制，留作后续。
- `/resume` picker 打开时会遍历全部会话（当前配置 `resume_list_limit = -1`
  等于不限量）。这影响 picker 打开速度，不影响选定后的加载；如需加速可把该值
  调小。

## 验证记录

- `go build ./...` 通过
- `internal/session`：新增 `TestHistoryTailMatchesFullHistorySuffix`、
  `TestHistoryTailToleratesPositionGaps`（后者覆盖 position 空洞）通过
- `internal/cli`：新增 `TestClipReplayHistoryKeepsShortHistoryIntact`、
  `TestClipReplayHistoryBoundsLongHistory`、`TestClipReplayHistoryWithoutUserTurns` 通过
- `internal/i18n` 全量通过（含多语言字段完整性检查）
- `internal/cli`、`internal/config` 的 5 个失败（WSL 剪贴板、
  `TestReadServeTokenFileRejectsLoosePermissions`、
  `TestDeepSeekCatalogUpgradeAtomicRetryAndUnknownInlineFields`、
  `TestCurrentConfigRepairsExactProviderEndpointContract`、
  `TestUpgradeDeepSeekProviderProtocolWritesThroughSymlinkAndPreservesMode`）
  在未改动的 master 上同样失败，属预存的环境性失败，与本次改动无关
