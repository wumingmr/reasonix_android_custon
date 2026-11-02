package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func createCanonicalTestSession(t *testing.T, v4root, id, prompt string) {
	t.Helper()
	persistence := session.NewFilesystemPersistence(v4root)
	sess, err := persistence.Create(session.CreateOptions{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "" {
		messagePayload, err := json.Marshal(map[string]any{
			"message": provider.Message{ID: id + "-m1", Role: provider.RoleUser, Content: prompt},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.Append(context.Background(), session.Batch{OperationID: "seed", TurnID: id + "-turn", Events: []session.Event{
			{Kind: "message/complete", Payload: messagePayload},
			{Kind: "turn/start"},
			{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sess.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func waitForCatalogMetadata(t *testing.T, sessionDir string, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	service := cliSessionService(sessionDir)
	if service == nil {
		t.Fatal("no session service for test workspace")
	}
	for time.Now().Before(deadline) {
		page, err := service.Query().List(context.Background(), "", 100)
		if err != nil {
			t.Fatal(err)
		}
		ready := map[string]bool{}
		for _, info := range page.Sessions {
			ready[info.SessionID] = info.MetadataStatus == session.MetadataReady
		}
		all := true
		for _, id := range ids {
			if !ready[id] {
				all = false
				break
			}
		}
		if all {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("catalog metadata for %v did not become ready", ids)
}

func writeTestMigrationMap(t *testing.T, v4root, sourcePath, targetID string) {
	t.Helper()
	mapping := session.MigrationMapping{SchemaVersion: session.SchemaVersion, Entries: []session.MigrationEntry{{
		SourcePath: sourcePath, TargetCodec: session.Codec, TargetID: targetID,
	}}}
	data, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v4root, "migration-map.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMergedResumeEntriesUnifiesStores proves the picker offers the same
// conversations the desktop tree shows: final-format catalog rows appear,
// never-chatted canonical placeholders stay hidden, and a legacy source with
// exactly one canonical successor is folded into that successor.
func TestMergedResumeEntriesUnifiesStores(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	v4root := filepath.Join(dir, "sessions-v4")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}

	migrated := saveQueryTestSession(t, sessionDir, "migrated-source.jsonl", "old conversation")
	fresh := saveQueryTestSession(t, sessionDir, "fresh-legacy.jsonl", "fresh legacy conversation")
	createCanonicalTestSession(t, v4root, "canchat1", "canonical hello")
	createCanonicalTestSession(t, v4root, "canempty1", "")
	writeTestMigrationMap(t, v4root, migrated, "canchat1")
	waitForCatalogMetadata(t, sessionDir, "canchat1", "canempty1")

	entries := mergedResumeEntries(sessionDir, resumeListCap)
	var sawFresh, sawMigrated, sawChatted, sawEmpty bool
	for _, entry := range entries {
		switch {
		case entry.session.Path == fresh:
			sawFresh = true
			if entry.target.canonical() || entry.target.path != fresh {
				t.Fatalf("legacy row carries wrong target: %+v", entry.target)
			}
		case entry.session.Path == migrated:
			sawMigrated = true
		case entry.target.ref.SessionID == "canchat1":
			sawChatted = true
			if entry.session.Turns != 1 || entry.session.Preview != "canonical hello" {
				t.Fatalf("canonical row = %+v", entry.session)
			}
		case entry.target.ref.SessionID == "canempty1":
			sawEmpty = true
		}
	}
	if !sawFresh {
		t.Fatal("fresh legacy session missing from merged picker list")
	}
	if !sawChatted {
		t.Fatal("chatted canonical session missing from merged picker list")
	}
	if sawMigrated {
		t.Fatal("migrated legacy source still listed beside its canonical successor")
	}
	if sawEmpty {
		t.Fatal("never-chatted canonical session listed")
	}
}

func TestCanonicalResumeHidden(t *testing.T) {
	if !canonicalResumeHidden(session.SessionInfo{MetadataStatus: session.MetadataReady}) {
		t.Fatal("ready empty session should be hidden")
	}
	if canonicalResumeHidden(session.SessionInfo{MetadataStatus: session.MetadataReady, Turns: 2}) {
		t.Fatal("session with turns should stay listed")
	}
	if canonicalResumeHidden(session.SessionInfo{MetadataStatus: session.MetadataReady, Preview: "hi"}) {
		t.Fatal("session with preview should stay listed")
	}
	if canonicalResumeHidden(session.SessionInfo{MetadataStatus: session.MetadataPending}) {
		t.Fatal("pending metadata must stay listed while the rebuild runs")
	}
}

// TestMergedResumeEntriesCapsAcrossStores keeps the picker cap honest when
// both stores contribute rows: the cap applies to the merged stream, not per
// store.
func TestMergedResumeEntriesCapsAcrossStores(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	v4root := filepath.Join(dir, "sessions-v4")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		saveQueryTestSession(t, sessionDir, "legacy-"+string(rune('a'+i))+"-session.jsonl", "legacy prompt "+string(rune('a'+i)))
	}
	var ids []string
	for i := range 4 {
		id := "bbbb" + string(rune('0'+i)) + "cccc"
		createCanonicalTestSession(t, v4root, id, "canonical prompt "+string(rune('0'+i)))
		ids = append(ids, id)
	}
	waitForCatalogMetadata(t, sessionDir, ids...)

	entries := mergedResumeEntries(sessionDir, 5)
	if len(entries) > 5+1 {
		t.Fatalf("merged list kept %d entries above the cap", len(entries))
	}
	if len(entries) == 0 {
		t.Fatal("merged list is empty")
	}
	newest := time.Time{}
	for _, entry := range entries {
		if entry.session.ModTime.After(newest) {
			newest = entry.session.ModTime
		}
	}
	if newest.IsZero() {
		t.Fatal("merged rows carry no recency stamps")
	}
}

func resumeStoreLegacyRow(name string, at time.Time) agent.SessionInfo {
	return agent.SessionInfo{Path: "/sessions/" + name + ".jsonl", ModTime: at, Preview: name, Turns: 1}
}

func resumeStoreCanonicalRow(id string, at time.Time) resumeEntry {
	return resumeEntry{
		session: agent.SessionInfo{Path: "/sessions-v4/" + id, ModTime: at, Preview: id, Turns: 1},
		target:  cliResumeTarget{ref: session.SessionRef{HostID: "local", SessionID: id}},
	}
}

func resumeEntryPreviews(entries []resumeEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.session.Preview)
	}
	return out
}

// TestMergeResumeStoresOrdersByRecency pins the interleave contract: both
// stores arrive newest-first and a canonical row is emitted ahead of every
// legacy run it is newer than. An inverted comparison pushed newer canonical
// rows behind the whole legacy list, where the display cap dropped them and a
// desktop-created session vanished from /resume, the picker, and /takeover <n>.
func TestMergeResumeStoresOrdersByRecency(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	legacy := []agent.SessionInfo{resumeStoreLegacyRow("L10", at(10)), resumeStoreLegacyRow("L5", at(5))}
	cases := []struct {
		name      string
		canonical []resumeEntry
		want      []string
	}{
		{"newer canonical row leads", []resumeEntry{resumeStoreCanonicalRow("C12", at(12))}, []string{"C12", "L10", "L5"}},
		{"canonical row slots between legacy rows", []resumeEntry{resumeStoreCanonicalRow("C7", at(7))}, []string{"L10", "C7", "L5"}},
		{"older canonical row trails", []resumeEntry{resumeStoreCanonicalRow("C1", at(1))}, []string{"L10", "L5", "C1"}},
		{"tie keeps the legacy row first", []resumeEntry{resumeStoreCanonicalRow("C10", at(10))}, []string{"L10", "C10", "L5"}},
		{"several canonical rows keep their own order", []resumeEntry{
			resumeStoreCanonicalRow("C12", at(12)), resumeStoreCanonicalRow("C7", at(7)), resumeStoreCanonicalRow("C6", at(6)),
		}, []string{"C12", "L10", "C7", "C6", "L5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resumeEntryPreviews(mergeResumeStores(legacy, tc.canonical, resumeListCap))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("merged order = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMergeResumeStoresCapKeepsNewestCanonicalRow proves the display cap trims
// the oldest rows, not the canonical store: with a full page of legacy
// transcripts the newest conversation stays listed when it is a catalog row.
func TestMergeResumeStoresCapKeepsNewestCanonicalRow(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var legacy []agent.SessionInfo
	for i := range resumeListCap {
		minute := 20 - i
		legacy = append(legacy, resumeStoreLegacyRow("L"+strconv.Itoa(minute), base.Add(time.Duration(minute)*time.Minute)))
	}
	canonical := []resumeEntry{resumeStoreCanonicalRow("C21", base.Add(21*time.Minute))}

	got := resumeEntryPreviews(mergeResumeStores(legacy, canonical, resumeListCap))
	if len(got) != resumeListCap {
		t.Fatalf("merged list has %d rows, want the cap of %d", len(got), resumeListCap)
	}
	if got[0] != "C21" {
		t.Fatalf("newest row = %q, want the canonical row C21 (order %v)", got[0], got)
	}
	if got[len(got)-1] != "L12" {
		t.Fatalf("cap dropped %q instead of the oldest legacy row (order %v)", got[len(got)-1], got)
	}
}

// fakeSessionCatalog pages a synthetic catalog in session-id order with the
// same cursor contract as FilesystemPersistence.List.
type fakeSessionCatalog struct {
	infos []session.SessionInfo
	calls int
}

func (f *fakeSessionCatalog) List(_ context.Context, cursor string, limit int) (session.SessionPage, error) {
	f.calls++
	page := session.SessionPage{Sessions: []session.SessionInfo{}}
	for _, info := range f.infos {
		if info.SessionID <= cursor {
			continue
		}
		if len(page.Sessions) == limit {
			page.NextCursor = page.Sessions[len(page.Sessions)-1].SessionID
			break
		}
		page.Sessions = append(page.Sessions, info)
	}
	return page, nil
}

func newFakeSessionCatalog(rows int, updatedAt func(i int) time.Time) *fakeSessionCatalog {
	catalog := &fakeSessionCatalog{}
	for i := range rows {
		id := fmt.Sprintf("s%05d", i)
		catalog.infos = append(catalog.infos, session.SessionInfo{
			SessionID: id, Ref: session.SessionRef{HostID: "local", SessionID: id}, Codec: session.Codec,
			MetadataStatus: session.MetadataReady, Turns: 1, Preview: id, UpdatedAt: updatedAt(i), Path: "/sessions-v4/" + id,
		})
	}
	return catalog
}

// TestCanonicalResumeEntriesRankNewestAcrossCatalogPages proves the listing
// walks every catalog page before ranking: with more than one page of
// sessions whose newest activity sorts last by id, the newest rows are the
// ones offered (and therefore the ones --continue picks), not the first page
// in lexical order.
func TestCanonicalResumeEntriesRankNewestAcrossCatalogPages(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	catalog := newFakeSessionCatalog(250, func(i int) time.Time { return base.Add(time.Duration(i) * time.Minute) })

	entries := canonicalResumeEntriesFrom(context.Background(), catalog)

	if len(entries) != canonicalResumeScanCap {
		t.Fatalf("listed %d rows, want the display cap of %d", len(entries), canonicalResumeScanCap)
	}
	if got := entries[0].target.ref.SessionID; got != "s00249" {
		t.Fatalf("newest row = %q, want s00249 from the last catalog page", got)
	}
	if got := entries[len(entries)-1].target.ref.SessionID; got != "s00150" {
		t.Fatalf("oldest offered row = %q, want s00150", got)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].session.ModTime.After(entries[i-1].session.ModTime) {
			t.Fatalf("rows are not newest-first at %d: %v after %v", i, entries[i].session.ModTime, entries[i-1].session.ModTime)
		}
	}
	if catalog.calls != 3 {
		t.Fatalf("catalog pages walked = %d, want 3 (250 rows at %d per page)", catalog.calls, canonicalResumeScanCap)
	}
}

// TestCanonicalResumeEntriesBoundTheCatalogWalk pins the documented hard cap:
// a store larger than canonicalResumeWalkCap stops paging instead of scanning
// every directory on each /resume.
func TestCanonicalResumeEntriesBoundTheCatalogWalk(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	catalog := newFakeSessionCatalog(canonicalResumeWalkCap+canonicalResumeScanCap, func(int) time.Time { return base })

	entries := canonicalResumeEntriesFrom(context.Background(), catalog)

	if want := canonicalResumeWalkCap / canonicalResumeScanCap; catalog.calls != want {
		t.Fatalf("catalog pages walked = %d, want %d (walk cap %d)", catalog.calls, want, canonicalResumeWalkCap)
	}
	if len(entries) != canonicalResumeScanCap {
		t.Fatalf("listed %d rows, want the display cap of %d", len(entries), canonicalResumeScanCap)
	}
}

// TestOtherProjectResumeRowsUseMigrationMapWithoutForeignService proves the
// cross-project rows come from the foreign workspace's migration map alone: a
// migrated source is hidden, the live transcript is offered, and no session
// service is opened (and cached for the process lifetime) for that root.
func TestOtherProjectResumeRowsUseMigrationMapWithoutForeignService(t *testing.T) {
	currentDir := t.TempDir()
	otherRoot := t.TempDir()
	otherDir := config.ProjectSessionDir(otherRoot)
	if otherDir == "" {
		t.Skip("project session dir unavailable")
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectsFile := filepath.Join(config.ReasonixHomeDir(), "desktop-projects.json")
	previous, readErr := os.ReadFile(projectsFile)
	t.Cleanup(func() {
		if readErr != nil {
			os.Remove(projectsFile)
			return
		}
		_ = os.WriteFile(projectsFile, previous, 0o644)
	})
	if err := os.WriteFile(projectsFile, []byte(`{"projects":[{"root":`+strconv.Quote(filepath.ToSlash(otherRoot))+`}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	migrated := saveQueryTestSession(t, otherDir, "migrated-source.jsonl", "migrated work")
	fresh := saveQueryTestSession(t, otherDir, "fresh-legacy.jsonl", "fresh work")
	v4root := session.RootForLegacyDir(otherDir)
	if err := os.MkdirAll(filepath.Join(v4root, "target0001"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestMigrationMap(t, v4root, migrated, "target0001")

	entries := otherProjectResumeEntries(currentDir)

	var foreign []resumeEntry
	for _, entry := range entries {
		if entry.project == filepath.Base(otherRoot) {
			foreign = append(foreign, entry)
		}
	}
	if len(foreign) != 1 || foreign[0].session.Path != fresh || foreign[0].target.canonical() {
		t.Fatalf("foreign project rows = %+v, want exactly the live transcript %q", foreign, fresh)
	}
	cliSessionServices.Lock()
	_, opened := cliSessionServices.byRoot[v4root]
	cliSessionServices.Unlock()
	if opened {
		t.Fatalf("listing another project opened a session service for %s", v4root)
	}
}
