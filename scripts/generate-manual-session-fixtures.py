#!/usr/bin/env python3
"""Generate rollback fixtures with the tagged Desktop storage implementations.

Run from any directory. Historical source is transient; generated files contain
only synthetic identities and content. No user home or application is opened.
"""
import pathlib
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
DESKTOP = ROOT / "desktop"
OUTPUT = DESKTOP / "testdata" / "manual-session-upgrade"


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


for version, tag in [(v, "desktop-v" + v) for v in ("1.38.9", "1.38.10", "1.38.11")] + [
        ("main-v2-pr10469", "e8762acc9"), ("main-v2-pr10572", "54826e46a")]:
    output = OUTPUT / version
    output.mkdir(parents=True, exist_ok=True)
    temporary = pathlib.Path(tempfile.mkdtemp(prefix="rollbackfixture", dir=DESKTOP / "internal"))
    try:
        imports = ['"context"', '"os"', '"path/filepath"', 'currentstate "reasonix/desktop/internal/workspacestate"']
        prefix = "reasonix/desktop/internal/" + temporary.name
        for package in ("workspacestate", "draftstate"):
            source = "desktop/internal/" + package
            paths = git("ls-tree", "-r", "--name-only", tag, source).decode().splitlines()
            paths = [p for p in paths if str(pathlib.PurePosixPath(p).parent) == source
                     and p.endswith(".go") and not p.endswith("_test.go")]
            if not paths:
                continue
            destination = temporary / package
            destination.mkdir()
            for path in paths:
                (destination / pathlib.Path(path).name).write_bytes(git("show", tag + ":" + path))
            imports.append(f'{package} "{prefix}/{package}"')
        body = '''
ctx:=context.Background(); output:=os.Args[1]
registry:=workspacestate.NewStore(filepath.Join(output,"workspace-state.json"))
must(registry.EnsureWorkspace(ctx,workspacestate.Workspace{ID:"global",Root:"/fixture/workspace",Title:"Synthetic upgrade fixture",Visible:true}))
must(registry.AttachSession(ctx,"","global","existing-session",""))
must(registry.BeginCreate(ctx,workspacestate.PendingCreate{OperationID:"interrupted-create",WorkspaceID:"global",SessionID:"reserved-session"}))
// Upgrade a separate copy, then exercise the real tagged reader and writer.
roundtrip,err:=os.MkdirTemp("","reasonix-registry-roundtrip-");must(err);defer os.RemoveAll(roundtrip)
source,err:=os.ReadFile(filepath.Join(output,"workspace-state.json"));must(err)
checkPath:=filepath.Join(roundtrip,"workspace-state.json");must(os.WriteFile(checkPath,source,0600))
current:=currentstate.NewStore(checkPath);must(current.AttachSession(ctx,"","global","new-version-session",""))
old:=workspacestate.NewStore(checkPath);_,oldReadError:=old.Load(ctx)
'''
        if version == "1.38.9":
            body += 'if oldReadError==nil {panic("1.38.9 unexpectedly accepted registry v3")}\n'
        else:
            body += '''must(oldReadError)
must(old.AttachSession(ctx,"","global","continued-in-old-version",""))
reupgraded,err:=currentstate.NewStore(checkPath).Load(ctx);must(err)
found:=false;for _,id:=range reupgraded.Workspaces["global"].SessionIDs {if id=="continued-in-old-version" {found=true}}
if !found {panic("old-version mutation lost on reupgrade")}
'''
        if (temporary / "draftstate").exists():
            imports.append('"fmt"')
            body += '''
drafts:=draftstate.New(filepath.Join(output,"drafts.sqlite")); defer drafts.Close()
for _,phase:=range []string{"editable","settings-only","converted","reserved","starting","dispatching","dispatching_shell","dispatch_unknown","accepted","cancel_requested","resume_required","failed","cancelled"} {
 draft,_,err:=drafts.Open(ctx,"workspace-"+phase,"project","/fixture/"+phase,"draft-"+phase,`{"model":"fixture/model","modelSource":"explicit","mode":"normal","toolApprovalMode":"ask","disabledMcp":{},"mcpOrder":[]}`);must(err)
 if phase=="settings-only" {continue}
 revision:=draft.Revision
 draft,err=drafts.Save(ctx,draft.ID,revision,`{"text":"unsent upgrade fixture","attachments":[{"path":"missing-historical-file"}],"future":{"preserve":true}}`,draft.SettingsJSON,false);must(err)
 if phase=="editable" {_,err=drafts.Save(ctx,draft.ID,revision,`{"text":"losing historical writer"}`,draft.SettingsJSON,false);if err==nil {panic("expected conflict")};continue}
 frozen:=fmt.Sprintf(`{"snapshotVersion":%d,"draftId":%q,"expectedRevision":%d,"content":{"text":"frozen historical request"},"settings":{"model":"fixture/frozen","modelSource":"explicit","toolApprovalMode":"read-only"},"futureSnapshot":{"preserve":true}}`,draftstate.SnapshotVersion,draft.ID,draft.Revision)
 op,_,err:=drafts.BeginOperation(ctx,draftstate.Operation{ID:"operation-"+phase,DraftID:draft.ID,WorkspaceID:draft.WorkspaceID,DraftRevision:draft.Revision,SessionID:"session-"+phase,TopicID:"topic-"+phase,SubmissionID:"submit-"+phase,Fingerprint:"synthetic",RequestJSON:frozen});must(err)
 if phase=="converted" {_,err=drafts.SetOperationPhase(ctx,op.ID,"accepted","");must(err);must(drafts.Convert(ctx,draft.ID,op.ID))} else if phase!="reserved" {_,err=drafts.SetOperationPhase(ctx,op.ID,phase,"");must(err)}
}
'''
        (temporary / "main.go").write_text("package main\nimport (\n" + "\n".join(imports) + "\n)\nfunc must(err error){if err!=nil{panic(err)}}\nfunc main(){" + body + "}\n")
        # A fresh output avoids idempotently reopening an earlier fixture.
        for name in ("workspace-state.json", "drafts.sqlite", "drafts.sqlite-wal", "drafts.sqlite-shm"):
            (output / name).unlink(missing_ok=True)
        subprocess.run(["go", "run", "./internal/" + temporary.name, str(output)], cwd=DESKTOP, check=True)
        print(f"PASS {tag}: fixture generation and registry downgrade/reupgrade boundary", flush=True)
        (output / "SOURCE").write_text(tag + "\n" + git("rev-parse", tag + "^{commit}").decode())
        for lock in output.glob("*.lock"):
            lock.unlink()
    finally:
        shutil.rmtree(temporary)
