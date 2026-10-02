# 自然文档流聊天区架构

本文描述唯一生产聊天实现，替代 TranscriptKernel、虚拟窗口及尺寸账本。渲染参考本地 DeepSeek Harness `c291e7961a`；保留 Reasonix controller、存储、输入框、审批和外围工作台。当前同步与历史行为遵循[会话同步 v2](TRANSCRIPT_V2.md)及[滚动与历史契约](TRANSCRIPT_SCROLL_CONTRACT.zh-CN.md)。完整轮次导航通过持久化摘要索引和直接历史窗口定位实现。

## 职责划分

```text
本地 / 远程 Follow v2 → 共享消费器与有界记录存储
                              ↓
                    Transcript 会话适配入口
                              ↓
              ChatSource：稳定顺序、独立节点与状态订阅
                              ↓
        ChatNodeList → ChatNodeSeat → 消息 / 过程 / 工具 / 记录
                              ↓
                         自然文档流

原生输入与 DOM 变化 → ChatScrollController → TranscriptViewportWriter
完整内容引用 → ChatContentLoader → 绑定会话的正文 API
Markdown 源文 → 共享 Worker → 稳定前缀块与可变流式尾部
```

- `src/lib/chatViewSource.ts` 仅维护可重建展示投影。key 来自消息、调用及用户轮次身份；无变化的顺序和节点保留引用。结构更新合并到微任务；流式复用 controller 按帧发布，按 LiveStream.id 定位，结束后仍使用同一助手宿主。
- `src/components/Transcript.tsx` 统一适配本地和远程。列表只订阅顺序，各节点订阅自身及过程折叠；计时、导航和抽屉独立订阅。切换释放监听、待发布任务、全文租约和观察器。
- `src/components/ChatNodes.tsx` 展示用户、回答、思考、过程、工具、通知、压缩、扩展和轮次操作。收起的重内容不挂载。
- 已加载历史全部使用普通块布局。聊天中不再有虚拟窗口、绝对定位行、冷热交接、尺寸账本、几何状态回路、逻辑选区、渲染模式切换或异常后重挂载。其他消费者使用的 TanStack 依赖保留。

## 功能取舍

正文最大宽度 800 px，左右留白 24 px，窄容器 16 px，沿用字体和主题。使用原生选区及滚动条。历史采用双向有界窗口，默认保留相邻三页，每页 32 条消息；翻页超出预算时回收另一端的页面。导航通过分页摘要覆盖全部可见持久化轮次；正文仍受常驻窗口预算约束。

## 完整轮次导航与历史访问

导航通过 `history-outline-v1` 从权威历史索引读取摘要，不依赖 Follow 的有界尾部投影。每页固定存储 generation 和 snapshotSequence。每会话最多缓存 6 页、每页 128 轮，全 renderer 摘要预算 8 MiB；导航只渲染可见标记及两端各 4 个缓冲标记。回收摘要不会减少总轮数，需要时重新加载预览。

点击未加载轮次后，以稳定 messageId 直接请求从该消息开始、朝较新方向的 32 条正文窗口，不加载中间页面。共享历史 Store 负责窗口替换、翻页与请求隔离；目标 DOM 挂载后由 ChatScrollController 统一定位。新点击、读者输入和会话替换取消旧请求的可见提交及滚动。切面失效最多按原 messageId 自动重新定位一次，不用可能已变动的轮次号猜测目标。

Follow 在消息提交和持久化覆盖变化时使摘要失效，普通 token 不触发目录读取。目录刷新不重启 Follow、不替换正文。缺少可选目录能力的远端保留已加载轮次导航并提示升级；强制 transcript-v2 的要求不变。

此次恢复 #10385 收缩前的完整导航产品能力，同时保留有界正文窗口。#10276 验收记录仍是历史文档，其中累积加载正文的旧跳转实现不会恢复。

## 全文及异步隔离

沿用历史游标。prepend 增加节点并修补跨页轮次，replace 按权威源重建；快照 revision、会话 generation、重试 attempt 仍由现有层负责。

每个挂载会话最多四个全文请求。相同源内容共享 Promise，内容变更不复用旧请求；释放使排队及在途结果失效，完成后移出请求表，不建立无界全文缓存。

