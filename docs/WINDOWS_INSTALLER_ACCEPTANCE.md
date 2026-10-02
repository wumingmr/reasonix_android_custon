# Windows package behavior, identity and acceptance / Windows 包行为、身份与验收

## Product behavior / 产品行为

Test builds are versions of the same Reasonix product. Installing a test build reuses the existing installation directory and application identity, updates the active version, and keeps shortcuts usable. Accounts, configuration, conversations and workspaces continue to use the same user data.

测试包是同一个 Reasonix 的测试版本。安装测试版复用已有安装目录和应用身份，切换当前版本并保持快捷方式有效；账号、配置、聊天记录和工作区继续共用原有用户数据。

Uninstalling removes the currently installed program, version payloads, shortcuts and uninstall registration. It preserves the independent Reasonix user data directory; it does not automatically restore a previous Stable version. Reinstalling Stable uses the preserved data. A test build that changes data formats must establish compatibility with the supported Stable reader or provide a reviewed recovery/migration path before distribution. This installer identity fix changes no user data formats.

卸载移除当前程序、程序版本目录、快捷方式及卸载登记，保留独立的 Reasonix 用户数据目录，不自动恢复先前正式版。重新安装正式版后继续使用保留的数据。测试版若修改数据格式，分发前必须证明受支持的正式版能够读取，或提供经过审查的恢复/迁移方案。本次安装身份修复不修改用户数据格式。

## Portable test builds / 便携版测试包

Portable test builds keep program files separate and share user data by default. Extract the complete ZIP into a new, independent directory on a local Windows drive and run `Reasonix.exe`. Do not extract into the installed Reasonix directory or overlay an old portable directory: files left by another version can produce a mixed, invalid package. Network shares, VM shared folders and mapped network drives remain unsupported for portable execution.

便携版测试包采用“程序独立、数据默认共用”。将完整 ZIP 解压到 Windows 本地磁盘上的全新独立目录，再运行 `Reasonix.exe`。不要解压到正式版安装目录，也不要直接覆盖旧便携目录，避免不同版本文件混杂。便携版仍不支持从网络共享、虚拟机共享目录或映射网络盘运行。

Extracting and launching the portable build must not overwrite the installed application or change its shortcuts, uninstall registration or default installation location. Portable means no installation is required; it does not mean user data lives beside the executable. By default, it uses the same Windows user's Reasonix data home (`%APPDATA%\reasonix`), preserving access to accounts, configuration, conversations and workspaces. An explicit `REASONIX_HOME` override still selects a separate data home; extraction alone does not create one.

解压和运行便携版不得覆盖已安装的程序，不修改正式版快捷方式、卸载登记或默认安装位置。“便携”指程序免安装，不代表数据存放在程序目录。默认沿用同一 Windows 用户的 Reasonix 数据目录（`%APPDATA%\reasonix`），继续使用账号、配置、聊天记录和工作区。显式设置 `REASONIX_HOME` 仍可选择独立数据目录；仅解压便携包不会自动隔离数据。

When switching between the installed and portable versions with shared data, exit the current application normally and wait for its shell and service to stop before starting the other version. Another installation owning the same data home blocks launcher startup; the shell also acquires a single-instance lock using the canonical data directory before starting its service. This does not automatically terminate the installed application or switch its active installation pointer. The Stable-reader compatibility requirement above applies equally to portable test builds.

共用数据时，切换正式版与便携测试版前应正常退出当前程序，待 Shell 和服务停止后再启动另一版本。另一安装目录的实例占用同一数据目录时，启动器会阻止启动；Shell 也会在启动服务前按解析后的数据目录获取单实例锁。这不会自动结束正式版进程，也不会切换正式版安装目录中的当前版本指针。上文对正式版读取测试版数据的兼容要求，同样适用于便携测试版。

To remove a portable build, exit it and delete only its extracted program directory. Keep the shared user data; the installed application remains available. Automated portable acceptance must use disposable directories and isolated data homes, even though normal product use shares data by default.

删除便携版时，退出程序后仅删除其解压目录即可，保留共享用户数据，不影响正式版安装。自动验收便携包仍须使用一次性目录和隔离数据目录，即使产品日常使用默认共用数据。

For release acceptance in a disposable Windows account, record the installed application's directory, current version pointer, shortcut targets and uninstall registration before and after portable startup and removal; they must remain unchanged. Exercise switching in both directions with disposable shared data, confirm that concurrent startup cannot create a second writer, and verify that retained data remains readable after the portable directory is deleted. These are real-package acceptance requirements, not claims established by mocked installer tests.

发布验收应在一次性 Windows 账户中，记录便携版启动及删除前后的正式版目录、当前版本指针、快捷方式目标和卸载登记，确认均未变化。使用一次性共享数据覆盖双向版本切换，确认并发启动不会产生第二个写入实例，并验证删除便携目录后仍能读取保留数据。这些是实际安装包的验收要求，不代表模拟安装测试已经验证了这些场景。

## Version contract / 版本约定

| Purpose / 用途 | Example / 示例 |
| --- | --- |
| Installation and runtime identity / 安装与运行身份 | `v1.38.9-2` |
| Human-readable version / 用户可见版本 | `1.38.9-2` |
| Numeric Windows version resource / Windows 数值版本资源 | `1.38.9.0` |

Stable, Preview, RC and numeric test suffixes follow the same contract. Never derive installation paths or activator arguments from the numeric resource version.

正式版、Preview、RC 和数字内测后缀均遵守同一约定。安装路径与激活器参数不得从 Windows 数值版本资源推导。

## Automated acceptance / 自动验收

