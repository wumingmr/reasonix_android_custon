# Archive integration qualification / 归档集成验收

Base: `main-v2@a4b83056a99ff1da1700a585e8b9da71fb872838`.
Desktop protocol 10; registry schema 2; canonical codec and Serve unchanged.
The earlier archive-v2.5 build is not evidence for this integration.

基线如上，本地协议 10、注册表 schema 2，正文 codec 及 Serve 不变。
旧 archive-v2.5 测试包不作为本次集成的验证证据。

## Current evidence / 当前证据

All data tests use disposable fixtures, not personal history. These counts do
not claim that a user's existing data has already been repaired.
全部数据测试使用一次性夹具，不操作个人历史，不宣称已经修复用户的真实数据。

| Gate / 门禁 | Status / 状态 |
| --- | --- |
| Root Go full suite / 根模块全量 | Passed after the recovery-close repair, including generated inventory / 恢复关闭修复后全量通过，包括生成物检查 |
| Desktop full suite / Desktop 全量 | Passed after the migration/catalog checkpoint and source diagnostic fixes (415 seconds) / 迁移与目录索引校验、来源诊断修复后全量通过（415 秒） |
| Frontend / 前端 | 379 suites passed; production build and typecheck passed / 379 套件及生产构建、类型检查通过 |
| Race / 竞态 | Desktop registry/runtime/migration and session purge race checks passed. Recovery-close deterministic interleavings and related cancellation race tests passed ten repetitions / Desktop 身份、注册表、迁移及删除竞态检查通过；恢复关闭确定性交错和取消竞态回归重复十次通过 |
| Fault tests / 故障测试 | Content-publication interruption, receipt replay, pending purge visibility, external writer exclusion, symlink/staging refusal / 正文发布中断、回执重放、删除未完成入口、外部写锁、符号链接及暂存冲突 |
| Production package / 生产包 | Earlier smoke results are not final qualification: asynchronous Playwright predicates advanced before durable completion. The corrected harness awaits actual RPC conditions and captures registry checkpoints; use the final PR review's exact SHA and package receipt / 早期脚本的异步 Playwright 谓词提前推进，结果不作为最终验收；修正后等待真实 RPC 条件并记录注册表检查点，交付以 PR 最终评审的 SHA 和包回执为准 |
| Windows/Linux / 跨平台 | Native runs unavailable on this macOS host; no native success claim / 本机未原生运行，不标记为通过 |

## Review repairs / 评审修复

- Preserve upstream multi-head migration and durable submission receipts;
  quarantine changed adopted sources without overwriting continued targets.
- Stamp command generation while holding the registry writer lock and reject
  intervening lifecycle changes again at child-operation admission.
- Publish proven legacy trash through an explicit archive-import commit;
  preserve its recorded timestamp or leave time unknown.
- Retain originals in the legacy purge RPC; canonical tombstones suppress
  repeated adoption. Reject unowned purge staging directories.
- Report malformed source manifests in the ledger while migrating healthy
  sources; replay failure does not suppress independent discovery.
- Compare durable migration inputs when catalog repair rewrites identical
  source bytes or disposable metadata; retain unknown metadata in the digest
  and reject changes to content, ancestry or workspace identity.
- Keep recovery terminal fanout and watchdog publication under controller
  resource ownership until both finish. The deterministic close interleaving
  failed before the repair; CI's temporary-directory cleanup failure was not
  addressed by disabling the race job or retrying cleanup.
- Retire unbound canonical navigation caches on archive and purge. A released
  client can leave a 60-second cached writer, which must not be confused with
  an active client. Bound or executing runtimes still reject retirement.
- Poll asynchronous smoke RPC conditions outside Playwright's synchronous
  truthiness loop; verify false, timeout and rejection behavior. Earlier runs
  could stop the process during purge and observe pending recovery on restart.

- 保留上游多 head 迁移与持久提交回执；已采用来源变化进入待校验，不覆盖后续正文。
- 注册表写锁内写入准确 generation，子事务再次拒绝过期生命周期意图。
- 明确的旧回收站记录使用专用归档导入提交，保留已有时间或显示未知。
- 旧 purge RPC 同样保留原件；canonical 墓碑阻止再次采用，拒绝无归属的删除暂存。
- 损坏 manifest 记录失败且不影响健康来源，重放失败不阻断独立发现。
- 目录索引修复重写相同正文或派生元数据时比较持久输入；摘要保留未知元数据，正文、谱系及工作区变化仍拒绝采用。
- 恢复末尾事件分发和 watchdog 发布完成前保留控制器资源所有权；确定性交错测试在修复前失败，未通过关闭 race 检查或重试目录清理掩盖问题。
- 归档及删除回收无客户端的 canonical 导航缓存，避免将解绑后保留 60 秒的 writer 当成活动会话；仍被绑定或正在执行的运行时继续拒绝回收。
- 异步验收 RPC 使用真正等待结果的轮询，覆盖 false、超时及拒绝；此前脚本可能在删除中途退出，重启时看到待完成恢复。

## CI assessment / CI 核查

The root Windows smoke selector excludes the separately owned agent, boot and
control groups. Full memory profiling is applicable because this change touches
the bridge and lifecycle owners. The earlier memory run was cancelled, not an
assertion failure or an exhausted 90-minute timeout; cancellation alone does not
justify deleting the guard. No CI budgets or required checks were weakened.
The build-contract guard now tests unchanged, changed, added, removed and failed
generation behavior instead of requiring a specific `git diff` spelling; an
uncommitted but correctly generated local contract may be packaged.
Current-head terminal checks and package receipts are recorded on
[PR #10396](https://github.com/esengine/DeepSeek-Reasonix/pull/10396), rather than
claiming that an older run qualifies a later commit.

Windows smoke 已排除单独执行的 agent、boot 和 control 分组。本次涉及桥接和生命周期，
完整内存检查适用。此前内存任务是被取消，并非断言失败或耗尽 90 分钟超时；仅凭取消不能
删掉门禁。本次未放宽预算或必需检查。构建契约检查改为验证无变化、修改、新增、删除及
生成失败的行为，不再硬编码要求某种 `git diff` 写法；尚未提交但生成正确的本地契约可打包。
当前提交的最终 CI 和打包证据记录在 PR #10396，
不以旧提交的通过结果替代新提交验收。

## Remaining qualification / 尚待验收

No release qualification is claimed until the integrated package, current-head
CI and final owner checks are recorded. Large-history list latency, real disk
exhaustion/power loss and native Windows/Linux locks remain separate gates;
cross-compilation and simulated failures are not substitutes.

集成包、当前提交 CI 和最终模块检查记录前，不宣称达到发布条件。大规模列表延迟、
真实磁盘满/断电、Windows/Linux 原生文件锁需单独验证；交叉编译或模拟失败不能替代。

See [compatibility guide](session-archive-upgrade.md) for source retention,
rollback and format handling. 降级、原件保留与格式矩阵见兼容文档。
