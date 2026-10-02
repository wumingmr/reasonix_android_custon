#!/usr/bin/env python3
"""Run each release's real Go writers in an isolated checkout (no user data).

The generated histories, event sidecars and content objects are synthetic.
Unlike schema-shaped JSON fixtures, these exercise the actual tagged encoder,
checksums, framing and externalization. Requires the historical Go dependencies.
"""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "desktop/testdata/session-history-upgrade"
TEMPLATE = ROOT / "scripts/fixtures/session-history.go.txt"


def redact_writer_hosts(output):
    # Writer announcements are environment metadata, not conversation content.
    # Preserve byte lengths so tagged event-index offsets stay valid.
    for path in output.rglob("*.events.jsonl"):
        lines = []
        for line in path.read_bytes().splitlines(keepends=True):
            record = json.loads(line)
            if record.get("type") == "writer":
                line = re.sub(rb'("hostname":")([^"\\]*)(")',
                              lambda match: match[1] + b"fixture-host".ljust(len(match[2]), b"-")[:len(match[2])] + match[3], line)
            lines.append(line)
        path.write_bytes(b"".join(lines))


def generate(version, writers_dir, build_only=False):
    tag = "desktop-v" + version
    commit = subprocess.check_output(["git", "rev-parse", tag + "^{commit}"], cwd=ROOT, text=True).strip()
    with tempfile.TemporaryDirectory(prefix="reasonix-tagged-history-") as temporary:
        checkout = pathlib.Path(temporary) / "source"
        checkout.mkdir()
        archive = pathlib.Path(temporary) / "source.tar"
        with archive.open("wb") as destination:
            subprocess.run(["git", "archive", tag, "go.mod", "go.sum", "internal", "sdk"], cwd=ROOT, stdout=destination, check=True)
        subprocess.run(["tar", "-xf", str(archive), "-C", str(checkout)], check=True)
        # The encoder remains the tagged implementation. Supply a synthetic
        # process identity before its first write so fixtures contain no host name.
        (checkout / "internal/agent/rollback_fixture_identity.go").write_text(
            'package agent\nfunc init() { sessionWriterID = "historical-fixture-writer" }\n')
        program = checkout / "cmd/rollback-history-fixture"
        program.mkdir(parents=True)
        source = TEMPLATE.read_text()
        canonical = int(version.rsplit(".", 1)[1]) >= 8
        source = source.replace("// CANONICAL_IMPORT", '"context"; "errors"; "reasonix/internal/session"' if canonical else "")
        start, end = source.index("// CANONICAL_START"), source.index("// CANONICAL_END")
        if not canonical:
            source = source[:start] + source[end:]
        source = source.replace("// CANONICAL_CALL", "writeCanonical(output, messages)" if canonical else "")
        source = source.replace("// CONTINUE_CALL", 'if len(os.Args)>2 { continueCanonical(os.Args[1],os.Args[2]); return }' if canonical else "")
        source = source.replace("// FORMAL_CALL", '''
    service,err:=session.NewService("desktop",session.NewFilesystemPersistence(filepath.Join(output,"formal")));must(err)
    runtime,err:=service.Create(ctx,session.CreateOptions{SessionID:"formal-session",CWD:"/synthetic/workspace",Origin:session.SessionOriginNew});must(err)
    var events []session.Event
    for _,message:=range messages {events=append(events,session.Event{Kind:"message/complete",Payload:body(map[string]any{"message":message})})}
    _,err=runtime.Session().Append(ctx,session.Batch{OperationID:"formal-history",Events:events});must(err)
    must(service.Close(ctx,runtime.Ref()))
''' if int(version.rsplit(".", 1)[1]) >= 9 else "")
        (program / "main.go").write_text(source)
        output = OUTPUT / version
        # Old writers can consult config paths. Keep every such path disposable.
        isolated_home = pathlib.Path(temporary) / "home"
        env = dict(os.environ, REASONIX_HOME=str(isolated_home), REASONIX_STATE_HOME=str(isolated_home), REASONIX_CACHE_HOME=str(isolated_home / "cache"))
        executable = pathlib.Path(writers_dir).resolve() / version if writers_dir else pathlib.Path(temporary) / "writer"
        executable.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["go", "build", "-o", str(executable), "./cmd/rollback-history-fixture"], cwd=checkout, env=env, check=True)
        if build_only:
            print(f"PASS {tag}: built historical reader/writer {executable}", flush=True)
            return
        if output.exists():
            shutil.rmtree(output)
        output.mkdir(parents=True)
        subprocess.run([str(executable), str(output)], cwd=checkout, env=env, check=True)
        redact_writer_hosts(output)
        # Locks/caches are rebuildable, not part of the durable fixture.
        for path in list(output.rglob("*")):
            if path.is_file() and (path.name.endswith((".lock", ".lease")) or ".query-cache" in path.parts):
                path.unlink()
        files = {str(path.relative_to(output)): hashlib.sha256(path.read_bytes()).hexdigest()
                 for path in sorted(output.rglob("*")) if path.is_file()}
        (output / "SOURCE.json").write_text(json.dumps({"tag": tag, "commit": commit,
            "normalizations": ["synthetic writer identity", "length-preserving writer hostname"], "files": files}, indent=2) + "\n")
        print(f"PASS {tag} ({commit[:12]}): historical writer, {len(files)} durable files", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--writers-dir", help="retain isolated writer executables for previous-reader roundtrip tests")
    parser.add_argument("--build-only", action="store_true", help="build old readers without changing existing fixture files")
    parser.add_argument("versions", nargs="*", default=[f"1.38.{n}" for n in range(3, 12)])
    args = parser.parse_args()
    if args.build_only and not args.writers_dir:
        parser.error("--build-only requires --writers-dir")
    for version in args.versions:
        generate(version, args.writers_dir, args.build_only)
