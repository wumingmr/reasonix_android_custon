# Windows Junction upgrade compatibility / Windows Junction 升级兼容

The native Windows resolver fixes a path-identity regression exposed by Desktop
v1.38.11. Go's `filepath.EvalSymlinks` can return a junction entry unchanged and
reject its descendants with `ENOTDIR` (Windows error 3). An existing cross-drive
alias can therefore fail before the configuration lock is acquired, even though
the Windows kernel can open the same path.

Windows 原生解析器修复了 Desktop v1.38.11 暴露的路径身份回归。Go 的
`filepath.EvalSymlinks` 可能原样返回 Junction 入口，并以 `ENOTDIR`（Windows
错误 3）拒绝其子路径。因此，即使 Windows 能正常打开跨盘别名路径，配置锁也会在
真正获取前失败。

## Repair boundary / 修复边界

- `pathidentity` v3 resolves existing Windows paths through a metadata-only
  native handle. This is the primary resolver, including successful-looking
  junction leaves. Missing tails resolve through an existing ancestor; dangling
  reparse points still fail. `FollowLeaf=false` follows the parent but preserves
  the final entry. UNC and long-path spelling are handled at native call sites.
- Configuration access and Windows instance validation use the same resolver.
  Instance validation still rejects missing executable/profile paths.
- Resume RPC failures have a separate frontend submission-error state. They do
  not mark a saved draft as unsaved. Concurrent resume clicks are coalesced at
  the owner, retain the original operation/revision, and preserve draft content.

- `pathidentity` v3 通过只读元数据句柄解析 Windows 已有路径，包含此前看似成功的
  Junction 入口。缺失后缀通过已有祖先解析；失效重解析点仍报错。
  `FollowLeaf=false` 只解析父目录、保留最终目录项；原生调用处理 UNC 和长路径。
- 配置访问与 Windows 实例校验共用此解析器；实例校验仍拒绝缺失的程序或配置目录。
- 恢复 RPC 失败单独记录为提交错误，不改变草稿保存状态。重复点击在所有者处合并，
  沿用同一操作 ID 和版本，并保留草稿内容。

The fix is integrated with the current eager-session creation flow. Draft UI
changes apply to recovery of historical drafts; they do not restore draft-first
creation or change formal-session input persistence.

修复基于当前立即创建正式会话的流程；草稿界面改动用于历史草稿恢复，不恢复旧的
草稿优先创建流程，也不改变正式会话的输入持久化。

## Persisted and cross-version contracts / 持久化与跨版本契约

| Contract / 契约 | Previous behavior / 旧行为 | New behavior / 新行为 | Compatibility / 兼容方式 |
| --- | --- | --- | --- |
| Config, credentials, transcripts, IDs / 配置、凭据、会话与 ID | Authoritative existing files / 原有权威数据 | Same formats and locations / 格式与位置保持 | No data migration / 不迁移数据 |
| Config lock filenames / 配置锁文件名 | User registry + resolved config hash / 用户锁目录与配置路径摘要 | Same naming algorithm / 算法保持 | Native metadata identity does not rename the lock file / 身份解析不重命名锁文件 |
| Session legacy locks / 会话旧版锁 | Frozen `Canonical` identity / 冻结的旧身份 | Remains frozen / 保持冻结 | Existing dual-lock protocol retained / 保留双锁协议 |
| Workspace locks / 工作区锁 | Historical alias may survive / 可能保留旧别名 | Physical identity plus historical spelling / 物理身份与历史拼写并存 | Both lock domains participate / 同时保护两种锁域 |
| Default catalog / 默认索引 | `v9.sqlite`, schema 13 | `v10.sqlite`, schema 14 | Old writers retain their own cache / 新旧进程使用不同缓存 |
| Explicit old catalog path / 显式指定旧索引路径 | Old filesystem keys / 旧身份键 | Migration invalidates derived rows once / 一次性重建派生行 | Source files stay authoritative; old readers have future-schema protection / 原始数据保持，旧读取器受未来版本检查保护 |
| Desktop hello | Optional identity metadata / 可选身份元数据 | `identityVersion=3` | Same payload shape / 字段结构保持 |

Native resolution does not replace the frozen legacy algorithms. In particular,
workspace compatibility keys must be derived using the old access-path behavior,
not from the newly resolved physical path. Otherwise an old alias-based writer
could hold a different lock. Unknown aliases used by unrelated old processes
cannot be discovered merely from a single supplied path; actual mixed-version
installation qualification remains required.

原生解析不替换冻结的旧算法。特别是工作区兼容键必须沿用旧访问路径算法，不能从
新物理路径重新派生，否则旧别名写入者可能持有另一把锁。仅凭一个输入路径无法发现
其它旧进程使用的所有未知别名，因此仍需要真实安装的混合版本验收。

## Verification / 验证

Release-tag inspection confirms that 1.38.7–1.38.10 share schema 12, the v8
cache generation and the same configuration/workspace lock algorithms; 1.38.11
uses schema 13 and v9. The schema-12/13 migration fixtures represent these two
storage generations, including an older reader reopening the upgraded cache.
This source-level comparison is not an installer-upgrade test.

发布标签核对确认：1.38.7–1.38.10 使用相同的 schema 12、v8 缓存及配置/工作区锁算法；
1.38.11 使用 schema 13 与 v9。迁移夹具覆盖这两代存储，并检查旧读取器重新打开升级
缓存时不会改写或隔离新缓存。此源码级对照不能替代安装升级测试。

Owner tests cover junction leaves, children, missing tails, dangling targets,
entry-preserving operations, long paths, old/current config-file locks,
historical workspace alias locks, cache migration/restart, and draft resume
failure/concurrency. A real Chromium fixture is runnable with:

```sh
cd desktop/frontend
pnpm test:draft-recovery-browser
```

Set `CHROME_EXECUTABLE` to use an installed Chromium browser. Native Windows
tests use temporary junctions created by `mklink /J`, without requiring symlink
creation privileges. Cross-compilation only validates compilation, not native
Windows execution or a full installer upgrade.

Pull-request CI runs the path, identity-lock, workspace-lock, instance and catalog
owners in Windows smoke, and the configuration-junction regression in the Windows
contract selector. The browser recovery fixture is part of `test:app-browser`.

PR CI 在 Windows smoke 中执行路径、身份锁、工作区锁、实例和索引所属包，并在
Windows contract 中执行配置 Junction 回归；恢复界面验证已接入 `test:app-browser`。

回归覆盖 Junction 入口/子项、缺失后缀、失效目标、保留目录项、长路径、旧新版配置锁、
历史工作区别名锁、缓存迁移及重启、草稿恢复失败与并发。上述命令运行真实 Chromium
场景；可通过 `CHROME_EXECUTABLE` 指定已安装浏览器。Windows 用临时 `mklink /J`
构造 Junction，不依赖符号链接创建权限。交叉编译不能替代 Windows 执行或完整安装升级。

Release qualification must additionally run upgrades from 1.38.7–1.38.11 on
Windows, ordinary/same-drive/cross-drive layouts, Desktop/CLI/Studio coexistence,
and creation/save/history/restart. The affected user's original junction should
be rechecked with the fixed resolver before declaring this incident resolved.

发布验收还需在 Windows 执行 1.38.7–1.38.11 升级、普通/同盘/跨盘目录、
Desktop/CLI/Studio 共存，以及创建/保存/历史/重启流程。宣布此事故解决前，需要用修复
解析器回验用户原来的 Junction。
