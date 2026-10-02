#!/usr/bin/env python3
"""Pin the release/PR storage boundaries used by the rollback compatibility gate."""
import json
import pathlib
import re
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def source(ref, path):
    result = subprocess.run(["git", "show", f"{ref}:{path}"], cwd=ROOT, capture_output=True, text=True)
    return result.stdout if result.returncode == 0 else ""


def areas(paths):
    groups = {
        "session-storage": r"^internal/(session/|sessioncontent/|agent/(session|save|branch))",
        "workspace-and-lifecycle": r"^desktop/(internal/workspacestate/|session_|historical_|tab_|topic_|legacy_)",
        "drafts-and-input": r"^desktop/(internal/draftstate/|draft_|frontend/src/.*(?:[Dd]raft|[Cc]omposer|[Ss]ubmission|[Ss]ession[Nn]avigation))",
        "inbox-and-receipts": r"^internal/(sessioninbox/|control/.*(?:submission|inbox|maintenance))",
        "attachments-and-identity": r"^internal/(attachment/|sqliteuri/|fileidentity/)|^desktop/attachments",
        "history-and-transcript": r"^internal/transcript/|^desktop/(history_|frontend/src/.*(?:[Hh]istory|[Tt]ranscript|[Ss]ubmission))",
        "native-shell": r"^desktop/electron/src/main/(lifecycle|graphics|window|index|icons)",
    }
    return [name for name, pattern in groups.items() if any(re.search(pattern, path) for path in paths)]


def main():
    baseline, main_tip = git("rev-parse", "HEAD"), git("rev-parse", "origin/main-v2")
    releases = []
    for patch in range(3, 12):
        tag = f"desktop-v1.38.{patch}"
        session = source(tag, "internal/session/store.go")
        revision = re.search(r"StorageRevision\s*=\s*(\d+)", session)
        registry = source(tag, "desktop/internal/workspacestate/store.go")
        draft = source(tag, "desktop/internal/draftstate/store.go")
        releases.append({"tag": tag, "commit": git("rev-parse", tag + "^{commit}"),
                         "framedStorageRevision": int(revision[1]) if revision else None,
                         "workspaceRegistry": bool(registry), "durableDrafts": bool(draft)})
    prs = []
    for line in git("log", "--first-parent", "--reverse", "--format=%H %s", "desktop-v1.38.3.." + main_tip).splitlines():
        commit, title = line.split(" ", 1)
        paths = git("diff", "--name-only", commit + "^1", commit).splitlines()
        domains = areas(paths)
        if not domains:
            continue
        number = re.search(r"#(\d+)", title)
        covered_by = next((release["tag"] for release in releases
                           if subprocess.run(["git", "merge-base", "--is-ancestor", commit, release["commit"]], cwd=ROOT).returncode == 0), "main-v2 after 1.38.11")
        prs.append({"commit": commit, "pr": int(number[1]) if number else None, "title": title,
                    "areas": domains, "firstRelease": covered_by,
                    "inRollbackBaseline": subprocess.run(["git", "merge-base", "--is-ancestor", commit, baseline], cwd=ROOT).returncode == 0})
    output = ROOT / "docs/testing/manual-session-compatibility-sources.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps({"rollbackBaseline": baseline, "testedMainV2": main_tip,
                                  "releases": releases, "relevantChanges": prs}, ensure_ascii=False, indent=2) + "\n")
    print(f"Pinned {len(releases)} releases and {len(prs)} relevant first-parent changes: {output}")


if __name__ == "__main__":
    main()
