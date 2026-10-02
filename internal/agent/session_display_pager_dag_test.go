package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func TestDisplayPagerDAGSelectedBranchMatchesNativeReplay(t *testing.T) {
	path := dagTestSession(t)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, at := dagLinearLog(t, path)
	patch, err := encodeSessionDAGMessage(dagMsg(provider.RoleAssistant, "patched answer", "A1"))
	if err != nil {
		t.Fatal(err)
	}
	system, err := encodeSessionDAGMessage(dagMsg(provider.RoleSystem, "fork system", "override"))
	if err != nil {
		t.Fatal(err)
	}
	dagAppend(t, path,
		sessionDAGEntry{Type: sessionDAGTypePatch, Target: "A1", Msgs: patch, At: at},
		sessionDAGEntry{Type: sessionDAGTypeFork, Head: SessionMainHead, NewHead: "fork", From: "A1", At: at.Add(5 * time.Second)},
		sessionDAGEntry{Type: sessionDAGTypeSystem, Head: "fork", Msgs: system, At: at},
		dagMessageEntry(t, "fork", "A1", "t2", dagMsg(provider.RoleUser, "fork question", "F1"), at.Add(6*time.Second)),
		sessionDAGEntry{Type: sessionDAGTypeSelect, Head: "fork", At: at},
	)
	original, err := os.ReadFile(store.SessionEventLog(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"", SessionMainHead, "fork"} {
		t.Run("head="+requested, func(t *testing.T) {
			p, err := OpenDisplayPager(t.Context(), path, filepath.Join(t.TempDir(), "dag.sqlite"), requested)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if !p.DAG || len(p.Header.Entries) != 0 {
				t.Fatal("DAG was not disk paged")
			}
			got, err := p.DAGMessages(0, p.Header.MessageCount)
			if err != nil {
				t.Fatal(err)
			}
			st := dagReplay(t, path)
			head := requested
			if head == "" {
				head = st.selectedHead()
			}
			want, _ := st.materialize(head)
			for i := range got {
				got[i].CreatedAt = want[i].CreatedAt
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("DAG differs from native selected view: got=%+v want=%+v", got, want)
			}
			turns, err := p.TurnEntries(1, 1000)
			if err != nil {
				t.Fatal(err)
			}
			wantIndex := BuildSessionDisplayIndex(want, 0, false, [32]byte{})
			var expected []DisplayIndexEntry
			for _, entry := range wantIndex.Entries {
				if entry.StartsTurn {
					expected = append(expected, entry)
				}
			}
			if len(turns) != len(expected) {
				t.Fatalf("outline count %d != selected branch %d", len(turns), len(expected))
			}
			for i, entry := range turns {
				if entry.Index != expected[i].Index || entry.AuthoredTurn != expected[i].AuthoredTurn {
					t.Fatalf("outline disagrees with branch replay: %+v != %+v", entry, expected[i])
				}
			}
		})
	}
	after, _ := os.ReadFile(store.SessionEventLog(path))
	if !reflect.DeepEqual(original, after) {
		t.Fatal("reading mutated the authoritative graph")
	}
}

func TestDisplayPagerDAGDamageDoesNotPublishPrefix(t *testing.T) {
	path := dagTestSession(t)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	dagLinearLog(t, path)
	f, err := os.OpenFile(store.SessionEventLog(path), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"schema_version\":2,"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if p, err := OpenDisplayPager(t.Context(), path, filepath.Join(t.TempDir(), "dag.sqlite")); err == nil {
		p.Close()
		t.Fatal("damaged prefix published as complete")
	}
}
