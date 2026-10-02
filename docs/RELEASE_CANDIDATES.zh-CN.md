# 发版候选

受保护的 `main-v2` 控制工作流在 Stable 发布说明审核并嵌入后准备不可变候选。
`Prepare release candidate` 使用 `version` 构建共用的 CLI/npm 文件、签名
Desktop 文件、执行最终安装包的原生验收并封存记录。`Publish release candidate`
只接受该记录，验证来源与文件字节，经过一次 `release` 批准后创建三个标签并发布。
`recover` 复用相同的封存文件，不重新构建或签名。

渲染发布说明时会为贡献者署名：`refs` 中属于合并请求的编号后面加上
`by @作者`；属于 issue 的编号，对 GitHub 记录为关闭它的每个已合并请求加上
`fixed in #M by @作者`。结尾的「贡献者」列表按首次出现顺序列出这些真人作者，
每人一次。直接推送修复的 issue 和机器人作者保持原样。查询需要 GitHub 令牌（`GH_TOKEN`，
本地没有时用 `gh auth token`），以及 `issues: read` 和 `pull-requests: read` 权限。
暂时性失败（5xx、限流、连接中断）会退避重试；重试后仍失败或鉴权失败时，渲染以
`release_credits.*` 错误码失败，而不是发布缺少署名的说明。指向讨论或不存在的编号保持原样并给出警告。

封存任务只渲染一次，写进候选包的 `evidence/release-notes.md`，记录为
`notes.renderedPath` 并与 `notes.renderedSha256` 绑定。发布时校验候选包后直接发布这份
内容，不再访问 GitHub，因此封存之后作者改名、issue 新关联了修复或接口故障都不会改变或
阻塞发布内容。本改动之前封存的候选没有 `renderedPath`，仍按原方式从自身源码离线渲染。

需要验收但不发布时，在受保护的 `main-v2` 上调度 `Prepare release candidate`，
填写 `version` 和 `rehearsal=true`。可使用已有审核完成的版本进行隔离演练。
它使用独立的 `release-candidate-rehearsal-*` 产物，记录
`purpose=rehearsal`，无法通过正式发布的解析和文件验证入口。Desktop 子流程
仅在这个不发布的模式下接受已存在的版本标签。该运行仍须完成源码 CI、签名和
原生验收；它不会创建标签、GitHub Release、npm 包，或更新 Homebrew、R2
指针及官网。

封存后以候选 ID 在 `main-v2` 上运行 `Verify release candidate rehearsal`。
这个独立工作流按精确的记录和文件 artifact ID 下载，验证 GitHub 归档摘要、
受保护的生产 run、OIDC 文件证明、封存文件摘要和原生验收凭据。保留 90 天的
报告绑定生产和复核 run；复核过程不需要编译器或签名凭证。它证明同一份签名
文件可以跨 run 复用，不等于公开发布，也不能代替下一次正式发布的公开验收。

候选文件保留 30 天，记录和验收证据保留 90 天。尚未发布时若文件过期，需
重新准备候选。正式发布后的标签、npm、Desktop 更新、Homebrew 与官网
渲染结果仍以发布 skill 的公开 postflight 验证为准。
