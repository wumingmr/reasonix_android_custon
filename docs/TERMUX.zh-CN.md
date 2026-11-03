# Reasonix Termux / Android 使用说明

本文对应 [`reasonix_android_custon`](https://github.com/wumingmr/reasonix_android_custon)
发布的 Android arm64 二进制，说明安装、当前沙箱限制，以及可选的权限 wrapper。

## 安装二进制

从 GitHub Release 下载当前版本。以下以 `v1.39.11-r1` 为例；升级时只需替换
`VERSION`：

```sh
mkdir -p ~/tmp
VERSION=v1.39.11-r1
REPO=wumingmr/reasonix_android_custon
ASSET="reasonix-android-arm64-$VERSION"

curl -fL \
  "https://github.com/$REPO/releases/download/termux-$VERSION/$ASSET" \
  -o "$HOME/tmp/$ASSET"
curl -fL \
  "https://github.com/$REPO/releases/download/termux-$VERSION/SHA256SUMS" \
  -o ~/tmp/SHA256SUMS

(cd ~/tmp && sha256sum -c SHA256SUMS) || exit 1
NEW="$(mktemp "$PREFIX/bin/.reasonix.new.XXXXXX")"
install -m 755 "$HOME/tmp/$ASSET" "$NEW" && \
  cmp -s "$HOME/tmp/$ASSET" "$NEW" && \
  mv -f "$NEW" "$PREFIX/bin/reasonix" || exit 1
"$PREFIX/bin/reasonix" --version
```

直接覆盖正在运行的二进制偶尔会遇到 `ETXTBSY`。更稳妥的是先复制到同目录临时
文件，再原子替换：

```sh
cp "$HOME/tmp/$ASSET" "$PREFIX/bin/.reasonix.new"
chmod 755 "$PREFIX/bin/.reasonix.new"
mv -f "$PREFIX/bin/.reasonix.new" "$PREFIX/bin/reasonix"
```

已经运行的进程继续使用旧 inode；退出后新进程自动使用新版。

## 当前沙箱限制

Reasonix CLI/TUI 默认权限模式是 `workspace-write`：

```text
--permission-mode workspace-write
```

`read-only` 和 `workspace-write` 都要求宿主提供 OS 级 shell sandbox。在 Linux 上
Reasonix 使用 bubblewrap；但 Android/Termux 当前没有可用的 bubblewrap backend，
Termux 官方仓库也没有可直接使用的 `bwrap` 包。因此 Bash/shell 工具会**失败关闭**，
常见错误是：

```text
shell sandbox requested but unavailable on this host; refusing to run unconfined.
```

这不是 provider、API key 或工作目录权限问题。以下设置也不能解决：

```toml
[sandbox]
bash = "off"
```

它控制 sandbox backend 的启用方式，不能把一个要求 sandbox 的权限 preset 变成
无沙箱运行。`--add-dir` / `additional_write_dirs` 也只扩展允许路径，不会提供 Android
上缺失的 sandbox backend。

目前可行办法是对需要 shell 的可信会话显式使用：

```sh
"$PREFIX/bin/reasonix" --permission-mode danger-full-access
```

或对单次任务：

```sh
"$PREFIX/bin/reasonix" --permission-mode danger-full-access -p '检查当前项目并运行测试'
```

> [!WARNING]
> `danger-full-access` 会关闭该会话的 Reasonix OS 沙箱。它**不会自行授予 Android
> root**，命令仍以当前 Termux 用户身份运行；但命令可以访问该用户本来就能访问的
> 所有内容。如果 Termux 环境里还可调用 `su`，风险范围会进一步扩大。只应对自己
> 信任的仓库、提示词和任务启用，不要把它当作普通桌面 Linux 的安全等价物。

## 可选 wrapper（一键安装）

不需要 clone 仓库，也不需要知道 `scripts/termux/...` 在哪里。安装器会从 GitHub
下载两个 wrapper 模板，并检查文件标记与 shell 语法：

- `reasonix-full`：普通会话默认注入 `--permission-mode danger-full-access`；显式
  传入 permission mode 时尊重用户选择。
- `reasonix-raw`：直连真实二进制，不注入任何权限参数。

### 方案 A：保留显式命令（推荐）

默认安装不会覆盖 `reasonix`，而是在 Termux 自带 PATH 的 `$PREFIX/bin` 中新增
`reasonix-full` 和 `reasonix-raw`：

```sh
curl -fsSL https://raw.githubusercontent.com/wumingmr/reasonix_android_custon/master/scripts/termux/install-wrapper.sh | bash

reasonix-full             # 默认 danger-full-access
reasonix-raw --version    # 原始二进制
```

这最不容易忘记当前会话没有 sandbox。

### 方案 B：让 `reasonix` 默认可用 shell

如果这是个人设备且你明确接受上述风险，可以一键安装 `~/bin/reasonix`。安装器会
自动处理 PATH；若同名文件不是本安装器管理的 wrapper，会先备份而不是覆盖丢失：

```sh
curl -fsSL https://raw.githubusercontent.com/wumingmr/reasonix_android_custon/master/scripts/termux/install-wrapper.sh | bash -s -- --default-full
```

重新打开终端后，正常结果应该先列出 wrapper，再列出真实二进制：

```sh
type -a reasonix
```

```text
reasonix is /data/data/com.termux/files/home/bin/reasonix
reasonix is /data/data/com.termux/files/usr/bin/reasonix
```

wrapper 会直接放行 `--version`、`-v`、`--help`、`-h`、`--auto/-y` 和配置/升级等
非普通会话参数，避免向不接受或会冲突的命令注入 `--permission-mode`。

卸载只删除安装器自己管理的 wrapper，不会删除 `$PREFIX/bin/reasonix` 真二进制：

```sh
curl -fsSL https://raw.githubusercontent.com/wumingmr/reasonix_android_custon/master/scripts/termux/install-wrapper.sh | bash -s -- --uninstall
```

## 临时退出 full access

即使安装了 wrapper，以下方式仍可回到原始行为：

```sh
reasonix-raw ...
REASONIX_FULL_ACCESS=0 reasonix ...
"$PREFIX/bin/reasonix" ...
```

也可以显式选择模式；wrapper 不会覆盖明确给出的参数：

```sh
reasonix --permission-mode read-only
reasonix --permission-mode workspace-write
reasonix --permission-mode danger-full-access
reasonix --permission-mode plan
```

注意：在当前 Termux 环境中，显式选 `read-only` / `workspace-write` 仍会遇到上述
shell sandbox unavailable；这正是原始、安全失败行为。

## 自检与排错

```sh
# 原始二进制是否存在
ls -l "$PREFIX/bin/reasonix"

# wrapper 解析到哪里
type -a reasonix reasonix-raw

# 两条 version 路径都应成功
reasonix --version
reasonix-raw --version

# 查看 wrapper 实际使用的真二进制
REASONIX_REAL="$PREFIX/bin/reasonix" reasonix-raw --version
```

如果 `reasonix --version` 报 `unknown flag: --version`，说明使用的是早期 wrapper；
重新运行上面带 `--default-full` 的一键安装命令即可安全更新。早期版本在 `--version`
之前错误注入了 `--permission-mode`。

## setup 识图能力提示

`reasonix setup` 中新增连接的“从 URL 获取模型”和已有连接的“测试连接并刷新模型”
会读取同一次 `/models` 响应中的图片能力字段。模型列表显示 `img: 支持 / 不支持 / 未知`，
并标明接口声明、内置目录/协议或手动设置；不根据模型名字推测，也不发送图片推理探针。

支持 `input_modalities`、`architecture.input_modalities`（OpenRouter）、
`modalities.input`、`capabilities.input_modalities` / `capabilities.vision`、
`supports_vision` 和 `vision`。缺字段或冲突保持未知，刷新失败不改变已有结果。

能力声明先留在 setup 草稿，选择“保存并退出”后才写入 Reasonix cache 下的
`model-capabilities-v2.json`，24 小时有效；端点、模型或凭据变化后旧缓存不复用。
不会把自动声明写成手动 `vision` 覆盖，也不覆盖已有的模型级设置。要手动声明可用：

```toml
# 放在对应 [[providers]] 中，保留 model_overrides 的其他已有项
model_overrides = { "codebuddy/deepseek-v4.1-flash" = { vision = true } }
```

没有识图声明且没有配置 `[agent] vision_model` 的自定义模型，会尝试发送你明确附加的图片，
仍显示“未知”而不伪称已验证；若配置了图片理解模型则走该 fallback。显式纯文本声明仍阻止
直接发图，官方 DeepSeek 纯文本型号的限制仍保留。普通纯文本请求不变化。

你的 `127.0.0.1:8790` 网关目前没有返回识图元数据，因此仅刷新不能显示“支持”。
这里仅支持 img，不启用音频、视频或原生 PDF 传输。附图可在 CLI 粘贴图片路径形成附件，
或使用工作目录内的 `@.reasonix/attachments/example.jpg`；工作目录外的裸路径不等于附件。

## setup 思考强度与每模型设置

新建连接和刷新模型沿用同一次 `GET /models` 读取思考声明，不发送推理请求。
支持 `reasoning` / `supports_reasoning` 布尔、`supported_parameters` 的思考参数提示，
以及明确的 `reasoning_protocol`、`supported_efforts` / `reasoning_efforts`、`default_effort`；
也可放在 `capabilities` 或 `reasoning = { supported, protocol, efforts, default }` 中。
只有布尔或参数名时显示“档位未知”，不会猜测 low/high/max，也不按模型后缀选择协议。

选择“保存并退出”后将发现记录写入对应 `model_overrides` 的 `discovered_reasoning`，
与手动协议/档位分开，24 小时有效并绑定端点、模型和凭据。取消/保存冲突不写入，
刷新成功但新响应缺字段会删除旧发现；网络失败保留已有数据。协议未知、不兼容、
档位重复或默认值非法的声明不会启用。手动声明和 adapter 固定限制始终优先。

连接操作新增“编辑各模型思考强度”，可设置具体档位、`auto` 或“继承”。
CLI `/effort` 现在只保存当前模型，不影响同分组其他模型：

```toml
# 放在对应 provider 的 model_overrides 中，与已有窗口/vision 合并
model_overrides = { "codebuddy/deepseek-v4.1-flash" = { effort = "high" } }
```

`effort = ""` 表示当前模型 auto（忽略分组固定 effort，使用该模型默认）；
删除模型级 effort 字段恢复继承分组。会话 `--effort` 优先于模型设置。
旧的分组 `effort` 未自动清除，因此还没单独设置的模型保持旧行为。GLM/Kimi 等
协议只允许自身档位；`auto` 不等于 adaptive。你的 8790 网关未声明思考字段，
自动读取仍只能显示未知或已有配置，不能证明后端确实执行了某个思考强度。

## 源码升级与发布保留规则

`update-reasonix.sh` 默认只同步上游最新稳定 1.x；Studio 2.x 不自动迁移。
升级前要求工作树干净，并在替换前、补丁重放后两次校验关键修复定义与测试。
`bash update-reasonix.sh --verify-local` 只做本地哨兵校验，不联网、不改源码。
网络查询失败不回滚；真正开始替换后失败恢复 tracked source，不 `git clean` 本地文件。

每次提交定制源码后运行 `bash rebuild-termux-patch.sh` 并提交累积补丁；
`--check` 校验补丁与 HEAD 一致并在干净上游树上验证应用。`.github`、LFS 属性、
update/release/rebuild 脚本、发布说明与 `artifacts` 不在源码 patch 中，升级显式保留。
新增修复须同时登记到 update 哨兵，不能只依赖“补丁里有文件”。

同上游版本的新定制发布使用 `termux-vX.Y.Z-rN`（例如 `termux-v1.39.11-r1`），
内嵌版本仍是 `1.39.11-termux.<源码sha>`，不冒充上游补丁版本。
`TERMUX_RELEASE_TAG=termux-v1.39.11-r1 bash release-termux.sh` 会校验新 tag、版本、
LFS 指针后原子推送 master/tag；旧 tag 不移动。失败重试前先观察远端状态。
发版 workflow 可显式传 `release_tag`，要求它指向上传二进制的提交，并按 tag 生成
资产名和 SHA256SUMS；不是从当前任意 master 隐式创建 tag。

## 升级后的关系

wrapper 通过 `${PREFIX}/bin/reasonix` 找真实二进制。以后只需替换
`$PREFIX/bin/reasonix`，通常不需要修改 wrapper。若真实二进制放在其它位置，可以：

```sh
REASONIX_REAL=/absolute/path/to/reasonix reasonix-full
```
