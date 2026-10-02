# Windows 应用身份

Reasonix Desktop 的 Electron 窗口、launcher、Go 桌面进程、快捷方式和通知统一使用
不随版本变化的 AppUserModelID `io.reasonix.desktop`，通知显示名称仍为 `Reasonix`。
它与 Reasonix Studio 的 `io.reasonix.studio`、旧 Tauri 桌面端的
`dev.reasonix.desktop` 分离。Wails Studio v2.10.0 和旧 Reasonix Desktop 曾共用
`Reasonix`，新版 Desktop 不再使用这个共享身份。

## 安装与升级

使用安装根目录的 `Reasonix.exe` 打开桌面端；`reasonix-cli.exe` 是独立的命令行入口。
新安装和解压到空目录的 ZIP 只提供这个 GUI 入口。已有安装原本存在的
`reasonix-launcher.exe` 会保留，且不设置强制删除期限，以兼容旧固定项、自定义快捷方式
和脚本。受控升级会用同一份已验证启动器字节更新两个名字。`versions/` 中的程序，
包括 Electron 的 `app/Reasonix.exe`，仍是内部组件。

在任何 Electron 窗口显示前，Desktop 会在永久 launcher 存在时，将窗口的重新启动
命令和图标优先设为 `Reasonix.exe`，必要时回退到旧 launcher。新建任务栏固定项因此不会依赖版本目录内的 Electron 程序，
也不会依赖启动时临时传入的服务路径环境变量。

安装器在启动 Desktop 前，为刚创建的准确快捷方式路径设置身份。它调用已安装的
launcher 维护命令 `--repair-shortcuts <绝对.lnk路径...>`；该命令只修复已有且属于
本安装的链接，随后退出，不创建窗口、不启动服务或旧版迁移器。

正常启动 launcher 和桌面进程时，也会修复安装目录、个人及公共桌面、个人及公共
开始菜单 Programs 目录（含 Reasonix 子目录），以及当前用户任务栏固定目录内名称
以 Reasonix 开头的已有链接。名称本身不能
证明归属：解析后的目标必须是当前安装内可识别的入口，指向外部的目录联接不被接受。

归属已确认且身份为空或旧 `Reasonix` 的链接会采用新身份。已经使用新身份的链接，
仍可修复过期的目标和图标。明确标注 Studio、Tauri 或未知身份的链接，即便名称为
Reasonix 也会保留原样；其他独立安装不会被修改。

标准入口 `Reasonix.exe` 存在时，指向旧 `reasonix-launcher.exe`、
`versions/<版本>/reasonix-desktop.exe`、`versions/<版本>/app/Reasonix.exe` 或平铺的 `app/Reasonix.exe`
的链接会迁回永久入口，之后删除旧版本目录也不会让快捷方式失效。修复保留启动参数、
描述、窗口显示状态、自定义图标和自定义工作目录；仅在工作目录为空或指向被迁移程序
的旧目录时，才改为安装根目录。安装器修复已有链接，不通过重建覆盖其属性。
只有旧 launcher 存在时继续使用这个有效入口，不生成失效链接。
仍在使用的平铺 Go 安装保留其有效 Go 入口。

无法读取或写入的链接会记录警告，留待后续启动重试；进程不会退回共享旧身份。
Windows Explorer 可能保留固定项缓存；若链接修复后仍显示为单独图标，可取消固定，
从永久 launcher 启动 Desktop 后重新固定。

## 共存与回退

签名更新载荷和 `/REASONIXSTAGE=1` 仍包含 `reasonix-launcher.exe`，因为已经安装的
旧更新器依赖该名称。安装后的布局单独决定：旧更新器可以直接升级，也可能继续生成
两个入口；新更新器保留已有旧入口，但不会在单入口安装中额外创建它。
`current.json` schema 1、`versioned-v1` 和签名载荷 schema 均不变。

便携 ZIP 应解压到新目录，或使用应用内更新。手工覆盖解压会保留旧文件，且不提供
事务安装保障；残留旧 launcher 可能仍是上一版字节，后续受控更新会同步更新。
若快捷方式或脚本可能引用它，不要为了节省空间自行删除。`current.json` 损坏时，
应使用完整安装器修复，不要把快捷方式改指向保留的旧版本。

升级通过既有完整发布单元机制同步交付 launcher、Electron 和 Go 二进制。
回退须恢复完整旧版本。旧 launcher 或旧桌面端可能恢复旧快捷方式身份，再次完整
升级后会重新修复自有链接；不同版本二进制混用不属于身份兼容保证。

旧 `Reasonix` 通知注册和通知历史不删除、不迁移，因为已安装的 Studio 仍可能使用它们。
新版 Desktop 通知使用自己的注册。绑定于旧身份的 Windows 通知偏好不会复制到新身份。

Studio 自身 Electron 运行时和通知身份的统一单列后续修复。Desktop 不修改 Studio
文件，不自动升级或卸载 Studio。

## 合并前验证

运行 Windows 原生应用身份、launcher、通知测试，Electron 类型检查与测试，以及安装器
打包检查。源码检查与模拟调用不能证明 Explorer 最终的任务栏分组行为。

在 Windows 11 上，分别将候选版本与 Studio v2.10.0、当前 Studio 版本同时运行。
使用独立测试安装与数据目录，覆盖首次安装、旧固定项升级、两种启动顺序、固定与取消
固定、从各自固定项重启、最小化与恢复、通知来源隔离，以及删除旧 Desktop 版本目录后
启动。两款产品必须独立分组并启动正确应用；记录实际构建版本，以及安装版和便携版
是否完成验证。
