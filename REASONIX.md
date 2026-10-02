# Reasonix project memory

These standing instructions enter the cache-stable system prefix. Keep durable
project contracts here; use the linked documents for task-specific procedures.

## Product and architecture

- Shared behavior belongs in the transport-agnostic `control.Controller` behind
  CLI, HTTP/SSE, and Desktop. Package ownership and import boundaries are enforced
  by `tools/repolint/layers.go`; package explanations belong in `doc.go`.
- Subagent boundaries: profile = worker policy/ceilings, `TaskSpec` = this call,
  `CapabilityGrant` = permitted resources, `ContextRequest` = initial context,
  `SchedulerPolicy` = scheduling. Keep per-call values out of profiles; see
  `internal/agent/profile_boundary_test.go`.
- Keep the provider-visible system prefix and tool schemas byte-stable across
  turns. Put changing state in the turn tail/session-context; standing-document
  edits enter the prefix after reload/new session.
- For shared-state defects, fix the owning state/lifetime boundary. Group related
  flags into named substates instead of multiplying independent booleans.
- Performance changes need evidence at the final provider, frontend, or trajectory
  boundary through real assembly; `internal/boot/effect_test.go` is an example.

## Execution and verification

Complete the authorized outcome and its relevant acceptance gates. A first patch
or focused green test is not completion when requested work remains. Continue
independent work when a gate is blocked, then report the missing evidence.

Local tests use disposable fixtures without production access. Run relevant
checks and fix failures introduced by the requested change without asking again.
Choose checks by changed behavior and blast radius; documentation-only work needs
content/link checks. Root Go tests do not cover the separate `desktop/` module.

Use ordinary commits and fast-forward pushes for review fixes. History rewrites
and force-pushes need explicit authorization and a freshly verified target.
A workflow document does not grant publication, merge, or release authorization.

## Task references

- Build, tests, lint, code style, and PR metadata:
  [CONTRIBUTING.md](CONTRIBUTING.md). Format changed Go files; retain required CI
  checks. Budget exceptions require narrow, measured justification.
- Transcript viewport, virtualization, measurement, or scroll behavior:
  [scroll contract](docs/TRANSCRIPT_SCROLL_CONTRACT.md).
- Standing instructions, imports, background facts, and memory scope:
  [memory retrieval](docs/SESSION_MEMORY_RETRIEVAL.md).
- Release qualification and publication: [releasing](docs/RELEASING.md).

Write comments for non-obvious constraints or invariants, not code paraphrases.
Use the repository lint rules as the source of truth for mechanical limits.
