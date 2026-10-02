# Instructions (AGENTS.md / REASONIX.md)

### Load order (ascending specificity)

User global docs → ancestor chain → project docs → project-local (`*.local.md`).

Recognized names: `REASONIX.md`, `AGENTS.md`, `CLAUDE.md` (and `*.local.md` variants). Multiple files in one directory can load; symlink identity is deduped.

Instructions fold into the system prompt at session boot (cache-stable prefix);
Hooks remain runtime event handlers loaded from their configured locations.

### Checks

Diagnostics → Instructions; memory Settings; read files on disk.

### Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| Guidance ignored | Wrong filename / empty file | Use recognized names under correct dir |
| Wrong scope won | Local override | Check load order in report |