用户和回答自动补齐，思考及工具按全文展开或复制读取。快照按字段解析，读正文不会顺带加载思考。工具详情读取完整原始不可变记录，绕过 Item 的预览与归档限制；全文不回写 controller，也不进入快照缓存。小型引用和内联工具记录遵守已有非活动缓存预算，保证再次打开可重新读取。引用过期走已有重载流程并显示可重试错误。

旧版历史存储也取消整页引用预取，避免绕过加载器的并发预算。旧版工具按调用 ID 读取对应引用，不展开其他调用，也不缓存取回的工具全文。未解决或过期的正文引用不能回退为一次成功的预览复制。

接受结果前核对源身份；关闭、更换目标、切换会话使旧回调失效。Worker 核对消息、文本 revision 和挂载生命周期。`surfaceCommitToken` 仅在正确首批 DOM 提交并经过两次动画帧机会后报告就绪，旧 effect 取消。

## 滚动与 Markdown

`ChatScrollController` 在 React 外保存跟随意图、节点 key、视口偏移、前序 key、原生位置和任务代次。`TranscriptViewportWriter` 是唯一直接写聊天 scrollTop 的模块，静态检查持续约束。

首次进入跟随最新，返回会话恢复有界内存中的位置。微小向上滚轮也解除跟随，包括底部 24 px 内；触控、滚动键和滚动条取得阅读控制，向下到达底部 24 px 内恢复跟随。返回最新和进入运行的新用户轮次明确恢复跟随。

分页、折叠、图片、Markdown 及输入区高度变化依据稳定节点和偏移恢复。过程子节点消失时使用摘要，节点删除时使用仍存在的前序节点或首个节点。分页期间继续滚动会更新锚点，不采用整体 scrollHeight 差值补偿。

一个 ResizeObserver 观察正文、视口和非空节点，MutationObserver 更新目标集合，统一合并到动画帧。没有尺寸 React state，也不在 ResizeObserver 交付中同步写滚动。禁用浏览器自动锚定；无变化的写入为 no-op，内容稳定后停止持续写入。

流式及完成消息共享 MarkdownHistory。Worker 复用稳定前缀块、更新尾部；结束完整解析引用、脚注和未闭合语法，不替换整条回答。错误仅影响本消息，显示可复制原文与提示。表格使用自然行与横向滚动，长代码只做展开，不建立纵向虚拟窗口。

在常驻历史窗口内，视口外已完成源文仍以纯文本挂载，靠近视口启用 Worker 格式化；格式化后的块仅在所属页面常驻期间保留。页面回收会释放对应行与解析任务，延后格式化不代表无限保留曾加载的文字。收起过程重内容按产品规则卸载。

Worker 由挂载会话租用，最后释放时结束待处理任务；累计诊断只保留数字，不保留源文或 AST。会话卸载清理观察器、节点监听及全文。非活动历史使用已有有界缓存。

## 兼容及验证

当前 Desktop／Serve 协议边界为 `transcript-v2`，兼容性与派生索引变更见[会话同步 v2](TRANSCRIPT_V2.md#compatibility-and-change-notes--兼容与变更说明)。持久化会话日志编码、provider 消息、工具 schema 和 prompt-cache 字节保持不变。旧显示偏好不能选择旧渲染器。任何回退都必须保持 Desktop 与 Serve 协议兼容；仅回退前端不构成通用兼容保证。

[英文说明](TRANSCRIPT_ARCHITECTURE.md#compatibility-and-verification)列出可复现命令。覆盖 transcript、stream、composer、remote、app lifecycle、motion、类型检查、lint、构建、bundle 预算、全量测试发现及浏览器回放。

聊天回放在生产构建中使用真实 Transcript、Markdown、Composer；工作台基准使用真实应用装配与现有 mock transport。它们不代替真实后端或原生输入法长时测试。门槛仍为输入 P95 ≤200 ms、切换 P95 ≤300 ms、长任务 ≤500 ms、释放后堆增长 ≤20 MiB。

实测数据、截图及未验证平台见[改造验收记录](CHAT_REFACTOR_ACCEPTANCE.zh-CN.md)。
