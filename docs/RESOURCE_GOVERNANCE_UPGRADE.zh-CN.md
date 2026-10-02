# 会话资源治理升级说明

> 基线：`main-v2` @ `2553bb31c`（2026-09-15 合并点）
> 范围：Desktop 会话历史、流式 Markdown、工具详情、正文读取、媒体释放与 skillwatch 诊断

## 为什么要改

1.38.8 的 CPU/内存反馈暴露的是资源所有权问题，而不是单个热点：历史记录、实时尾部、Markdown 解析、详情正文和媒体解码分别增长，却没有共享的驻留、并发和释放契约。旧实现中的全量历史物化、活跃会话绕过页预算、流式解析重复传输全文和反复重建 worker，均可从源码确认；它们对用户反馈的实际贡献比例仍需在 1.38.8 原生包上采样，当前不能用源码推断替代测量。

本次重构借鉴 DeepSeek Harness 的几个设计原则：资源由明确的 owner 持有；数据可回收但必须可重新到达；能力协商使用类型化结果；后台工作按交互优先级调度；诊断只记录有界、无正文的计数。

## 架构变化

### 1. 有界、双向、可恢复的历史窗口

- 默认只驻留相邻 3 页，每页 32 条；活跃会话与历史会话遵守同一页预算。
- 支持“更早内容”“更新内容”“回到最新”，回收远离读者的一端。
- 回收会返回必须卸载的稳定 item ID；工具调用和结果作为一个所有权单元释放，不留下孤儿结果。
- 被回收内容仍可通过游标重新到达。游标失效返回类型化 `stale_cursor` 并重新定位，不通过错误文本猜测控制流。
- 翻页前检测 transcript 内原生选区，避免卸载用户正在复制的节点。
- `turn_done` 后把实时尾部重新绑定到最新的持久化有界窗口，长跑活跃会话不再无限追加到页预算之外。

### 2. 分层正文与统一资源预算

资源默认值集中在 `desktop/frontend/src/lib/resourceBudgets.ts`：

| 资源 | 默认预算 | 超出后的行为 |
| --- | ---: | --- |
| 历史正文 | 32 MiB / 进程 | 按加权 LRU 回收非活跃会话 |
| 历史窗口 | 3 × 32 条 / 会话 | 回收远离读者的一端，保留游标 |
| 正文读取 | 全局 4、每会话 2 | 排队、去重；会话释放时取消 |
| Markdown AST | 12,000 元素 / 发布页 | 保持顶层语义块完整，用户按页继续加载 |
| 超大 Markdown 表格 | 2,000 单元格 / 页 | 表内继续加载 |
| 工具预览 | 16 KiB、64 块 | UTF-8/代理对安全截断；完整值仍可按需读取 |
| 工具关联项 | 20 项 / 页 | 详情抽屉内继续加载 |

12,000 是渐进挂载目标，不是删除阈值。单个不可拆的顶层 AST 块即使超过目标也会完整呈现，以保持 Markdown 语义；超大纯文本表格另有单元格分页。完整复制走解析期 selection projection，不依赖当前已挂载页。

### 3. 流式 Markdown worker 文档协议

worker 从“一次性全文请求”升级为文档生命周期：

```text
open -> append* -> replace* -> finalize -> release
```

- 前缀增长只传新增后缀；非前缀变更使用 `replace`，结束时用权威全文 `finalize`。
- 新流式提交 supersede 旧解析时只丢弃旧响应，不终止并重建 worker。
- 调度优先级为：交互流式正文 > 可见历史 > 后台历史。
- worker 不可用时继续使用异步主线程解析；失败不会卡死队列。
- 解析时同时生成 block fingerprint 与元素计数；渲染端用整数身份复用稳定 AST，不再对每块做两次 `JSON.stringify`。
- 当前 Markdown parser 仍是 whole-document 语义解析；增量协议消除了线程抖动和重复桥接全文，但没有宣称 parser 本身已成为增量语法解析器。

### 4. 导出、详情和媒体释放

