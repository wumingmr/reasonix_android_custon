#!/usr/bin/env python3
"""Verify that the previous registry implementation refuses v3 without rewriting it."""
import json
from pathlib import Path
import subprocess
import tempfile
import sys

root = Path(__file__).resolve().parents[2]
baseline = sys.argv[1] if len(sys.argv) > 1 else "4c8cec3b9f69f979c34769eb197908b7f549c578"
with tempfile.TemporaryDirectory(prefix="reasonix-registry-previous-") as temporary:
    checkout = Path(temporary)
    package = checkout / "internal/workspacestate"
    package.mkdir(parents=True)
    files = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", baseline,
                                     "desktop/internal/workspacestate"], cwd=root, text=True).splitlines()
    for name in files:
        if name.endswith(".go") and not name.endswith("_test.go"):
            (package / Path(name).name).write_bytes(subprocess.check_output(["git", "show", f"{baseline}:{name}"], cwd=root))
    manifest = (root / "desktop/go.mod").read_text().replace("replace reasonix => ../", f"replace reasonix => {root}")
    manifest = manifest.replace("=> ./third_party/systray", f"=> {root}/desktop/third_party/systray")
    (checkout / "go.mod").write_text(manifest)
    (checkout / "go.sum").write_bytes((root / "desktop/go.sum").read_bytes())
    (package / "previous_writer_test.go").write_text(r'''package workspacestate
import ("bytes"; "errors"; "os"; "path/filepath"; "testing")
func TestPreviousWriterRejectsV3(t *testing.T) {
 path := filepath.Join(t.TempDir(), "registry.json")
 body := []byte(`{"version":3,"generation":9,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","sessionIds":["a","b"],"organization":{"revision":2,"order":["ref\u0000local\u0000b","ref\u0000local\u0000a"],"futureField":"preserved"}}},"sessionStates":{},"futureRoot":true}`)
 if err := os.WriteFile(path, body, 0600); err != nil { t.Fatal(err) }
 s := NewStore(path)
 if _, err := s.Load(t.Context()); !errors.Is(err, ErrUnsupportedVersion) { t.Fatalf("read: %v", err) }
 if err := s.RenameWorkspace(t.Context(), "global", "old writer"); !errors.Is(err, ErrUnsupportedVersion) { t.Fatalf("write: %v", err) }
 after, err := os.ReadFile(path)
 if err != nil || !bytes.Equal(body,after) { t.Fatal("old writer modified v3 registry") }
}
''')
    result = subprocess.run(["go", "test", "./internal/workspacestate", "-run", "TestPreviousWriterRejectsV3", "-count=1"],
                            cwd=checkout, text=True, capture_output=True)
    print(result.stdout, end="")
    print(result.stderr, end="", file=sys.stderr)
    evidence = {"baseline": baseline, "passed": result.returncode == 0,
                "scope": "actual baseline registry reader and writer; v3 bytes unchanged",
                "stdout": result.stdout, "stderr": result.stderr}
    (Path(tempfile.gettempdir()) / "reasonix-independent-registry-compatibility.json").write_text(json.dumps(evidence, indent=2))
    sys.exit(result.returncode)
