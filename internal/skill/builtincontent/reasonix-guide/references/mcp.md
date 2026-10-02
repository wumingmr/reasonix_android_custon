# MCP servers

### Merge order

`config.LoadForRoot` merges:

1. User/project TOML `[[plugins]]` (higher name wins vs later sources when already defined)
2. Project `.mcp.json` servers not already in TOML
3. Enabled **plugin packages** MCP (skipped if name already defined)

Transports: `stdio` (default), `http` / streamable-http, `sse`.

Enabled servers register cached tools and start on the first real tool call.
Persisted activation overrides take precedence over `auto_start`; without an
override, false means disabled and nil/true means enabled. `tier` is retained
for configuration compatibility and diagnostics; it does not control runtime
process start timing. Diagnose enablement and connection state separately.

Env/header values may contain secrets — diagnostics list **keys only**.

### Checks

| Mode | Behavior |
| --- | --- |
| Static doctor | Config validity, command path / URL shape, start intent — **no** subprocess |
| CLI `--live` | Isolated Host via `boot.PluginSpecsForRoot` + `plugin.Start`; auto-start only; concurrency 4; always Close |
| Desktop runtime | Read active tab Host only |

### Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| Not connected | Disabled, not yet called, or failed start | Check activation override and runtime state; fix command/URL if startup failed (`mcp.command_not_found`, `mcp.start_failed`) |
| No tools | Connected but empty tools/list | Server config or permissions (`mcp.no_tools`) |
| Wrong source | Shadowed by TOML vs `.mcp.json` vs package | Inspect report Source / package owner |
| Invalid transport | Bad `type` | Use stdio/http/sse (`mcp.invalid_transport`) |
