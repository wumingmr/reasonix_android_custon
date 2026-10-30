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

## 相对上游改了什么

### 平台适配

- `internal/notify/sender_android.go` —— Android 通知走 `termux-notification`
- `internal/mcplaunch/`、`internal/repair/` —— `_unix.go` → `_other.go`
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
`/copy` 与 `/export` 仍读取完整历史，不受截断影响。真实 49 MB / 5486 条消息会话：
全量读取约 9.2 秒，索引尾读约 2.7 秒。

### 新增 `[ui] resume_list_limit`

`/resume` 选择器原本只显示 10 条，且搜索只在这 10 条里过滤 —— 更早的会话既看不到
也搜不到。已挖出隐藏的 100 条上限并改为可配置：

```toml
[ui]
resume_list_limit = -1     # 负数 = 不限制；0/缺省 = 内置默认 10
```

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

仓库附带修正版 wrapper 模板：

```sh
mkdir -p ~/bin
curl -fL "https://raw.githubusercontent.com/$REPO/master/scripts/termux/reasonix-wrapper" \
  -o ~/bin/reasonix
curl -fL "https://raw.githubusercontent.com/$REPO/master/scripts/termux/reasonix-raw" \
  -o ~/bin/reasonix-raw
chmod 755 ~/bin/reasonix ~/bin/reasonix-raw
export PATH="$HOME/bin:$PATH"
hash -r

reasonix --version       # wrapper，普通会话默认 danger-full-access
reasonix-raw --version   # 直连 $PREFIX/bin/reasonix，不注入权限参数
```

如不想让 `reasonix` 默认 full access，可把 wrapper 安装成 `~/bin/reasonix-full`。
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
