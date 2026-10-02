# 消息队列：行内编辑与排序的实现和验证

日期：2026-09-21。变更在本地工作区；未发布安装包。

## 用户可见行为

- 正在运行时，普通发送及发送快捷键默认加入下一轮队列；“引导当前轮”是独立操作。结构化指令和图片仍通过独立轮次执行。
- 队列与输入区连在一起，默认显示两条；点击编辑时在原消息行打开完整多行正文，不自动展开其他条目。编辑中的行只展示正文、附件/引用与“取消 / 保存”。
- 编辑时隐藏主输入区、上下文附件和固定文件栏，保持挂载以保留主草稿及附件。保存或取消后恢复输入区与焦点。停止当前任务的入口在编辑时移到队列上方，退出编辑后恢复原有主输入区按钮。
- 保存原位更新消息，保留 ID、顺序、附件、引用和幂等身份。`Ctrl/⌘ + Enter` 保存，`Esc` 仅退出编辑，不停止当前任务。取消时未改动的正文直接清理；已改动或冲突的正文保留临时草稿供恢复。
- 手柄支持鼠标拖拽，也支持空格拾取、方向键移动、空格放下。更多菜单提供上移、下移、移到队首、移到队尾及删除。
- “暂停队列”收纳在标题的更多菜单中；暂停后显示状态及“继续队列”入口。只暂停后续派发，当前任务继续运行。暂停期间仍可编辑、排序和删除；保存不会自动恢复执行。
- 已接纳的引导显示接收状态，不提供编辑或排序。blocked/uncertain 条目明确标示原因，并提供显式重试。
- 发生内容冲突、消息已执行、会话变化或保存回包丢失时，保留修改；可读取最新版本，或把修改追加回主输入框。读取最新版本前，旧修改也会保留在草稿列表。
- 停止当前任务不再删除待处理消息，也不会把截断的预览文本恢复到输入框。

## 从 ZCode 移植的部分

来源为 `zai-org/ZCode` 的 `872ad960de7ec172591f7e1952f7849229f94521`，
`packages/ui/src/v4/ConversationQueuePanel.tsx`：

- `@dnd-kit/core`、`sortable`、`utilities` 的排序结构；
- 独立拖拽手柄、纵向边界约束、保持缩放为 1 的行变换；
- 紧凑列表、折叠/展开、基于 ID 和目标锚点的移动；
- 最初版本采用在主输入区处理队列编辑的交互；用户随后选定 A 方案，已改为原消息行内编辑、编辑期间隐藏主输入区。

`desktop/frontend/licenses/ZCode.txt` 保留 Apache-2.0 许可与改动声明。
样式使用 Reasonix 已有字体、色彩、按钮、Popover 和主题变量。
编辑采用原位保存；不会采用 ZCode 原实现的“删除条目、回填输入框、重发入队”。

## 数据与并发规则

| 层 | 实现 |
| --- | --- |
| Store | `ContentVersion` 绑定不可变正文 blob 与 checksum；保存做正文版本比较，移动做 Inbox revision 比较；移动只改 pending 槽位 |
| 派发 | `TransitionPrepared` 在同一磁盘事务中检查状态、正文版本、暂停状态和当前队首；过时候选重新选取 |
| Controller | `InboxQueue` 固定原 Store；read 返回完整正文；保留原 envelope，引用变化时重新冻结；旧 update/append/refresh 路径也使用版本检查 |
| Desktop | `InboxQueueForTarget` 校验 tab、sessionPath、generation、selection；本地操作持有既有 admission fence |
| Remote | `inbox-mutations-v1` 能力协商，`POST /inbox/queue` 与 expected-session header；不支持时展示升级提示，不回退到本地 |
| Frontend | 编辑草稿与主输入草稿分别保存，scope 包含会话及主机身份；快照按 revision 单调应用；拖拽与菜单统一使用 before-item 锚点 |

`POST /inbox/queue` 请求为 `{sessionPath, request}`。操作包括 snapshot、read、edit、move、delete、pause、retry、steer、enqueue_steer。
移动到队尾时客户端明确传 `beforeItemId: null`。结果包含 outcome、reason、权威 snapshot，以及可选 edit/receipt。

保存超时后只查询一次 snapshot，不自动再次保存或创建新消息。编辑草稿保存在独立的内存与 sessionStorage 中，支持会话切换、组件重新挂载和同一页面刷新；不承诺退出应用后保留未保存草稿。已保存正文、队列顺序和暂停状态由 Store 持久化。

## 验证证据

### 行内编辑 Bug 复查（2026-09-21）

