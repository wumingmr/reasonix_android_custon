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

## 下载与安装

```sh
BASE=https://github.com/wumingmr/reasonix_android_custon/releases/download/$REL_TAG

curl -L -o $ASSET  "$BASE/$ASSET"
curl -L -o SHA256SUMS "$BASE/SHA256SUMS"   # 校验要用它，必须一起下

sha256sum -c SHA256SUMS   # 期望输出: $ASSET: OK
chmod +x $ASSET
mv $ASSET $PREFIX/bin/reasonix
reasonix --version
```

**安装后文件名必须是 `reasonix`** —— 二进制本身叫什么都无所谓（实测以任意名字都能
运行），但 `$PREFIX/bin` 在 `PATH` 里，`reasonix` 这个命令要能调得到，文件就得叫
`reasonix`，所以上面 `mv` 时顺手改了名。

SHA256：`$HASH`

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

### 更新脚本不再选错补丁源

`update-reasonix.sh` 原本用"最后一个 `termux:` 开头的 commit"定位补丁源，
`termux: 修正版本号推导`这个只改 Makefile 和脚本的提交因此顶替了真正的适配
commit，升级后 Android 通知、中文输入修复、`resume_list_limit` 会全部消失。现改
为校验 commit 是否真的改到适配文件。实测该静默降级**编译验证挡不住** ——
`GOOS=android` 满足 `linux` build tag，`sender_linux.go` 会接手编译。

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

## 已知限制

- 上游 2.x 暂不适用：重构了 `internal/` 目录结构且不含任何 Android 支持，
  本补丁无法套用。详见仓库 `TERMUX.md`。
- 本 release 由仓库的 `verify-and-release.yml` 自动发布。该 workflow 已适配
  Termux 版本号：比较时剥离 `-termux.<sha>` 后缀，且只认上游 1.x 线。

## 源码

https://github.com/wumingmr/reasonix_android_custon

改动说明见 `TERMUX.md`：https://github.com/wumingmr/reasonix_android_custon/blob/master/TERMUX.md

上游 MIT 许可，本改动同样以 MIT 发布。