`scripts/test-windows-installer-startup.ps1` is destructive to the test account's application registration and shortcuts. Run it only on a disposable Windows user or VM snapshot. GitHub-hosted runners qualify automatically; local and self-hosted runs require `-DisposableEnvironment`. Even with this flag, the script refuses existing current/legacy uninstall entries in either registry view, Reasonix shortcuts, the default program or user-data directory, legacy WebView data, or running Reasonix processes. These checks belong only to the acceptance script: they do not restrict a user's intentional upgrade or replacement install.

`scripts/test-windows-installer-startup.ps1` 会修改测试账户的应用登记和快捷方式，只能在一次性 Windows 用户或虚拟机快照中运行。GitHub 托管 runner 自动满足此要求；本地及自托管环境必须指定 `-DisposableEnvironment`。即使指定该参数，若存在当前版/历史版卸载登记（任一注册表视图）、Reasonix 快捷方式、默认程序或用户数据目录、历史 WebView 数据或运行进程，脚本也会拒绝执行。这些检查仅约束自动验收，不限制用户主动升级或覆盖安装。

```powershell
./scripts/test-windows-installer-startup.ps1 `
  -InstallerPath C:\test-artifacts\Reasonix-windows-amd64-installer.exe `
  -ExpectedVersion v1.38.9-2 `
  -EvidenceDirectory C:\test-evidence\reasonix-install-1 `
  -DisposableEnvironment
```

The evidence directory must not exist. Installation diagnostics and both runtime phases use isolated data homes. The script validates a fresh install's identity, registration, startup, instance reuse and normal exit **before** constructing the historical truncated-version fixture. Suffix builds then reinstall and repeat runtime acceptance, writing separate `fresh-install` and `repaired-install` reports. A failure retains the fixture and logs without automatically uninstalling a potentially running app. Successful acceptance uninstalls the fixture and writes `acceptance.json` bound to the unchanged installer SHA256; Stable records truncated-version repair as not applicable.

证据目录必须尚不存在。安装诊断及两个启动阶段均使用隔离数据目录。脚本先验证全新安装的版本身份、卸载登记、启动、实例复用和正常退出，**之后**才构造历史版本截断状态。带后缀版本覆盖修复后再次验收，分别生成 `fresh-install` 和 `repaired-install` 报告。失败时保留现场和日志，不自动卸载可能仍在运行的应用；全部通过后卸载测试程序，并生成绑定未变化安装包 SHA256 的 `acceptance.json`。正式版本将版本截断修复标为不适用。

Both installer acceptance and portable startup acceptance use `windows-acceptance-environment.ps1`. It clears inherited `REASONIX_*`, `NODE_OPTIONS` and `ELECTRON_RUN_AS_NODE` overrides, explicitly sets isolated home/state/cache directories and noninteractive mode, and restores the caller's environment on success or failure. Nested runtime checks restore the surrounding installer environment before uninstall.

安装验收和便携启动验收共用 `windows-acceptance-environment.ps1`：清理继承的 `REASONIX_*`、`NODE_OPTIONS` 和 `ELECTRON_RUN_AS_NODE`，显式设置隔离的主数据、状态、缓存目录及非交互模式，无论成功还是失败均恢复调用方环境。嵌套启动验收结束后先恢复外层安装验收环境，再执行卸载。

An uninstaller exit code of zero is necessary but insufficient. Acceptance also requires all program entries and version payloads, desktop/Start menu shortcuts and uninstall registrations to be absent, with no remaining Reasonix processes. The script hashes files in its isolated data homes before uninstall and verifies those bytes afterward. It also places a retention sentinel in the initially absent default `%APPDATA%\reasonix` directory, so an uninstaller that directly deletes default user data cannot pass. `acceptance.json` records `uninstall` and `dataPreserved` as passed only after these checks. Lock files or user-owned files may keep the install root nonempty.

卸载退出码为 0 只是必要条件。验收还要求程序入口及版本文件、桌面/开始菜单快捷方式、卸载登记均已移除，且无 Reasonix 进程残留。脚本在卸载前记录隔离数据目录内文件的哈希，卸载后逐一核对；同时在原本不存在的默认 `%APPDATA%\reasonix` 目录中放置保留哨兵，拦截卸载器直接误删默认用户数据的情况。仅在这些检查通过后，`acceptance.json` 才记录 `uninstall` 和 `dataPreserved` 通过。安装根目录允许因锁文件或用户自有文件而保留。

Successful acceptance retains the data fixtures, including the default-directory sentinel, as evidence. Reset the disposable account or restore its clean VM snapshot before another real acceptance run; do not bypass preflight to reuse a dirty account. The mock regression suite redirects default data into its own temporary directory and never installs, uninstalls or writes real account data.

验收成功后保留数据夹具（包括默认目录哨兵）作为证据。下一次真实验收前应重置一次性账户或恢复干净虚拟机快照，不应绕过预检复用残留账户。模拟回归测试将默认数据目录也重定向到自己的临时目录，不执行真实安装、卸载或写入真实账户数据。

The release workflow runs acceptance after the final manual-package rebuild and before minisign/upload. Authenticode releases additionally install and start the final signed installers on native x64 and ARM64 runners before publication. The lightweight PowerShell harness tests run on Windows PR CI with mocked OS boundaries; they are not a substitute for package runtime acceptance.

发布工作流在手动分发包最后一次重建之后、minisign 签名及上传之前执行验收。Authenticode 发布还必须在原生 x64 和 ARM64 runner 上安装并启动最终签名安装器，才可发布。轻量 PowerShell 回归测试在 Windows PR CI 上模拟系统操作执行，不能替代真实安装包启动验收。
