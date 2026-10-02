# Terminal text encoding / 终端文本编码

## Child-process locale / 子进程 locale

When LANG, LC_ALL and LC_CTYPE are all empty or absent, Reasonix selects an
installed UTF-8 locale for child processes. It prefers C.UTF-8, then en_US.UTF-8,
then another installed UTF-8 locale. Discovery is bounded and cached per process.
If discovery fails or no UTF-8 locale is installed, the environment is retained.
Windows continues to use its existing encoding-specific process handling.

当 LANG、LC_ALL、LC_CTYPE 均为空或未设置时，Reasonix 为子进程选择已安装的 UTF-8
locale，优先 C.UTF-8，其次 en_US.UTF-8，再选择其他已安装的 UTF-8 locale。
探测有时间限制，并在进程内缓存。探测失败或没有可用 UTF-8 locale 时，保留原环境。
Windows 继续使用既有的编码处理。

Explicit values, including C/POSIX and category overrides, remain unchanged.
Reasonix does not change the parent process environment or force LC_ALL. Thus an
explicit LC_ALL=C still requests byte-oriented sed/grep/sort semantics.

用户显式设置的值（包括 C/POSIX 和各分类覆盖）保持不变；不会修改父进程环境或强制
设置 LC_ALL。因此显式 LC_ALL=C 仍然要求 sed、grep、sort 等工具使用对应的字节语义。

## Interactive input / 交互输入

Machine-submitted persistent-shell commands use ASCII-only byte escapes. The
integrated Bash terminal instead uses a private, temporary Readline configuration:
it includes the user's existing inputrc and then enables eight-bit input/output
without meta conversion. Other editing preferences remain in place. This accepts
Chinese and emoji even with an explicitly selected C locale. The temporary file
is removed on launch failure, terminal closure or process exit.

机器提交给持久 Shell 的命令使用纯 ASCII 字节转义。内置 Bash 终端则使用私有临时
Readline 配置：先包含用户原有 inputrc，再开启八位输入输出并关闭 meta 转换，保留
其他编辑偏好。这样在显式 C locale 下也能接收中文和 emoji。临时文件会在启动失败、
终端关闭或进程退出时清理。

Long persistent-shell commands are sent as bounded ASCII blocks. Each block
must be acknowledged before the next is submitted; only the final step executes
the assembled command in the current shell. This avoids canonical terminal
input truncation while preserving variables, directory, exit status and stdin
isolation. Cancellation during staging retires the shell without executing the
partial command.

较长的持久 Shell 命令会拆成有长度上限的 ASCII 块，每块确认接收后才发送下一块，
最后在当前 Shell 中执行完整命令。这样可避免终端规范模式截断输入，同时保留变量、
工作目录、退出码与标准输入隔离。分块传输期间取消操作会关闭该 Shell，不执行未完整
传输的命令。

## Tool output / 工具输出

Shared live output buffers incomplete UTF-8 characters across reads before
emitting JSON events. Completion and cancellation flush an unfinished character
as one replacement character. Progress limits and retained output head/tail stop
at character boundaries; byte budgets are not increased. Durable protocol fields,
tool schemas and model prompt prefixes are unchanged.

共享实时输出层会缓存跨读取的不完整 UTF-8 字符，完整后才发送 JSON 事件。正常结束和
取消时，未完成字符以一个替代字符收尾。进度限额及最终保留的头尾内容都在字符边界
截断，不提高字节预算。持久协议字段、工具定义和模型提示前缀保持不变。
