# Reasonix for Termux

这是 [Reasonix](https://github.com/esengine/DeepSeek-Reasonix) 的 **Termux (Android)
定制版**：在上游源码上追加 Android 平台适配，并修复了几个在 Termux 上无法正常使用的
交互缺陷。上游功能与本仓库一致，基线为 **v1.39.8**。

当前安装版本：`1.39.8-termux.92e050d7`

---

## 下载

编译好的 Android arm64 二进制在 `artifacts/reasonix-android-arm64`，同时提供
预编译的 [release 附件](https://github.com/wumingmr/reasonix_android_custon/releases/tag/termux-v1.39.8)。

**推荐（release 附件，匿名可下载，不需要登录）**：

```sh
curl -L -o reasonix-android-arm64 \
  https://github.com/wumingmr/reasonix_android_custon/releases/download/termux-v1.39.8/reasonix-android-arm64-v1.39.8
```

安装：

```sh
sha256sum reasonix-android-arm64
# a5ef9d45b36e7a3c1e06234a258f82ed92dca8a7c88a305b4fe7567a7d6b6c80
chmod +x reasonix-android-arm64
mv reasonix-android-arm64 $PREFIX/bin/reasonix
reasonix --version
```

> ⚠️ **文件名必须是 `reasonix`**。构建产物名带 `-v1.39.8` 后缀，但放进 `$PREFIX/bin`
> 后必须叫 `reasonix`，否则 `reasonix` 命令调不到。

其他方式也能拿到同一个文件，但有坑：

| 方式 | 结果 |
|---|---|
| release 附件（`releases/download/...`） | ✅ 真实二进制（**推荐**） |
| `media.githubusercontent.com/media/...` | ✅ 真实二进制 |
| `github.com/<repo>/raw/master/artifacts/...` | ✅ 真实二进制（LFS 自动重定向） |
| `raw.githubusercontent.com/...` | ❌ 只返回 133 字节的 **LFS 指针文本**，不是可执行文件 |

`raw.githubusercontent.com` 直接返回指针是因为该端点不做 LFS 重定向。这是
GitHub 的已知行为，不是本仓库的问题。

用 git 取源码的人需要自己拉 LFS 对象，否则 `artifacts/` 里只是指针：

```sh
git clone https://github.com/wumingmr/reasonix_android_custon.git
cd reasonix_android_custon
git lfs install && git lfs pull
```

> release 由手动创建。仓库的 `.github/workflows/verify-and-release.yml` 要求
> 二进制版本号与上游 tag 完全相等才发布，而本仓库版本号带 `-termux.<sha>`
> 后缀，该 Action 会跳过。

---

## 相对上游改了什么

### 平台适配（`internal/`）

- `internal/notify/sender_android.go`：Android 通知走 `termux-notification`
- `internal/mcplaunch/`、`internal/repair/`：`_unix.go` → `_other.go` 适配
- `internal/fileutil/atomicwrite.go`：Android 文件系统的原子写
- `internal/tool/builtin/bash.go`：Termux 的 shell 路径与进程组处理
- `internal/agent/migrate.go`、`internal/cli/theme_osc_*.go`：终端能力差异

### 输入修复：中文与空格打不进去

上游多个输入框用 `msg.String()` 兜底处理按键，而 bubbletea 的
`KeyPressMsg.String()` 对空格返回 `"space"`、对 IME 组合态键返回按键名而非文本。
结果是**中文打不进去、空格被吞**，多词搜索失效。

涉及：`select.go`（`reasonix --resume` 菜单，raw 字节解析路径）、`quick_picker.go`、
`skill_picker.go`、`connection_setup.go`。修复方式与官方 textarea 一致：只用
`msg.Text`，空则忽略。

`select.go` 的问题更底层 —— 它用 `term.MakeRaw` 读 stdin **原始字节**，
`buf := make([]byte, 8)` 会腰斩 UTF-8 多字节序列，且 `k[0] >= 32 && k[0] < 127`
按字节判断丢弃全部非 ASCII。已改为按 rune 处理。

> 注意 TUI 内的 `/resume` 和命令行的 `reasonix --resume` 是**两套独立实现**，
> 提示语不同（`Type to filter` vs `/ 搜索`），修复时别只改一处。

### 新增 `[ui] resume_list_limit`

`/resume` 选择器原本只显示 10 条，且搜索只在这 10 条里过滤 —— 更早的会话既看不到
也搜不到。已挖出并可配置：

```toml
[ui]
resume_list_limit = -1     # 负数 = 不限制；0/缺省 = 内置默认 10
```

> 该字段必须同时出现在 `internal/config/render.go` 的两个渲染函数里。
> `RenderTOMLForScope` 是**全量重写** `[ui]` 段，不认识的键会被直接删除 ——
> 漏掉的表现是"设置自己消失了"。

### 已知：API key 输入框显示为圆点是正常的

添加 API key 时输入框只显示 `•`，且不画自己的光标。这是**密码框设计**，不是缺陷
（上游行为一致，也没有"显示/隐藏"开关）。输入一直是正常接收的：空格、中文、
IME 一次提交多字符、backspace 按 rune 删除均正确。

---

## 从源码构建

```sh
git clone https://github.com/wumingmr/reasonix_android_custon.git
cd reasonix_android_custon
git lfs install

make android
install -m 755 bin/reasonix-android-arm64 $PREFIX/bin/reasonix
```

版本号由 Makefile 自动推导为 `<upstream>-termux.<短sha>`，**不要传 `VERSION=`**
（传了会覆盖推导逻辑）。

---

## 更新到上游新版

```sh
./update-reasonix.sh
```

默认只同步最新的 **1.x** tag，并重新应用 Termux 适配补丁。2.x 重构了 `internal/`
目录结构且不含任何 Android 支持，补丁无法套用，需要：

```sh
REASONIX_TAG=v2.30.0 ALLOW_MAJOR=1 ./update-reasonix.sh   # 目前会失败
```

关于 2.x 的评估结论见 [HANDOVER.md](https://github.com/wumingmr/reasonix_android_custon)
的本地副本；简述：等官方 2.x GA（其 `docs/ROADMAP.md` 的 GA 门槛目前多数未达成）
后再评估，届时移植成本更低。

---

## 仓库结构

| 路径 | 说明 |
|---|---|
| `artifacts/reasonix-android-arm64` | 编译好的二进制（Git LFS） |
| `reasonix-termux.patch` | Termux 适配补丁，与 master 上的 `termux:` commit 内容一致 |
| `update-reasonix.sh` | 升级到上游新版并重放补丁 |
| `release-termux.sh` | 编译 + 测试 + 上传二进制到 `artifacts/` |

`update-reasonix.sh` 的 `resolve_patch_ref()` 只取 master 上**最新一个**
`termux:` commit 做 cherry-pick，所以**平台适配与本地定制必须合并在同一个
`termux:` commit 里**，拆开会让平台适配在升级时丢失。

---

## 许可

上游 MIT，本仓库的改动同样以 MIT 发布。