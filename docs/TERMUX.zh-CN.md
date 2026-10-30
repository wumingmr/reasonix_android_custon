# Reasonix Termux / Android 使用说明

本文对应 [`reasonix_android_custon`](https://github.com/wumingmr/reasonix_android_custon)
发布的 Android arm64 二进制，说明安装、当前沙箱限制，以及可选的权限 wrapper。

## 安装二进制

从 GitHub Release 下载当前版本。以下以 `v1.39.10` 为例；升级时只需替换
`VERSION`：

```sh
mkdir -p ~/tmp
VERSION=v1.39.10
REPO=wumingmr/reasonix_android_custon
ASSET="reasonix-android-arm64-$VERSION"

curl -fL \
  "https://github.com/$REPO/releases/download/termux-$VERSION/$ASSET" \
  -o "$HOME/tmp/$ASSET"
curl -fL \
  "https://github.com/$REPO/releases/download/termux-$VERSION/SHA256SUMS" \
  -o ~/tmp/SHA256SUMS

(cd ~/tmp && sha256sum -c SHA256SUMS)
install -m 755 "$HOME/tmp/$ASSET" "$PREFIX/bin/reasonix"
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

## 可选 wrapper

仓库提供两个模板：

- `scripts/termux/reasonix-wrapper`：普通会话默认注入
  `--permission-mode danger-full-access`；显式传入 permission mode 时尊重用户选择。
- `scripts/termux/reasonix-raw`：直连真实二进制，不注入任何权限参数。

### 方案 A：保留显式命令（推荐）

保留 `$PREFIX/bin/reasonix` 作为原始命令，把 wrapper 安装成 `reasonix-full`：

```sh
mkdir -p ~/bin
install -m 755 scripts/termux/reasonix-wrapper ~/bin/reasonix-full
install -m 755 scripts/termux/reasonix-raw ~/bin/reasonix-raw

reasonix-full             # 默认 danger-full-access
reasonix-raw --version    # 原始二进制
```

这最不容易忘记当前会话没有 sandbox。

### 方案 B：让 `reasonix` 默认可用 shell

如果这是个人设备且你明确接受上述风险，可以让 `~/bin/reasonix` 覆盖 PATH 中的真实
二进制名称：

```sh
mkdir -p ~/bin
install -m 755 scripts/termux/reasonix-wrapper ~/bin/reasonix
install -m 755 scripts/termux/reasonix-raw ~/bin/reasonix-raw

# 确保 ~/bin 排在 $PREFIX/bin 前；zsh 用户可写入 ~/.zshrc
export PATH="$HOME/bin:$PATH"
hash -r

type -a reasonix
```

正常结果应该先列出 wrapper，再列出真实二进制：

```text
reasonix is /data/data/com.termux/files/home/bin/reasonix
reasonix is /data/data/com.termux/files/usr/bin/reasonix
```

wrapper 会直接放行 `--version`、`-v`、`--help`、`-h` 和配置/升级等非会话子命令，
避免向不接受该参数的命令注入 `--permission-mode`。

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
请用仓库当前的 `scripts/termux/reasonix-wrapper` 覆盖它。早期版本在 `--version`
之前错误注入了 `--permission-mode`。

## 升级后的关系

wrapper 通过 `${PREFIX}/bin/reasonix` 找真实二进制。以后只需替换
`$PREFIX/bin/reasonix`，通常不需要修改 wrapper。若真实二进制放在其它位置，可以：

```sh
REASONIX_REAL=/absolute/path/to/reasonix reasonix-full
```
