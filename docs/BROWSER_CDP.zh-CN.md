# CDP 浏览器后端

[English](BROWSER_CDP.md)

[桌面浏览器](DESKTOP_BROWSER.zh-CN.md)由 Electron 外壳提供，用户和 agent 共用同一个
Chromium 界面。CLI、`reasonix serve` 和 headless 会话背后没有外壳，于是同一批
`browser_*` 工具注册之后无人应答，只能失败关闭。

这个后端是宿主中立的 `browser.Executor` 的第三个实现（前两个是 Electron 外壳和
SSH broker）：它通过 DevTools 协议驱动一个外部 Chrome。工具、描述和 schema 都没有
变化，变化的只是谁来应答。

## 启用

```toml
[browser]
enabled = true
```

普通路径到此为止。第一次调用浏览器工具时，Reasonix 才会用一次性 profile 启动一个
自己持有的 Chrome，并随会话一起结束。会话启动时不做任何事：没碰过浏览器的会话不为
浏览器付出任何代价。

要驱动已经在运行的 Chrome，先用调试端口启动它，再把 endpoint 指过去：

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --remote-debugging-port=9222
```

```toml
[browser]
enabled = true
endpoint = "http://127.0.0.1:9222"
```

| 配置项 | 含义 |
| --- | --- |
| `enabled` | 默认关闭。打开后工具才会拿到真实浏览器。 |
| `endpoint` | 运行中 Chrome 的 DevTools 端点。留空则自行启动一个。 |
| `allow_remote_endpoint` | 允许非 loopback 端点，默认关闭。 |
| `chrome_path` | 浏览器可执行文件。留空则按常见位置查找 Chrome、Chromium 和 Edge，再读 `REASONIX_CHROME` 与 `CHROME_PATH`。 |
| `chrome_args` | 额外启动参数，例如代理。 |
| `user_data_dir` | 自行启动时使用的 profile。留空使用一次性目录，登录态不会比会话活得更久。 |
| `headless` | 无窗口启动。 |

桌面端忽略这一节：它本来就持有浏览器，宿主自带的浏览器由宿主继续持有。

## agent 能碰到什么

只有这个后端自己打开的标签页。被 attach 的 Chrome 通常还开着用户自己的已登录标签页，
`browser_tabs` 从不枚举它们，因此 agent 既读不到也驱动不了用户没有交给它的页面。
`temporary` 标签页使用独立的浏览器 context，不共享任何 cookie，并随标签页一起丢弃。

## 这个后端负责的拒绝语义

裸浏览器不会记录 agent 让它做过什么，所以 Electron 外壳里由 ledger 提供的保证，在
这里由本后端自己负责：

- **`operationId` 只能用一次。** 重放的 id 被永久拒绝。上一次尝试的效果（包括未知
  的效果）已经成立，因此模型被告知重新读取页面，而不是再试一次。
- **`documentToken` 把 ref 绑定到一个文档版本。** 每次 `browser_snapshot` 铸造一个
  新的不透明 token；导航、页面替换和用户接管都会让它失效。携带失效 token 的写操作
  按 stale 拒绝，而不是对着模型没见过的页面重放。
- **未知结果既少见又诚实。** 在任何东西到达页面之前失败，报告为未执行，模型可以据此
  改写计划。只有在输入**已经**落到页面之后失败——点击的第二个事件、页面脚本执行到
  一半抛错——才报告未知结果，而未知结果永远不得重试。

### 这里的接管检测是近似的

外壳能知道人碰了页面，是因为它的 guest preload 能看到并非自己合成的可信输入。
CDP 派发的输入一旦到达 DOM，就和真人敲键盘无法区分，所以这个后端在自己每次派发前后
标记一个短窗口，把窗口之外的可信输入算作用户的。落在窗口内的人工点击会被漏掉。
其失效模式是快照过期，而绝不会是静默重放，因为每次写操作仍然携带一次性的
`operationId`。

ref 与接管计数器都活在按文档创建的隔离世界里，页面脚本既读不到 agent 的 ref，也伪造
不了计数器。

## 产物

截图和下载落在一个随会话删除的私有目录里。下载保留服务端建议的文件名，经过清洗以保证
文件名无法逃出该目录，并且绝不覆盖已存在的文件。

## 安全

DevTools 端点等于把该浏览器以及它能读到的所有文件的完全控制权交出去。因此除非显式
设置 `allow_remote_endpoint`，非 loopback 的 `endpoint` 一律拒绝。

`browser_upload` 只能读取会话的写根目录（工作区及附加目录）以及本执行器自己的产物
目录，这样 agent 刚下载的文件仍然可以上传。符号链接在包含性检查之前就被解析，因此
工作区里的链接无法把文件输入框指向工作区之外的密钥。其他路径一律带原因拒绝，不会交给
页面：页面是不可信的，而文件输入框就是一条上传通道。

## 缓存

这里没有任何东西对 provider 可见。工具只进 registry，通过 `use_capability` 触达，
因此无论有没有挂上浏览器，请求里的工具数组和系统提示词前缀都逐字节一致。守卫测试是
`internal/boot` 中的 `TestConfiguredBrowserBackendStaysOffTheProviderSurface`。

## 验证改动

```bash
go test ./internal/browser/... ./internal/boot/
```

单元测试跑在一个脚本化的 DevTools 服务器上。注入页面的 helper——快照遍历、ref 表、
接管监听——只有对着真实浏览器才算真正被执行过：

```bash
REASONIX_LIVE_CHROME=1 go test ./internal/browser/cdp -run '^TestLiveChrome$' -v -count=1
```

该测试需要本机安装 Chrome，否则保持跳过。

## 限制

- 仅支持 Chrome、Chromium 和基于 Chromium 的 Edge。
- agent 的标签页只属于它自己，没有办法把用户的标签页交给它。
- `browser_select` 通过 DOM 驱动 `<select>`，因为原生下拉由平台渲染，无法用合成鼠标
  事件操作。其余写操作都使用真实输入事件。
- 单次快照最多 2000 个节点；用 `selector` 把范围缩到一个子树。
