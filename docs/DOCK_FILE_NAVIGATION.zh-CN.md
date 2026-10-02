# Dock 文件导航

右侧 Dock 如何打开一个文件、结果归谁所有，以及为什么一次点击不再引发渲染
循环。改动 `WorkspaceDockRegion`、`WorkspacePanel`、`RemotePanel` 或
`lib/fileNavigation*` 之前请先读本文。

## 契约

打开文件是一条命令，不是一次渲染。

```
文件行 / Markdown 链接 / 宿主验证的回答引用 / 文件树 / 预览操作
  → 已绑定运行实例的打开命令（FileNavigationOwner）
  → 解析资源并校验该命令是否仍然有效
  → 确定目标 Dock 与预览位置
  → 提交导航记录
  → Dock 订阅并渲染已提交的结果
```

- **渲染不执行导航。** 面板通过 `useSyncExternalStore` 读取
  `owner.getSnapshot(key)`，React 渲染期间不发布任何请求。
- **资源身份、访问上下文与导航参数分离。** 预览身份由资源空间 + 后端规范路径
  决定；会话标签、来源和可选 `toolCallId` 属于 `FileAccessContext`；
  `preview`、`source`、`reveal-tree` 只是参数。切换参数复用同一个预览标签，
  切换会话则可在不改变文件身份的情况下重绑定访问权限。
- **访问凭据随命令传递。** workspace、presented 与宿主验证的回答引用使用
  不同读取入口。从另一个入口重新打开同一路径时，只使用本次命令的凭据，
  不沿用此前 presented 请求的权限；回答引用的每次读取与直接操作都会由宿主
  重新解析和授权。
- **记录按 Dock 标签索引。** 记录包含 Dock 实例身份、生命周期代数、
  带访问上下文的条目、最后一次导航意图、显示修订、内容修订与生命周期信号。
- **只有显式命令推进修订。** 等价命令会保持条目对象与条目列表完全一致，
  因此仍有效的内容读取不会被重新启动。

## Harness 设计 → Reasonix 实现

| Harness 设计 | Reasonix 实现 |
| --- | --- |
| 命令驱动导航：打开由事件触发，渲染只读取结果 | `FileNavigationOwner`（`lib/fileNavigationOwner.ts`）提交记录；`WorkspaceDockRegion` 不再在渲染中构造请求；`useFileNavigationRecord`（`app-shell/useFileNavigation.ts`）只读取 |
| 稳定资源身份：按文件规范坐标识别，不按对象引用 | `FileResourceRef` / `FileAccessContext` / `ResolvedFileResource`（`lib/fileResource.ts`）；后端解析的 `identityPath` 合并相对/绝对路径别名，访问上下文另行区分 workspace、presented 与验证引用读取 |
| 导航参数与身份分离 | `FileNavigationParams`——`action`（`preview`/`source`/`reveal-tree`）与 `view`（`files`/`changed`）不改变资源本身 |
| 每个 Dock 独立导航实例 | 每个运行实例一个 `FileNavigationOwner`（`useFileNavigationRuntime`），每个 Dock 标签一条记录，每次生命周期一个 `generation` |
| 命令结果上报而非抛出 | `FileNavigationOutcome`——`opened` / `cancelled`（superseded、closed、disposed）/ `failed`；取消不显示错误 |
| 取消浏览器预览时的 URL 归属 | `openBrowserPreview` 只释放一次 URL：交接后归标签页所有，未交接则由命令撤销 |
| 取消全局化 | 删除 `fileNavigationLifetime.ts`；新命令取代该记录尚未完成的旧命令，`retain`/`bindScope` 结束记录生命周期 |
| 恢复不重放命令 | 记录在 Dock 折叠后保留；重新挂载只读取已保留的选择，不重新应用最后一次意图，也不重新执行打开命令 |

未移植：分栏、浮窗、布局撤销历史、Cordis 插件框架，以及
`dsh-resource://` 外部协议（资源标识仅用于内部导航）。

## 生命周期与取消

| 事件 | 效果 |
| --- | --- |
| 同一 Dock 的新命令 | 中止该记录尚未完成的旧命令，旧结果返回 `cancelled` |
| 发往其他 Dock 的命令 | 无影响：面板之间不互相取消 |
| Dock 标签关闭或移出当前工作区 | `retain` 删除记录并中止其生命周期 |
| 同一项目内切换会话 | `bindScope` 保留条目与选择，并把访问上下文替换为当前会话的 workspace 权限 |
| 项目或远程主机资源空间变化 | `bindScope` 重建记录：条目清空、代数递增、旧生命周期中止 |
| Dock 折叠（`workspacePanelOpen` 为 false） | 无影响：展开时正是靠该记录恢复 |
| 运行实例卸载 | `dispose` 中止全部记录与一次性操作 |
| 关闭后复用同一标签 ID | 使用新的生命周期代数；记忆中的路径仅以 workspace 访问权限恢复 |

## 持久化

只持久化路径（`workspaceViewMemory`、Dock 标签列表）。生命周期代数、取消信号
与访问上下文仅存于内存，不写入 `localStorage`。旧版本写入的存储按路径读取，
并重新走当前工作区校验，因此恢复的预览不会重新获得 presented 工具范围。

不修改 Go/Electron 桥接负载、会话日志、工具 schema、standing instruction 或
provider 请求字节，对提示缓存无影响。

## 验证

| 范围 | 测试 |
| --- | --- |
| 导航实例 | `file-navigation-owner.test.ts` |
| 命令顺序与取消 | `file-navigation-races.test.ts` |
| 验证回答引用的读取与源码切换 | `workspace-reference-reader.test.tsx` |
| 真实 Dock 链路（本地与远程） | `dock-file-navigation.test.tsx` |
| 预览标签、上限、源码模式、定位、代数 | `file-navigation-dock.test.tsx` |
| 渲染循环缺陷、StrictMode、重渲染、重挂载 | `file-navigation-lifecycle.test.tsx` |
| 远程读取、保存隔离、断连 | `remote-file-navigation-races.test.tsx` |
| Dock 请求投递 | `dock-navigation.test.ts`、`dock-view-requests.test.tsx` |
| 真实 DOM 与 Electron | `bench/dock-file-navigation.mjs`（`test:app-browser`、`test:dock-electron`） |
| 回答引用点击进入运行实例 Owner | `bench/chat-file-reference.mjs`（`test:chat-file-browser`） |
