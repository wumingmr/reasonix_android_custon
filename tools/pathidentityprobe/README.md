# Reasonix Windows 路径诊断 / Windows path probe

用于核对 Go 路径解析、构建时的 Reasonix 路径解析和 Windows 原生句柄解析是否在 Junction 上产生不同结果。报告中的 identity_version 标识 Reasonix 解析器版本；修复版为 3。它是诊断工具，不会修改已安装应用。

Compares Go, the build's Reasonix resolver, and native Windows handle resolution through junctions. The identity_version field identifies the Reasonix resolver (3 for the fixed implementation). This diagnostic does not change the installed application.

## 使用 / Usage

1. 将整个 ZIP 解压到普通、可写的本地文件夹，不要直接在压缩包内运行。
2. 使用受影响用户的普通 Windows 账户，双击 `run-probe.cmd`。无需安装 Go，也无需管理员权限。
3. 等待完成，通常数秒，采集上限默认 60 秒。把同目录生成的 `reasonix-path-probe-*.json` 返回给排障人员。
4. 若程序被系统阻止或报错，记录原文并返回；不需要关闭系统防护。

Extract the ZIP to a writable local folder. Run `run-probe.cmd` as the affected Windows user without elevation. Return the generated JSON beside the executable. Go installation is not required. If execution is blocked, return the exact error without disabling protections.

可选命令 / Optional command:

```powershell
.\reasonix-path-probe.exe -out .\new-report.json -timeout 60s 'E:\ReasonixData\.reasonix'
```

指定的输出文件必须尚不存在。额外路径只增加采集目标，默认锁目录仍会检查。

The output must not already exist. Positional paths add targets; default lock paths remain included.

## 采集范围 / Collection scope

- 检查当前 OS 用户的 `.reasonix\locks\config-edits-<用户身份摘要>`、最多 99 个已有 `.lock` 文件及祖先目录，并比较原生解析得到的物理路径；最多 300 个路径。
- 逐项记录 `Lstat`、`Readlink`、`filepath.EvalSymlinks`、Reasonix `pathidentity.Resolve` 和 Windows `CreateFileW` / `GetFinalPathNameByHandleW` 的结果、错误链、系统错误码、文件身份。
- 不读取配置、凭据、锁文件或会话内容；不申请锁、不联网、不修改现有应用数据。唯一写入是新建 JSON 报告。
- 报告替换当前主目录用户名和 SID；其他目录名、盘符、文件名和时间仍保留，便于判断路径问题。转发前可检查报告。
- 普通文件上的 `Readlink` 失败、刻意不存在的 `reasonix-probe-uncreated-child\absent.lock` 上的直接查询失败均属预期；不能把每条 `ok: false` 都当作根因。
- 超时会保存已完成部分，并在 `timed_out` / `incomplete_path` 标明。原生路径成功但文件身份读取失败时，`errors` 仍会记录后者。

Collects path metadata, structured error chains and file identity only. It does not load configuration, read file contents, acquire locks, access the network, or modify existing application data. Its only write is a newly created report. The current home-directory username and SID are redacted; other path components remain. Readlink failures on ordinary entries and direct lookup failures on the deliberately absent child are expected. Timed-out collections preserve completed observations. Inspect `errors` even when native path resolution succeeds.

## 判读 / Interpretation

若逻辑路径的 Go/Reasonix 解析失败，而同一路径的原生解析成功，且原生解析得到的物理路径能被 Go/Reasonix 正常解析，则支持 Junction 解析分歧这一判断。必须以报告中的具体失败路径和错误链为准；工具不会自动认定 Studio 为原因。

A failure on the alias in Go/Reasonix, native success on that same alias, and Go/Reasonix success on the resulting physical path support a junction-resolution discrepancy. Use the actual failing paths and error chains; the probe does not attribute causation to Studio.

## 构建 / Build

From the repository root, using the affected release's Go version:

```sh
GOTOOLCHAIN=go1.26.6 go test ./tools/pathidentityprobe ./internal/pathidentity
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOTOOLCHAIN=go1.26.6 go build -trimpath -o reasonix-path-probe.exe ./tools/pathidentityprobe
```

随包构建信息记录完整源码提交、工具链、源码校验值及验证范围。Windows 交叉编译通过不等于 Windows 实机执行通过。

The package build record identifies the source commit, toolchain, source hashes and validation scope. Windows cross-compilation does not establish native Windows runtime success.
