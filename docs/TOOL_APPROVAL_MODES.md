# Permission presets

Reasonix uses one permission preset for every execution surface: Desktop, CLI,
Serve, ACP, bots, automations, and subagents. Collaboration mode (Normal, Plan,
or Goal) controls how work advances; the permission preset controls the enforced
filesystem and process boundary.

| Preset | Filesystem boundary | Approval behavior |
| --- | --- | --- |
| **Read only** (`read-only`) | Workspace and session files are mounted read only. | Reads run directly. Writes and unknown external side effects require an exact one-time or session grant. |
| **Workspace write** (`workspace-write`) | The workspace and session-private temporary directory are writable. | Default. Ordinary commands, pipes, command substitution, inline Python/Node, builds, and tests run without syntax-based prompts while they remain inside the boundary. |
| **Full access** (`danger-full-access`) | Commands use the normal host path as the current OS user; Reasonix filesystem and network sandboxing are disabled. | Ordinary `ask` fallbacks are skipped. Explicit host `deny` rules still apply before launch, but Reasonix does not constrain the launched process. |

- **Collaboration mode** (Normal / Plan / Goal) decides how Reasonix advances the task. There is no automatic task mode or selectable quality floor. Verification obligations come from real tool actions, project rules, task risk, and explicit user requirements.
- **Permission preset** decides the enforced filesystem and process boundary and when exact grants are requested.

macOS enforces restricted presets with Seatbelt and Linux uses bubblewrap. If
the required backend cannot start there, restricted presets fail closed;
Reasonix never offers to silently rerun the command without isolation. Windows
has no OS-level shell sandbox: restricted presets still confine Reasonix file
tools and request exact grants, but shell commands run as the current OS user.

## Approval scope

An approval card offers at most three decisions:

1. **Allow once** — authorize only this request ID.
2. **Allow for this session** — reuse the displayed canonical directory,
   command prefix, server capability, or operation target until the session ends.
3. **Deny** — reject the call and return the denial to the model.

There is no permanent approval action. Session grants are held in memory, do not
cross restarts or forks, and can be inspected and revoked. Changing presets or
revoking a grant advances the permission revision. Preset changes and approval
commits are serialized, so an approval that commits first remains valid while a
reply from an older committed revision is rejected.

Ordinary command failures, HTTP errors, timeouts, and application exceptions are
tool errors. They do not create permission prompts. A retry may request
`sandbox_permissions` only after the host records a real sandbox denial, and
the retry must carry the matching `denial_id` and a justification.

## CLI

New sessions default to workspace write:

```sh
reasonix --permission-mode read-only
reasonix --permission-mode workspace-write
reasonix --permission-mode danger-full-access
reasonix run --permission-mode workspace-write "run the tests"
```

In the interactive CLI, `Shift+Tab` cycles Read only → Workspace write →
YOLO → Plan, and `Ctrl+Y` toggles YOLO directly. Both shortcuts
apply the canonical `danger-full-access` preset when they enter YOLO. Legacy saved values are migrated
conservatively: `ask` becomes read only, and `auto` or `yolo` becomes workspace
write. Legacy values never enable full access.

## Remote compatibility

Serve advertises `permission-presets-v1` and its real enforcement capability.
An older remote without that capability remains available for history viewing,
but execution and permission changes are disabled until the remote is upgraded.
