<!--
  Template for the GitHub release notes of the Termux build.
  verify-and-release.yml renders it with envsubst-style substitution, so the
  version, asset name and checksum never drift from what was actually uploaded.
  Placeholders: $TAG $REL_TAG $ASSET $VERSION $HASH $REPO
-->
# Reasonix $TAG for Termux (Android arm64)

Termux 定制版构建，基线为上游 [Reasonix](https://github.com/esengine/DeepSeek-Reasonix) `$TAG`，
追加 Android 平台适配并修复了几个在 Termux 上无法正常使用的交互缺陷。

内嵌版本：`$VERSION`

> [!IMPORTANT]
> **首次在 Termux 使用请先阅读：**
> [Termux / Android 安装、沙箱限制与 wrapper 指南](https://github.com/$REPO/blob/master/docs/TERMUX.zh-CN.md)。
> 默认 `workspace-write` 在 Android 上因缺少可用的 bubblewrap backend，shell 会
> 安全地拒绝运行；文档说明了 `danger-full-access` 的风险边界和可选解决办法。

## 本次更新：v1.39.11 Termux r2

发布标识：`$REL_TAG`。上游受支持的 1.x 最新基线仍是 `v1.39.11`；这是 Termux
修订版，不冒充新的上游版本，不迁移到 Studio 2.x，不移动或覆盖旧 tag/资产。
保留此前 Android 适配、中文搜索、历史回放、模型窗口、模型切换和 r1 模型能力修复。

### 修复：多会话同目录操作不再挂起等待

- 明确路径的 `write_file` / `edit_file` / `move_file` 等文件工具允许同目录内
  不同文件并行写入。写锁冲突立即向 AI 返回 `blocked: WRITE_CONFLICT`，不再
  因其他会话写入显示等待提示并挂起本次工具调用。
- 冲突提示包含请求的文件或保护范围，明确本次工具、hooks、checkpoint 尚未执行，
  没有排队或自动重试。引导 AI 选择/创建其他文件继续独立工作；若必须修改同一文件，
  在对方写完后重新读取、合并修改再重试，不通过 shell 绕过冲突保护。
- 任意脚本、编译、修改型 `git`、未知 MCP 和可写 hooks 无法证明完整写入目标，
  仍保留执行期间的目录级保护，但冲突同样立即返回。提示区分文件争用、目录/祖先
  保护和本会话的后台写入；`additional_write_dirs` 是权限，不是文件级锁声明。
- 一次性 `bash` / `sh` 中目标可证明的简单 `echo` / `printf` 重定向可使用文件锁。
  persistent shell、startup code、导出函数、host terminal、后台/保留进程命令
  保持目录级保护。同会话子代理调度规则与后台任务既有租约生命周期不变。
- 保留文件版本检查、旧锁协议和 Termux 权限策略；不关闭保护、不增加新权限，
  不改变 provider-visible tool schemas。极少数不同文件会因哈希条带碰撞收到
  保守冲突，提示会说明；Reasonix 外部进程不受这套协作锁强制隔离。

**升级注意：** 已运行的会话仍用旧代码，可能持有目录级锁。请让操作同一目录的
相关会话都保存工作并重新启动新版，才能完整体验。不要删除正在使用的锁文件。
安装只更新真实二进制，保留已有 Full access wrapper；sandbox 的平台限制未改变。

### r2 验证与保留机制

- 补充文件并行、跨进程冲突、队列争用和多文件回滚等回归测试，保留过期读取保护。
- 累积补丁与源码一致，关键修复和测试纳入升级校验，避免同步上游时丢失。
- Android/Termux 的 sandbox、硬链接等平台限制仍存在；本次不改变这些限制。

## 保留的 r1 更新：模型能力与思考强度

### 新增：setup 图片能力自动读取

- 新建连接“从 URL 获取模型”和已有连接刷新，复用同一次 `GET /models` 读取 img
  元数据，显示“支持 / 不支持 / 未知”及来源。兼容 OpenRouter 的
  `architecture.input_modalities`，不按模型后缀猜测，不发送推理实测请求。
- 能力先进入 setup 草稿，保存成功后写入 24 小时模型能力缓存；手动 `vision`
  优先，取消、保存冲突、换端点/凭据不错误复用发现。
- 修复 adapter 与 Agent 将“未知”当“不支持”的路径。没有图片理解 fallback 时，
  用户明确附加的图片可发送给当前未知自定义模型，仍标明未知而非已验证；配置
  fallback 时保持原路由。明确纯文本声明和官方协议硬限制仍保留。
- 本次仅支持 img，不启用 audio/video/native PDF 传输。普通文本请求不增加图片或改变提示词。

**能力识别仅供参考，并非实际可用性保证：** 自动读取的是 `/models` 元数据，不会发送
图片请求验证。网关可能缺失、过时或错误声明，CLI 能识别的字段、协议适配以及图片附件
传输也有实现边界，因此显示结果可能与实际识图效果不同。“支持”不保证该端点一定能
处理图片；“未知”不等于不支持；“不支持”也可能来自端点声明、手动设置或当前适配限制，
不能据此断言底层模型本身的能力。请结合显示的来源、服务商文档和实际附图请求确认。
手动 `vision = true` 只修改能力声明，不会让纯文本模型或不兼容协议获得识图能力。

### 新增：setup 思考声明与每模型独立 effort

- `GET /models` 可读取 `reasoning` / `supports_reasoning`、`supported_parameters`、
  `reasoning_protocol`、`supported_efforts` / `reasoning_efforts`、`default_effort`，
  以及嵌套 `capabilities` / `reasoning` 声明。只有布尔或参数名时档位仍未知，
  不自动编造 low/high/max，不按模型名替网关选择协议。
- 保存后写入每模型 `discovered_reasoning`，与手动声明分开，24 小时有效并绑定实际
  端点/模型/凭据。协议未知、不兼容、重复/冲突/非法档位不启用；刷新失败保留，
  成功响应移除字段则撤掉旧发现。GLM/Kimi 等固定 adapter 限制仍优先。
- 新增“编辑各模型思考强度”，支持具体档位、auto、继承。`/effort` 只保存当前模型，
  不影响同分组其他模型；会话 `--effort` 优先。模型 `effort = ""` 为 auto，删除该
  字段恢复分组继承。已有分组固定 effort 不自动清除，未单独设置的模型保持旧行为。
- 旧版程序忽略新字段，降级不保证保留模型级 effort/发现记录；当前版本可读旧配置。
  更新后先用 `/effort` 或 setup 检查各模型设置，尤其混合协议网关。

### 更新、保留和验证机制

- 升级前和补丁应用后都核查修复定义/关键测试；支持 `--verify-local` 无联网校验。
  累积补丁与源码一致性检查及干净上游重放作为发布前门槛。
- 新增 `termux-vX.Y.Z-rN` 修订发布规则：上游基线、内嵌版本、tag、资产名和
  SHA256SUMS 一起校验，master/tag 原子推送，旧 tag 不移动，发布说明从模板渲染。
- 图片能力和思考档位来自接口声明或已有设置，自动读取不是端点行为实测；
  能否实际生效仍受服务端、网关和当前协议适配约束。

### 上游安全修复及同步内容

- 修复 Python 只读命令误判：`python3 -v script.py` 会运行脚本，不再绕过审批；
  只认可精确的 `--version` / `-V` 版本查询。关联 PR #14928、GHSA-qrrg-f72x-hx7r。
  `danger-full-access` 仍按用户选择放开执行权限，不因本修复变成受限沙箱。
- 同步完整 tag 中的终端 grapheme/ANSI 换行、底部面板高度和键盘滚动修复，
  防止长面板挤掉输入框和状态栏；Termux 可用 `Alt+PgUp/PgDn` 查看超长面板。
- 同步严重超窗时的分批压缩、历史图片失败降级，以及 SQLite 查询索引校验复用。
- 修复模型请求在发送阶段可能长时间等待的问题，并增强传输失败诊断。
  发送阶段预算为 300 秒，不限制健康 SSE 长流总时长；部分 HTTP/2 错误仅影响后续
  请求的临时兼容选择，不自动重放当前失败请求。

### 本次 Termux 审查修复

- 升级前拒绝未提交/未跟踪修改；网络查询失败不触发破坏性回滚。仅替换已跟踪
  源码，移除整目录删除与 `git clean`，保留本地产物和状态。
- Android 新文件创建改用 `renameat2(RENAME_NOREPLACE)` 原子发布，不暴露半写文件；
  不支持原子操作时安全报错，不回退到直接复制最终目标。
- wrapper 保持 `run/chat/code` 的子命令路由，其他命令原样放行；尊重显式权限选择。
- 中文搜索无条件保留跨读取的 UTF-8 pending，避免半个汉字被丢弃。
- 索引尾部回放保留省略计数并调整到用户回合边界；尾读接入上游统一索引校验入口。
- Termux 构建 workflow 固定当前仓库的触发 SHA，避免构建其他 fork 的可变 master。

新增回归测试与哨兵；累积补丁按 `v1.39.11` 重建。升级不要求手动迁移配置，
建议同时更新 wrapper，原始二进制与 wrapper 可独立更换。

## 相对上游改了什么

### 平台适配

- `internal/notify/sender_android.go` —— Android 通知走 `termux-notification`
- `internal/mcplaunch/`、`internal/repair/` —— Android 纳入 Unix 进程存活检测，排除 other stub
- `internal/fileutil/atomicwrite.go` —— Android 文件系统的原子写
- `internal/tool/builtin/bash.go` —— Termux shell 路径与进程组处理
- `internal/agent/migrate.go`、`internal/cli/theme_osc_*.go` —— 终端能力差异

### 修复：中文与空格打不进去

上游多个输入框用 `msg.String()` 兜底处理按键。bubbletea 的
`KeyPressMsg.String()` 对空格返回 `"space"`、对 IME 组合态键返回按键名而非文本，
结果是**中文打不进去、空格被吞**，多词搜索失效。已改为只用 `msg.Text`，与官方
textarea 一致。

涉及 `select.go`（`reasonix --resume` 菜单）、`quick_picker.go`、`skill_picker.go`、
`connection_setup.go`。其中 `select.go` 更底层 —— 它用 `term.MakeRaw` 读 stdin
**原始字节**，`buf := make([]byte, 8)` 会腰斩 UTF-8 多字节序列，
`k[0] >= 32 && k[0] < 127` 按字节判断丢弃全部非 ASCII，backspace 也按字节删。
已改为按 rune 处理。

### 修复：搜索后用方向键选不中

`reasonix --resume` 里按 `/` 搜索并输入内容后，**按上下方向键无法移动选择** ——
界面反而回到"未搜索"的样子。

根因是分支顺序而非按键编码：方向键序列 `ESC [ A` 的首字节是 `0x1b`，与取消键
Esc 完全相同，而搜索态分支表的第一个 case 就是 `k[0] == 27`。`switch` 按顺序匹配，
于是每按一次方向键都走了 Esc 分支——退出搜索、清空查询。非搜索态一直有方向键
分支，搜索态从来没有，所以只有搜索后才出问题。

已新增 `escArrow()` 识别方向键并放在 Esc 分支之前；`ESC [ 1;2B` 这类带参数的序列
同样识别。同时修复了两个连带问题：跨 read 的半个汉字曾被丢弃（`searchPending`
只在查询变化时才写回），以及方向键移动后必须重绘。

### 更新脚本统一使用累积补丁

本地 `update-reasonix.sh` 和 GitHub 同步 workflow 现在统一应用
`reasonix-termux.patch`，不再从历史里只挑一个旧适配 commit。每次本地定制改动后
用 `./rebuild-termux-patch.sh` 从当前上游 tag 到 HEAD 重建补丁；
`release-termux.sh` 在发版前强制校验补丁与源码一致，避免下一次升级静默丢修复。

同步 workflow 同时固定在受支持的 1.x 线，并显式保留 `.gitattributes`（LFS）、
发版脚本与 release notes 等仓库基础设施，不再可能因同步上游而删除这些文件。

### TUI resume 长会话提速

final-format 长会话恢复不再全量解压并重放整个 `events.frames`，而是通过已有的
history-locator sqlite 索引读取最近 200 条消息；回放顶部会提示省略了多少更早消息。
`/copy` 与 `/export` 仍读取完整历史，不受截断影响。长会话恢复减少全量读取开销，
实际耗时取决于会话规模、存储和设备性能。

### 新增 `[ui] resume_list_limit`

`/resume` 选择器原本只显示 10 条，且搜索只在这 10 条里过滤 —— 更早的会话既看不到
也搜不到。已挖出隐藏的 100 条上限并改为可配置：

```toml
[ui]
resume_list_limit = -1     # 负数 = 不限制；0/缺省 = 内置默认 10
```

### 新增：每个模型自己的上下文窗口

`model_overrides` 里的 `context_window` 一直存在，但终端里没有入口 —— 只能手改 TOML。
现在两处都能设：

```toml
model_overrides = { "glm-5.3" = { context_window = 202752, max_output_tokens = 32768 } }
```

`reasonix setup` → 选中 provider → **编辑各模型上下文窗口**：逐个模型提示，空回答保持
不变，填 `0` 或负数则清除该模型的窗口、恢复继承连接级设置（这是唯一的撤销方式）。
菜单项本身会显示当前形态（`100000 tokens for all 2` / `1 of 2 models`）。

顺带修掉一个静默覆盖：刷新模型列表时，端点声明的窗口会写进 per-model override。原先
它连连接级的 `context_window` 都会覆盖 —— 用户明明设了 128k，刷新后被换成端点自称的
值。现在只填「既不继承连接级窗口、也没有自己窗口」的真空隙。

### 新增：从 `/models` 读取模型声明的窗口

`/models` 响应里通常带容量，但解析器原先只取 `id` 和模态，把 `context_length` 直接
丢掉。现在会读：`context_length` / `max_context_length` / `max_model_len` /
`max_input_tokens`，以及输出上限 `max_completion_tokens` / `max_output_tokens`，顶层
优先，再查 `top_provider` / `capabilities` / `limits` 嵌套块。

`0` 和负数是「不适用」的写法，**跳过而不钳制** —— 钳成 0 会关掉压缩，钳成任意值都是
编造上限。重复行取更小的窗口（overstate 会被 provider 拒绝，understate 只是提前压缩）。

实测 OpenRouter 的 `/models`：456 个模型全部读出 `context_length`。

### 新增：窗口没声明时不再等于「关掉压缩」

`context_window = 0` 原本会直接关闭自动压缩，会话一路涨到 provider 拒绝为止。手加的
中转服务最容易踩这个 —— 它没有 preset，窗口解析不到任何值。

现在按优先级解析：`model_overrides` → 连接级 `context_window` → 内置目录里该模型的公开
窗口 → 保守兜底 128k。`/context` 会说明这个数字是谁定的：

```
window            128,000  (usable 119,808 after output reserve)
window from       the model's own published ceiling
```

兜底时会明确写 `nothing established it; conservative fallback`，并提示该设哪个字段 ——
不会把 128k 伪装成「模型的真实上限」。

### 修复：`/model` 切换后新模型不生效

**症状**：任何情况切换模型都提示 `runtime activation: background ownership cannot cross
sessions`，且必须退出重进（resume）新模型才生效，否则会话仍跑旧模型。

**根因**：v3（canonical）会话的 `SessionPath()` 返回空串，
`ContinueSessionPath("", ...)` 于是拼出一个带时间戳的全新路径。`AdoptHistory` 看到路径
不存在，走进「fresh history」分支调 `BindFreshSession` —— 生成了第二个会话身份。新旧
controller 共享同一个 background scope，激活校验看到「同一份后台归属被两个不同会话声明」
就拒绝了。

**修法**：重建的 controller attach 到旧会话已有的身份，模型切换就只是模型切换。

这里有个必须注意的顺序：canonical 会话的 `AdoptHistory` 走 session event，而这个提交被
执行所有权拦着 —— 新 controller 要到激活之后才有权写。**构建期写会被静默丢弃，不报错**。
所以携带的对话改为 `pendingCarry`，等激活完成再补写。旧格式（无 canonical 身份）的会话
行为完全不变。

### 修复：无窗口的 provider 不再原样重发超限请求

provider 只回一句「prompt is too long」（智谱 GLM 等）时，原先因为没有可用的窗口数字，
重试预算算不出来，只能原样重发。现在用实测值界定：被拒的 prompt 大小就是窗口上界，据
此算重试预算。

测量值有一个地板（8k）：小到装不下一次摘要折叠的窗口比诚实的「未知」更糟，所以低于地板
的测量仍然记录，但不用于定窗口。

### 已知：API key 输入框显示为圆点是正常的

添加 API key 时输入框只显示 `•` 且不画自己的光标。这是**密码框设计**，非缺陷
（上游行为一致，也没有"显示/隐藏"开关）。输入一直正常接收：空格、中文、IME 一次
提交多字符、backspace 按 rune 删除均正确。

## 重要：Termux shell 沙箱限制

Reasonix CLI/TUI 默认是 `workspace-write`。该模式要求 OS 级 shell sandbox；但
Android/Termux 当前没有可用的 bubblewrap backend，因此 Bash 会失败关闭：

```text
shell sandbox requested but unavailable on this host; refusing to run unconfined.
```

这不是 API key 或目录权限错误，`[sandbox] bash = "off"`、`--add-dir` 也不能提供
缺失的 sandbox。可信任务可以显式运行：

```sh
"$PREFIX/bin/reasonix" --permission-mode danger-full-access
```

> [!WARNING]
> `danger-full-access` 会关闭 Reasonix 的 OS 沙箱。它不会自行授予 Android root，
> 但命令可以访问当前 Termux 用户（以及可用 `su`）本来能访问的一切。仅用于自己
> 信任的仓库、提示词和任务。

一键安装 wrapper（无需 clone 仓库，安全默认：新增 `reasonix-full` / `reasonix-raw`，
不覆盖普通 `reasonix`）：

```sh
curl -fsSL "https://raw.githubusercontent.com/$REPO/master/scripts/termux/install-wrapper.sh" | bash
```

若明确希望 `reasonix` 本身默认 Full access：

```sh
curl -fsSL "https://raw.githubusercontent.com/$REPO/master/scripts/termux/install-wrapper.sh" | bash -s -- --default-full
```

安装器会检查下载脚本的标记与 shell 语法、备份非本安装器管理的同名文件，并支持
`--uninstall`。
完整说明、风险边界、临时退出 full access 与排错方法见
[`docs/TERMUX.zh-CN.md`](https://github.com/$REPO/blob/master/docs/TERMUX.zh-CN.md)。

## 已知限制

- 上游 2.x 暂不适用：重构了 `internal/` 目录结构且不含任何 Android 支持，
  本补丁无法套用。
- 本 release 由仓库的 `verify-and-release.yml` 自动发布。该 workflow 已适配
  Termux 版本号：比较时剥离 `-termux.<sha>` 后缀，且只认上游 1.x 线。

## 源码

https://github.com/wumingmr/reasonix_android_custon

上游 MIT 许可，本改动同样以 MIT 发布。
