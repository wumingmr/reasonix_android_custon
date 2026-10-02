# Windows canonical entry and compatibility / Windows 标准入口与兼容

User guidance: [English](WINDOWS_APP_IDENTITY.md) · [中文](WINDOWS_APP_IDENTITY.zh-CN.md).

## Installed layout / 安装布局

`Reasonix.exe` is the canonical GUI entry; `reasonix-cli.exe` remains the CLI
forwarder. Fresh installers and newly extracted ZIPs omit the root
`reasonix-launcher.exe`. A managed upgrade preserves and refreshes that entry
only when it already exists. There is no automatic retirement deadline.

`Reasonix.exe` 是标准 GUI 入口，`reasonix-cli.exe` 仍为 CLI 转发入口。
新安装、全新解压 ZIP 不包含根目录 `reasonix-launcher.exe`；受控升级仅在旧入口
已存在时保留并更新它，不设置自动删除期限。版本目录内的 Electron 程序不属于用户入口。

## Transaction and wire compatibility / 事务与协议兼容

Installer activation and the Windows update helper use
`ActivationRequest.WindowsRootEntries`. This input is mutually exclusive with
explicit root members. After taking the activation lock, the shared owner
inspects the legacy entry and constructs an exact root whitelist. Invalid
legacy paths fail activation. Version files and all selected root entries are
published before `current.json`; failures before that commit restore the prior
files through the existing rollback mechanism. Process coordination retains its
existing lock order. Concurrent old and new writers are covered by a forced
interleaving test, not timing-based sleeps.

安装器和 Windows 更新器统一使用 `ActivationRequest.WindowsRootEntries`，与显式
根成员参数互斥。共享实现持有激活锁后检查旧入口，并生成精确白名单；旧入口路径
非法时中止激活。版本文件和所选根入口均在 `current.json` 提交前发布，提交前失败
通过既有恢复机制恢复旧文件。进程协调锁顺序不变，并以确定性交错测试覆盖新旧写入者竞争。

| Contract / 契约 | Old and new readers / 新旧读取行为 | Change / 变化 |
| --- | --- | --- |
| `current.json` schema 1 | Same strict fields / 相同严格字段 | None / 无 |
| `versioned-v1` | Same version directory resolution / 相同版本目录解析 | None / 无 |
| Windows payload schema 2 | Retains historical launcher member / 保留旧载荷启动器名称 | None / 无 |
| `io.reasonix.desktop` | Same window and shortcut identity / 相同窗口及链接身份 | None / 无 |
| User configuration and sessions / 用户配置与会话 | No migration or new writes / 无迁移或新增写入 | None / 无 |

Signed payloads and NSIS staging continue to use `reasonix-launcher.exe`.
Already installed update helpers cannot be changed by shipping a new helper;
they must still accept and install the next payload. New activators map these
verified bytes to `Reasonix.exe`, and to the old name when preservation applies.
An old helper may continue publishing both names. This does not require a bridge
release or a new pointer field. It does not reduce the signed payload inventory.

签名载荷和 NSIS staging 继续使用 `reasonix-launcher.exe`。安装新版 helper 不会
改变执行本次升级的旧 helper，所以必须保持旧载荷契约。新激活器将已验证字节映射
到标准入口，必要时同时更新旧入口。旧 helper 可能仍发布两个名字；无需过渡版本、
无需添加指针字段，也不减少签名载荷的程序清单。

## Artifact verification / 产物验证

The default layout is `canonical`. Historical verification must explicitly select
`legacy-dual`; neither validator auto-detects its policy from archive contents.
The candidate-owned `desktop/packaging/windows-portable-layout.txt` declares the
build layout to the protected release verifier. Recovery of older candidates
without that declaration explicitly uses `legacy-dual`.

默认验证 `canonical`；历史产物必须显式指定 `legacy-dual`，不得根据包内文件自动
放宽规则。候选源码中的 `desktop/packaging/windows-portable-layout.txt` 声明产物布局，
受保护的发布验证器读取它；恢复没有此声明的历史候选时，显式使用 `legacy-dual`。

```sh
# New ZIP; verify members and entry bytes without extracting.
node desktop/packaging/verify.mjs dist/Reasonix-windows-amd64.zip
# Historical ZIP; require both identical GUI entries.
node desktop/packaging/verify.mjs OLD.zip --kind windows-portable-zip --portable-layout legacy-dual
# Before packaging: also compare canonical entry with the payload source.
scripts/verify-windows-portable.sh STAGING canonical PAYLOAD/reasonix-launcher.exe
```

The Authenticode verifier additionally checks the exact EXE/DLL inventory and
hashes every mapped PE against its signed payload source. Use
`-PortableLayout legacy-dual` only for historical dual-entry packages; new
packages use the default `canonical`. Signature checks are not replaced by
the structural or byte-equality tests.

Authenticode 验证器额外校验精确 EXE/DLL 清单，并逐个比对签名载荷源文件哈希。
仅历史双入口包使用 `-PortableLayout legacy-dual`；新包默认 `canonical`。
结构验证和字节相同比较不能替代签名验收。

## Release acceptance / 发布验收

Run root Go tests and the separate Desktop module tests, Electron type checking
and tests, packaging tests, and the activation/shortcut race tests. Windows
cross-compilation only proves compilation; it never counts as a native test.

运行根 Go 模块、独立 Desktop 模块、Electron 类型检查与测试、打包测试，以及激活与
快捷方式 race 测试。Windows 交叉编译只证明可编译，不计为原生运行测试。

Before promotion, record native x64 and ARM64 evidence for both installer and ZIP:

- Fresh installation, repair, repeat upgrade and existing legacy entry preservation.
- Real unchanged old helper upgrading to the signed candidate, including the
  retained `1.38.9` internal sample and an available supported official release.
- Both entry names, CLI argument/output/exit semantics, shortcuts with custom
  arguments/icons/working directories, taskbar grouping and repeat launch.
- Strict shell/service handshake, renderer `Version`, clean shell/service exit,
  and a separate deliberate mismatch negative case.
- File locks, read-only entries, denied access, invalid pointers and interrupted
  activation. Preserve logs and distinguish rollback errors from success.

晋级发布前，安装器和 ZIP 均需补齐原生 x64、ARM64 证据：新装、修复、连续升级、旧入口
保留；使用真实未修改的旧 helper 升级签名候选（含保留的 `1.38.9` 内测样本和可取得的
受支持正式版本）；两个入口与 CLI 行为、快捷方式自定义属性、任务栏分组和重复启动；
严格握手、渲染端 `Version`、正常退出及独立的故意不匹配反例；文件占用、只读、权限错误、
损坏指针和安装中断。保留日志，不能把恢复失败报告为成功。

Use isolated installations and data homes. Do not replace packaged binaries,
enable development mode, or disable signature/identity checks to obtain a pass.
Missing native or signing evidence blocks release qualification. Publishing and
channel changes remain separate actions; local implementation creates neither.

使用独立安装和数据目录，不替换包内程序、不启用开发模式、不关闭签名或身份校验来
取得通过结果。缺少原生或签名证据时不能判定可正式发布；公开发布和更新渠道切换是独立动作。
