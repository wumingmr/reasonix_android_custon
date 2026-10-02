# Hooks

### Events and blocking

Use `/hooks` or Diagnostics → Hooks for the resolved events and sources. Tool and stop failures have separate events: `PostToolUseFailure` and `StopFailure`.

Native `PreToolUse` and `UserPromptSubmit` hooks can block progress. Imported Claude `PermissionRequest` hooks also deny the action on exit 2 or timeout; check the hook's payload format before interpreting a failure.

### Sources

- Project: `<workspace>/.reasonix/settings.json` — loaded automatically
- Plugin packages: installed enabled packages
- Global: `<Reasonix home>/settings.json` (always)

Match field is an **anchored** regex: `file` does **not** match `read_file`; use `.*file` or `*`. Native timeout values are **milliseconds**: defaults are 5s for `PreToolUse`, `PermissionRequest`, and `UserPromptSubmit`, and 30s otherwise.

### Checks

`/hooks`, Settings → Hooks, Diagnostics → Hooks.

### Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| Project hooks silent | Wrong workspace / restart required | Confirm the project path and restart Reasonix after saving |
| Matcher never fires | Non-anchored assumption / bad regex | Fix match (`hook.invalid_matcher`) |
| Command missing | Empty command / missing context file | Fix settings entry |
| Malformed JSON | Invalid settings.json | Repair JSON (file yields no hooks, no crash) |
