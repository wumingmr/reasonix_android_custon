package main

import (
	"encoding/json"
	"testing"
)

// singleSurfaceTabsFile rewrites the persisted layout. Every branch must carry
// the forward-compatibility keys of a newer build through untouched; the
// local-kept branch dropped them, so a downgrade-then-upgrade cycle lost them
// whenever a remote surface was active.
func TestSingleSurfaceLocalKeptBranchPreservesUnknownKeys(t *testing.T) {
	extra := map[string]json.RawMessage{"futureKey": json.RawMessage(`{"a":1}`)}
	out := singleSurfaceTabsFile(desktopTabsFile{
		Tabs:       []desktopTabEntry{{ID: "l1"}, {ID: "l2"}},
		RemoteTabs: []desktopRemoteTabEntry{{ID: "r1", HostID: "h", Workspace: "~/a"}},
		ActiveTab:  "r1",
		extra:      extra,
	})
	if len(out.Tabs) != 1 || out.ActiveTab != "r1" {
		t.Fatalf("collapse = %+v, want the remote surface with one dormant local tab", out)
	}
	if string(out.extra["futureKey"]) != `{"a":1}` {
		t.Fatalf("collapsed extra = %v, want the forward-compatible key retained", out.extra)
	}
	extra["futureKey"] = json.RawMessage(`{"a":2}`)
	if string(out.extra["futureKey"]) != `{"a":1}` {
		t.Fatal("collapsed extra aliases the caller's map instead of cloning it")
	}
}
