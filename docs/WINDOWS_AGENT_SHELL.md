# Windows Agent shell / Windows Agent 解释器

Windows Agent commands use PowerShell by default. Automatic selection prefers PowerShell 7 and falls back to Windows PowerShell 5.1. To use Git Bash, choose **Git Bash** in Settings → Sandbox → Shell interpreter, or set `[tools.shell] prefer = "bash"`. Existing Bash preferences are honored again; compatible configured paths are preserved. Remote execution uses the remote host's operating system.

Windows Agent 命令默认使用 PowerShell；自动模式优先选择 PowerShell 7，再回退到 Windows PowerShell 5.1。要使用 Git Bash，可在“设置 → 沙箱 → Shell 解释器”选择 **Git Bash**，或配置 `[tools.shell] prefer = "bash"`。已有 Bash 偏好重新生效，兼容的自定义解释器路径会保留。远程执行根据远端主机操作系统选择解释器。

The settings page reports Git Bash's detection status and executable path alongside PowerShell. If Git Bash is missing, install Git for Windows manually through the official download link, then select **Re-detect and reload session**. Reasonix does not run an installer. Missing or unusable explicit Bash preferences warn and fall back to automatic PowerShell selection; WSL launchers and the graphical `git-bash.exe` launcher are never executed as the shell. The latter is resolved to its console `bin/bash.exe` or `usr/bin/bash.exe`.

设置页与 PowerShell 一起展示 Git Bash 的检测状态和可执行文件路径。未安装时，通过官方链接手动安装 Git for Windows，再点击“重新检测并加载当前会话”；Reasonix 不会运行安装器。显式选择的 Bash 缺失或不可用时，会提示并回退到自动选择的 PowerShell。WSL 启动器和图形界面的 `git-bash.exe` 不会作为命令解释器执行；后者会解析到安装目录中的 `bin/bash.exe` 或 `usr/bin/bash.exe`。

When Git Bash is bound, the provider-visible tool is `bash` and commands use Bash syntax. With PowerShell bound, the tool remains `pwsh`. Settings show the current session's interpreter separately from the interpreter selected after reload. A deliberate interpreter switch changes the tool schema and can invalidate the prompt cache once; schemas remain stable while the interpreter stays unchanged. Both interpreters run as the current OS user on Windows, without an OS-level shell sandbox.

绑定 Git Bash 时，发给模型的工具名是 `bash`，命令使用 Bash 语法；绑定 PowerShell 时仍为 `pwsh`。设置页分别显示当前会话解释器和重载后的解释器。主动切换解释器会改变工具说明，可能使提示词缓存失效一次；解释器不变时工具说明保持稳定。Windows 上两种解释器都以当前系统账户运行，没有 OS 级 Shell 沙箱。

No configuration format migration is needed. / 不需要迁移配置格式。

Explicit `powershell` and `pwsh` preferences select their respective versions: a retained path named for the other version is ignored at runtime, without deleting it from the configuration. Compatible portable PowerShell paths appear in detection with the same executable used by the resolver. WSL aliases in `Microsoft\WindowsApps` are excluded even when their health check succeeds.

显式选择 `powershell` 或 `pwsh` 时，会按对应版本解析；配置中保留的另一版本路径只在运行时忽略，不会被删除。兼容的便携版 PowerShell 路径会出现在检测列表中，与实际解析的解释器保持一致。`Microsoft\WindowsApps` 中的 WSL 别名即使通过健康检查，也不会被当作 Git Bash。

Foreground Git Bash sessions use a persistent process with piped input/output on Windows. Direct pipes avoid ConPTY screen rendering changing Unicode output or completion markers, while retaining the working directory, variables, and functions between calls. Cancellation and timeout retire the tracked process tree and reset the session. This does not change explicitly opened user terminals or background jobs.

Windows 上的 Git Bash 前台会话使用管道连接的持久进程，避免 ConPTY 屏幕渲染改写中文输出和命令结束标记；工作目录、变量、函数仍可跨调用保留。取消或超时会终止受跟踪的进程树并重置会话。主动打开的用户终端及后台任务保持原有行为。

