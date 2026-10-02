# Commands (slash templates)

### Priority

`config.CommandDirsForRoot`: home convention commands → Reasonix home commands → project convention commands. **Later directory overrides earlier** on name clash (`command.Load`).

Name from path: `git/commit.md` → `/git:commit` (slashes → `:`).

### Checks

CLI/Desktop Diagnostics → Commands; invoke `/name` in chat.

### Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| Wrong body | Shadowed by later dir | Check `command.shadowed` winners |
| Missing command | Wrong dir / extension | Place `*.md` under a scanned `commands/` root |
| Parse fail | Unreadable file | Fix permissions / encoding (`command.read_failed`) |
