# Model connection regression fixes

## Follow-up approval review

The follow-up fixes bind installation approval to imported environment and header
values with a durable keyed digest, while keeping those values out of the public
preview. Existing plan IDs require a new preview after upgrading. Plugin CLI JSON
again includes actions, risk, plan ID, failures and recovery instructions.

Desktop tab metadata now exposes a runtime-bound pending-settings hint. This lets
a preserved draft reach backend apply-before-admission after a credential save;
it does not mark the old controller authenticated. CLI submission checks saved
settings asynchronously before authentication, rebuilds through its existing
controller replacement path and submits only to the activated controller.
Failed builds preserve the original controller and draft; stale completions do
not start another session.

Authentication also guards actual primary, child and auxiliary requests through
the request context, preserving concrete provider capability interfaces and wire
requests. Missing credentials cover sibling models; HTTP and streamed rejection
errors carry the actual request model, so a child's 403 cannot block its parent.
Project credential edits and shell setup use document deltas, preserving unknown
and nested fields without promoting project providers into global configuration.
Windows repair preserves both repair and rollback error chains.

Regression coverage: `TestImportedExecutionInputsInvalidateApproval`,
`TestPluginDryRunPreservesApprovalPlan`,
`TestSavedCredentialAllowsAdmissionThroughPendingMetadata`,
`TestTurnAppliesExternalCredentialSaveBeforeAuthentication`,
`TestTurnSettingsCompletionCannotStartAnotherSession`,
`TestMissingCredentialBlocksSiblingModel`,
`TestSubagentRejectionMustNotBlockPrimary`,
`TestRequestGateObservesHTTPAndStreamRejectionsWithoutChangingRequest`,
`TestProjectCredentialEditPreservesUnknownProviderFields`,
`TestProjectShellSetupPreservesUnknownFields`, and composer workspace/auth tests.

Compatibility: `modelSettingsPending` is optional and defaults to false. Existing
TOML and credential files remain readable. Legacy unkeyed credential receipts
return `unknown_result` rather than replaying a write whose content cannot be
verified; new receipts use the existing durable HMAC protocol.

The following findings were reproduced with disposable Reasonix homes and
local fixtures. No production credentials or model requests are used.

| Finding | Repair | Regression evidence |
| --- | --- | --- |
| F1: credential paste reaches chat | Terminal and native clipboard delivery are intercepted by the credential editor | `TestAuditSetupPasteDoesNotEnterChat` |
| F2: repair overwrites concurrent credentials | Verification opens without creating, truncating, or rewriting the file | `TestAuditRepairVerificationPreservesConcurrentSave`, `TestRepairPreservesCredentialContentsAndFileIdentity` |
| F3: key-only shell edit is skipped | Credential edits participate in the operation/precondition stream and trigger publication | `TestAuditShellSetupKeyOnlyEditPersists`, `TestShellCredentialOnlyEditRejectsConcurrentRotation` |
| F4: separate edits share the last key | Credential drafts are keyed by connection identity | `TestAuditShellSetupDistinctConnectionKeysStaySeparate` |
| F5: repair follows linked home | Validate home/target components and identity; mutate/roll back the opened file handle | `TestAuditRepairRefusesLinkedHome` |
| F6: TUI edits shadowed project provider | Choose the source selected by the config merger | `TestAuditSetupEditsEffectiveProviderSource`, `TestProviderEditPathRespectsProjectOverrideOfBuiltins` |
| F7: recovery invents commit evidence | Journal the exact candidate revision before strict atomic publication; require matching revision and references | `TestAuditRecoverDoesNotInventReceiptAfterExternalEdit`, persistence-boundary tests |
| F8: receipt replay conflicts after restart | Use a private durable HMAC key; recheck receipts under writer locks | `TestAuditDurableReceiptAcrossProcessRestart` (actual child process) |
| F9: preferences cannot recover receipts | Empty slot sets are valid; receipt queries recover proven publications | `TestAuditRecoverCommittedPreferenceReceipt`, `TestReceiptQueryRecoversPublishedPreferenceBeforeMark` |
| F10: cleanup loses failure evidence | Retain journals on failed deletion or changed configuration | `TestAuditCleanupRetainsEvidenceOnFailedRemoval` |
| F11: auxiliary authentication keeps requesting | Record title and sibling auxiliary errors; isolate 403 by model and 401 by connection | `TestAuditTitleAuthenticationRejectionStopsFurtherRequests`, `TestAuxiliaryAuthenticationFailureIsScopedToConnectionAndModel` |
| F12: frontend retry bypasses readiness | Remove local send authorization; refresh authoritative backend tab metadata after serialized retry | `composer-authentication-recovery.test.tsx`, `TestRetryAuthenticationPublishesAuthoritativeTabRefresh` |

Browser fixture: `desktop/frontend/bench/authentication-recovery.html`. It uses
the real Composer with a disposable host. Verified delayed retry, tab switching,
controller unavailability, replacement connection, draft preservation, and
backend Ready enabling send. Model submission count stays zero throughout.

Compatibility: TOML, `.env`, and RPC field names remain unchanged. Transaction
records retain schema 1 and add the `config_prepared` phase using existing
revision fields. Older records without a publication revision remain uncertain
when references exist; they are not converted into success. Legacy Desktop
digests return `unknown_result` when replay content cannot be verified. Old
applications still read the saved connection configuration and credentials.

Windows handle-based repair is cross-compiled locally. Native ACL, sharing and
reparse-point behavior requires Windows CI; cross-compilation is not that evidence.

Local validation (2026-09-17): root and independent Desktop module `go test ./...`, focused shared-core and
Desktop race tests, shared-package `go vet`, frontend production build and test
typecheck, Composer and app-lifecycle suites, and the browser scenario above
passed. The actual Electron smoke passed the service handshake, renderer RPC,
native window query, website view, child-service ownership, and clean exit.
The smoke service used its standard binary name because the process assertion
matches `reasonix-desktop` in the executable name.