| Field / 字段 | Existing data / 已有数据 | Restored reader / 恢复后的读取行为 | Previous PowerShell-only reader / 此前仅支持 PowerShell 的版本 | Compatibility / 兼容性 |
| --- | --- | --- | --- | --- |
| `tools.shell.prefer` | `bash` remains valid / `bash` 仍有效 | Explicitly selects Bash / 显式选择 Bash | Retains `bash`, resolves as auto / 保留 `bash`，按自动模式运行 | Same stored value, restored behavior / 值不变，恢复运行行为 |
| `tools.shell.path` | Existing executable path / 原解释器路径 | Preserves path; rejects cross-dialect binaries / 保留路径，排除不兼容解释器 | Remains readable / 仍可读取 | No rewrite / 不改写 |

The provider-visible PowerShell tool is `pwsh`. Each foreground call runs in a fresh PowerShell process, so directories, variables, functions, and environment changes do not carry into the next call. Use `run_in_background=true` for servers and watchers; it returns a `pwsh-*` job id that can be read with `job_output` and stopped with `job_kill`. PowerShell 5.1 does not support `&&` or `||`; portable calls should use `;` or `if ($?) { ... }`.

PowerShell 向 provider 暴露的工具名为 `pwsh`。每次前台调用都使用全新的 PowerShell 进程，因此目录、变量、函数和环境修改不会带入下一次调用。服务器和 watcher 应设置 `run_in_background=true`，调用会返回 `pwsh-*` job id，可用 `job_output` 读取、用 `job_kill` 停止。PowerShell 5.1 不支持 `&&` 或 `||`；兼容写法应使用 `;` 或 `if ($?) { ... }`。

For a local OpenMAIC checkout whose start command is `npm run start`, a
background call looks like this (replace the directory and start command for
the deployment):

```json
{"command":"Set-Location 'C:\\OpenMAIC'; $env:PORT='3000'; npm run start","description":"Start OpenMAIC service","run_in_background":true}
```

Read its incremental log or wait for a terminal state with
`job_output({"job_id":"pwsh-1","wait":false})`, and stop the whole process
tree with `job_kill({"job_id":"pwsh-1","reason":"service no longer needed"})`.

OpenMAIC and its children may use any stdio mode, including Node/libuv
`stdio: "pipe"` capture: Windows commands run as the current OS user without a
restricted token, so no sandbox-level `EPERM` occurs. Runtime failures are
reported as `execution` results with their real exit codes.

如果本地 OpenMAIC 的启动命令是 `npm run start`，可以按上面的方式后台启动；
请按实际部署替换目录和启动命令。返回 `pwsh-*` 后，使用 `job_output` 读取增量
日志或等待终态，使用 `job_kill` 停止完整进程树。

OpenMAIC 及其子进程可以使用任意 stdio 方式，包括 Node/libuv 的 `stdio: "pipe"`
捕获：Windows 命令以当前系统账户运行、没有受限令牌，因此不会出现沙箱层面的
`EPERM`。运行期失败以真实退出码作为 `execution` 结果报告。

Reasonix no longer runs a nested shell/child-process preflight before each Windows command, and it no longer launches commands through a restricted-token runner: the command starts directly as the current OS user. A process that fails to start is still reported as not run rather than as command output, so stdout/stderr cannot claim that execution never started; runtime failures remain `execution` with possible partial effects.

Reasonix 不再在每条 Windows 命令前运行嵌套 shell／子进程预检，也不再通过
restricted-token runner 启动命令：命令直接以当前系统账户运行。进程启动失败仍按
“未执行”报告而不是混入命令输出，命令输出无法伪造“未执行”状态；运行期失败仍保持
`execution` 和可能已部分修改的状态。

An unconfirmed submission is not evidence that sending failed. Reconnect and inspect the session before sending again. Tool timeouts and cancellations take priority over legacy zero exit codes; missing results are shown as unknown.

提交结果未确认不代表发送失败。请先重连并检查会话，再决定是否重新发送。工具超时和取消状态优先于历史零退出码；缺少结果时显示未知状态。
