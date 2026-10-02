# Windows cloud signing / Windows 云签名

Windows releases use Certum SimplySign. The repository needs `CERTUM_USERNAME`,
`CERTUM_OTP_URI` (the complete TOTP provisioning URI), and `CERTUM_KEY_ID` (the
40-character SHA-1 certificate thumbprint). Never commit provisioning data.
The certificate private key remains in Certum's cloud. CI holds the credentials
needed to authenticate signing, so access to these secrets is signing authority.

Windows 发布使用 Certum SimplySign。配置以上三个 Secret；不要提交二维码或 TOTP
配置。证书私钥保留在云端，但 CI 凭据能授权签名，应按签名权限保护。

Run **Certum signing smoke test** on protected `main-v2`, approve the `release`
environment, and require successful login, signing, trust, signer and timestamp
verification. This test publishes nothing. The third-party login action is
pinned to an audited commit; authentication screenshots are disabled. Review
upstream code before updating the pin.

先在 `main-v2` 手动运行签名测试并批准 `release` 环境。测试不发布产物，必须通过
登录、实际签名、信任链、签名者及时间戳检查。第三方登录 Action 固定提交，更新前
需要审查，登录截图默认关闭。

Release builds retain native x64 and ARM64 startup tests. Their exact payloads
and generated NSIS identity files pass through same-run artifacts to serialized
x64 signing jobs. Those jobs sign the PE manifest, bind the minisign payload
manifest, rebuild packages, sign installers, verify portable hashes and expected
signer/timestamps, and only then produce final minisign-signed release bundles.
Any signing failure blocks publication and signing attestation.

正式发布保留 x64/ARM64 原生构建与启动测试，再将同一运行中的产物交给 x64 签名任务。
先签内部 PE 文件，再绑定清单、重打包、签安装器并验证便携包哈希，最后生成发布包。
任何签名失败都会阻止发布和签名证明。

The legacy `signpath-contract` command, `.signpath/contracts` location and
`SIGNPATH_RELEASE_SIGNING_ATTESTATION` variable remain as compatibility names for
existing release recovery. Their fingerprint now covers the Certum setup and
signer scripts; old attestations do not authorize this new signing path. No
SignPath API request is made by the release workflow. Historical receipt helpers
remain for investigating old runs.

旧契约命令、目录和证明变量名为恢复兼容而保留，但指纹已覆盖 Certum 实现，旧证明
不能授权新流程。发布不再调用 SignPath；旧回执工具仅供历史运行排查。

A successful standalone preflight uploads `verified-signing-contract-*` evidence
and prints the maintainer command for promoting its fingerprint to the recovery
variable in the job summary. `GITHUB_TOKEN` cannot modify repository variables;
the workflow does not need a privileged personal token. Promote only the exact
fingerprint from a run whose two Windows signing jobs succeeded. Official
orchestrated releases use successful same-run preflight evidence instead.

独立预检成功后上传 `verified-signing-contract-*` 证明，并在任务摘要中给出由维护者
将指纹写入恢复变量的命令。`GITHUB_TOKEN` 无权修改仓库变量，无需为此添加高权限
个人令牌。仅登记两个 Windows 签名任务均成功的运行所验证的准确指纹；正式编排发布
使用同一运行内的预检成功结果。