- 修复离开并返回同一会话后，旧保存回包清掉新编辑草稿的问题。每次挂载拥有独立操作身份，异步完成时同时核对操作身份与草稿对象；失效回包不再更新快照、草稿或忙碌状态。
- 修复切走会话后，延迟完成的目标捕获仍发出队列操作的问题。捕获目标、提交请求及处理回包前后均检查当前操作是否有效；失效保存失败也不会继续发送状态核对请求。
- 修复会话 selection 变化后“读取最新版本”仍复用旧目标、无法恢复的问题。显式读取最新版本重新捕获当前目标，并保留原修改到恢复列表；普通保存仍校验原目标和正文版本。
- 修复焦点位于编辑区按钮时 `Esc` 无效的问题。键盘处理覆盖整个编辑区，同时保留输入法组合保护，避免冒泡触发停止任务。

新增确定性生命周期测试覆盖延迟保存成功、目标捕获、完整正文读取、保存失败，以及 selection 变化后的恢复。三个相关 React 测试文件、测试类型检查和生产构建通过。浏览器预览验证保存按钮聚焦时 Esc 能退出、恢复主草稿及焦点，当前任务继续运行，控制台无警告或错误。本轮仅修改前端，未重跑 Go 或安装包测试。

### 此前行内编辑验证

本次行内编辑改造额外验证：

- 原行挂载、单一编辑器、只显示取消与保存、不自动展开队列；主附件 DOM 身份和主草稿在保存及取消后保持不变。
- `Esc` 不触发停止；无修改取消不会生成恢复提示；已修改的取消可以继续编辑。
- 保存冲突仍保留行内正文；消息离开队列时转为明确的恢复区，停止任务也不会丢弃这些文字。
- 浏览器在 1280×720、900×700 验证保存、取消、焦点恢复、临时草稿恢复、键盘排序、指针拖拽和标题菜单暂停。以下 Go 验证为前一阶段后端实现的已有结果，本轮只改前端，没有重跑 Go 套件。

- Store 确定性双 writer 测试：保存先完成则旧正文派发失败，排序先完成则旧队首派发失败，过时的引用失败不能把新状态改为 blocked；已开始执行的条目不可编辑。
- Controller 实际调度测试：暂停 → 编辑第二条 → 移到队首 → 恢复；runner 先收到第二条的新正文，再收到原第一条。
- 保存长正文及空白、完整 envelope 保留、正文冲突、丢失锚点、重启后顺序及暂停状态保留。
- Desktop + httptest Serve：远程完整读取和保存、旧服务能力拒绝、selection 变化和远端 foreground 变化拒绝。
- React 回归：主草稿保留、默认入队与显式引导分开、保存不重发、停止不撤回队列、冲突草稿恢复、丢失回包只查询一次、不同 scope 的草稿隔离；同路径切换远程主机后，允许新主机较低的队列 revision，不保留旧主机条目。
- 浏览器使用生产组件和开发预览数据验证编辑、保存、暂停、菜单置顶、键盘排序和拖拽；1440×1024 与 900×700 编辑态可用。预览不连接真实模型或生产会话。
- 前端生产构建、TypeScript、ESLint、CSS/层边界及包体预算检查；保持原预算。
- Go Store/Controller/Serve 包测试，以及 Desktop Inbox/host ownership 测试；针对编辑、排序和实际派发运行 race 检查。

主要验证命令：

```sh
go test ./internal/sessioninbox ./internal/control ./internal/serve
go test -race ./internal/sessioninbox ./internal/control -run 'TestVersionedAppend|TestInboxQueue|TestPreparedQueue|TestQueuePause'
cd desktop
go test . -run 'Test.*Inbox|TestHostCommandOwnership'
cd frontend
pnpm build
pnpm test:typecheck
node --import ./scripts/css-stub-register.mjs --import tsx src/__tests__/inbox-queue-lifecycle.test.tsx
node --import ./scripts/css-stub-register.mjs --import tsx src/__tests__/inbox-queue-editor.test.tsx
node --import ./scripts/css-stub-register.mjs --import tsx src/__tests__/composer-queue-integration.test.tsx
```

复现入口：`cd desktop/frontend && pnpm dev --host 127.0.0.1 --port 5191`，打开 `http://127.0.0.1:5191/?mock=guidance`。
浏览器预览仅用于开发，生产构建会移除该 fixture。视觉检查见 `docs/INBOX_QUEUE_DESIGN_QA.md`。

## 明确边界

- 结构化调用的偏移量与指令实体不能安全地被纯文本编辑，因此显示明确的暂不支持提示；仍可排序和删除。
- 当前未在用户原报错会话或安装包中复现；无法断言截图那一次失败的唯一根因。
- 当前未创建 PR、推送、安装新包或部署远程服务；本地验证不代表这些步骤已完成。