- 本地 Markdown 导出在 host 侧用 64 KiB 缓冲写临时文件，完成 `flush/sync/close` 后原子替换目标；renderer 不再拼接完整会话字符串。
- 本地导出明确按 tab 身份读取；远端旧协议保留 renderer 投影降级。远端降级只能导出当前已加载窗口，因此 UI/发布说明必须保留该兼容限制。
- 音频和视频在 URL 变化或卸载时执行 `pause -> remove src -> load`，主动释放解码器和网络资源。
- 工具预览、子调用列表和全文读取共享显式预算，折叠/切换后不保留无主请求。

### 5. 可观测性

Runtime Doctor 与 Desktop 诊断面板现在显示 skillwatch 的物理/逻辑 watcher、扫描次数、降级状态和详细计数；session pipeline 记录窗口驻留条目、最大页数和回收页数。所有指标只包含计数、耗时、大小和闭集状态，不记录消息正文或路径。

## 兼容性与回退

- 新 `history-window-v1` 能力由绑定身份决定路由；旧 Serve 返回 `unsupported` 后保持协议 7 的只向旧页读取，不用全量下载伪造“更新内容”。
- 本地/远端读失败不会自动跨 host 重试，避免瞬时错误从另一个身份源返回不同会话。
- 旧 `HistorySlice`、一次性 Markdown `parse` 和远端导出路径保留一个兼容周期。
- 所有回收都只影响内存/DOM 驻留，不删除持久化记录。

## 本地验证

- Go：`./internal/session/... ./internal/serve/... ./internal/skill/... ./internal/boot/...`；race 覆盖 skillwatch/session；`desktop/ go test ./...`。
- Frontend：`typecheck`、`test:typecheck`、`test:transcript`、`pretest`、hooks lint、单滚动写入者与 app layer 门禁。
- 关键专项：10,000 turn 双向可达与 3 页驻留、实时尾部回收、工具调用/结果成对释放、4/2 正文并发、16 KiB/64 块预览、20 项详情分页、worker 文档协议与优先级、媒体释放、host 导出原子发布。
- `repolint` 无新增预算债务，不需要更新基线。

2026-09-15 在 macOS arm64 / Chromium 153 上实跑的浏览器 fixture 首次通过：240 turns 的初载 129.3 ms、深翻页 2040.7 ms、流式提交 1173.6 ms、输入 p95 24.1 ms、DOM 12,090；1000 turns 兼容压力场景的对应值为 147.6 ms、23175.6 ms、4987.7 ms、45.9 ms、DOM 47,834。会话切换 p95 44 ms，测试区间 JS heap 增长 349,380 bytes，anchor drift 0。1000 turns 的全页 DOM 数字也说明 12,000 预算目前按单个 Markdown 发布页执行，并非全会话全局硬上限。

生产前端构建的全部 bundle 预算通过；`desktop/ go test ./...` 全绿（主包 335.895 s）。根 `go test ./...` 仅有 3 个 `cmd/reasonix-legacy-migrator` 既存失败，错误均为 `pending update belongs to a different installation: pending update bundle is not the current Guard installation`；资源治理涉及的其余根包通过。

## 尚未被本地证据证明的事项

- 未在 1.38.8 原生包采集 CPU、RSS、JS heap、DOM、轮询频率基线，因此不能给出可信的版本前后百分比。
- macOS 浏览器/Electron 基准只能证明当前分支的预算和回归门禁；不能替代 Windows WebView2、Linux WebKit 或三平台安装包验证。
- Windows 轮询与全量挂载对原反馈的贡献仍是源码推断，需在真实包上用相同 fixture 采样。
- 本地 Markdown 文件是流式写入，但兼容控制器仍可能先提供完整 `HistoryForTab` 切片；它解决 renderer 全量字符串峰值，不等同于所有旧存储路径都已常量内存化。

发布前应使用同一长会话 fixture 对 1.38.8 与候选包各进行冷启动、会话切换、15 分钟流式运行、深翻页和导出采样，并记录主进程/renderer CPU、RSS、JS heap、DOM 节点、worker 创建次数与读取并发峰值。三平台任一出现游标不可达、选区丢失、导出不完整或持续增长，应阻止发布而不是放宽预算。
