package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"reasonix/internal/sessioncatalog"
)

type catalogWatchProbe struct {
	catalog   *sessioncatalog.Catalog
	requested atomic.Int32
	started   atomic.Int32
	completed atomic.Int32
}

// openCatalogWatchProbe opens an on-disk advisory catalog over dir. Every
// discovery "started" is one beginDirectoryScan, i.e. one scan_generation step.
func openCatalogWatchProbe(t *testing.T, dir string) (*catalogWatchProbe, sessioncatalog.DirectoryTarget) {
	t.Helper()
	probe := &catalogWatchProbe{}
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{
		Path: filepath.Join(t.TempDir(), "v10.sqlite"), MetadataOnly: true, StartPaused: true,
		OnDiscovery: func(event sessioncatalog.DiscoveryEvent) {
			switch event.Phase {
			case "requested":
				probe.requested.Add(1)
			case "started":
				probe.started.Add(1)
			case "completed":
				probe.completed.Add(1)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	probe.catalog = catalog
	target := sessioncatalog.DirectoryTarget{Path: dir, Scope: "project", WorkspaceRoot: filepath.Dir(dir)}
	done, accepted := catalog.ScheduleReconcile(target)
	if !accepted {
		t.Fatal("initial discovery rejected")
	}
	catalog.ResumeDiscovery()
	select {
	case <-done:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("initial discovery did not finish")
	}
	return probe, target
}

func (p *catalogWatchProbe) settle(t *testing.T) {
	t.Helper()
	// Requests dispatch asynchronously, so settled means quiet for a while.
	deadline := time.Now().Add(sessionCatalogTestDeadline)
	quietSince, last := time.Now(), p.started.Load()
	for {
		started := p.started.Load()
		if started != last || p.completed.Load() < started {
			quietSince, last = time.Now(), started
		} else if time.Since(quietSince) > 300*time.Millisecond {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery did not settle: started=%d completed=%d", started, p.completed.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Reported state (#11693): an unchanged project directory of legacy rows
// (log_format=1, turns unknown, repair pending at zero attempts) beside files
// that are not transcripts and that other writers keep touching.
func writeLegacyCatalogDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(filepath.Join(dir, "s0.checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s%d.jsonl", i)), []byte("{\"role\":\"user\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCatalogWatchNonSessionChurnDoesNotRescanUnchangedRoot(t *testing.T) {
	dir := writeLegacyCatalogDirectory(t)
	probe, target := openCatalogWatchProbe(t, dir)
	record, found, err := probe.catalog.GetSession(t.Context(), filepath.Join(dir, "s0.jsonl"))
	if err != nil || !found || record.LogFormat != 1 || record.TurnsState != sessioncatalog.TurnsUnknown {
		t.Fatalf("fixture is not the reported legacy row state: %+v found=%v err=%v", record, found, err)
	}
	key := canonicalWorkspaceRoot(dir)
	targets := map[string]sessioncatalog.DirectoryTarget{key: target}
	watched, dirty := map[string]bool{key: true}, map[string]bool{}
	pacer := &catalogRootPacer{}
	baseRequested, baseStarted := probe.requested.Load(), probe.started.Load()
	churn := []fsnotify.Event{
		{Name: filepath.Join(key, ".titles.json.tmp"), Op: fsnotify.Create},
		{Name: filepath.Join(key, ".titles.json.tmp"), Op: fsnotify.Rename},
		{Name: filepath.Join(key, ".display.json"), Op: fsnotify.Write},
		{Name: filepath.Join(key, "s0.wire.jsonl"), Op: fsnotify.Write},
		{Name: filepath.Join(key, "s0.context.json"), Op: fsnotify.Write},
		{Name: filepath.Join(key, "s0.checkpoints"), Op: fsnotify.Write},
		{Name: filepath.Join(key, "writer.lock"), Op: fsnotify.Create},
		{Name: filepath.Join(key, "writer.lock"), Op: fsnotify.Remove},
	}
	now := time.Now()
	for cycle := range 20 {
		for _, event := range churn {
			admitCatalogWatchEvent(probe.catalog, event, targets, watched, dirty, nil, false, nil)
		}
		admitCatalogWatchBatch(t.Context(), probe.catalog, targets, dirty, false, pacer, now.Add(time.Duration(cycle)*3*time.Second))
	}
	probe.settle(t)
	if requested, started := probe.requested.Load()-baseRequested, probe.started.Load()-baseStarted; requested != 0 || started != 0 || len(dirty) != 0 {
		t.Fatalf("unchanged root rescanned on non-session churn: requested=%d scans=%d dirty=%v", requested, started, dirty)
	}
}

func TestCatalogWatchSessionSourcesStillReachTheCatalog(t *testing.T) {
	dir := writeLegacyCatalogDirectory(t)
	probe, target := openCatalogWatchProbe(t, dir)
	key := canonicalWorkspaceRoot(dir)
	targets := map[string]sessioncatalog.DirectoryTarget{key: target}
	watched, dirty := map[string]bool{key: true}, map[string]bool{}
	for _, name := range []string{"s1.jsonl", "s1.jsonl.meta", "s1.events.jsonl"} {
		admitCatalogWatchEvent(probe.catalog, fsnotify.Event{Name: filepath.Join(key, name), Op: fsnotify.Write}, targets, watched, dirty, nil, false, nil)
		if len(dirty) != 0 {
			t.Fatalf("%s write scheduled a root scan instead of an exact index: %v", name, dirty)
		}
	}
	admitCatalogWatchEvent(probe.catalog, fsnotify.Event{Name: filepath.Join(key, "s1.events.jsonl"), Op: fsnotify.Write}, targets, watched, dirty, nil, true, nil)
	if !dirty[key] {
		t.Fatal("pre-discovery session event lost its root invalidation")
	}
	clear(dirty)
	admitCatalogWatchEvent(probe.catalog, fsnotify.Event{Name: filepath.Join(key, "s2.jsonl"), Op: fsnotify.Remove}, targets, watched, dirty, nil, false, nil)
	if !dirty[key] {
		t.Fatal("transcript removal must reconcile the root to mark the row missing")
	}
}

func TestCatalogWatchRootPacingConvergesUnderPersistentTrigger(t *testing.T) {
	dir := writeLegacyCatalogDirectory(t)
	probe, target := openCatalogWatchProbe(t, dir)
	key := canonicalWorkspaceRoot(dir)
	targets := map[string]sessioncatalog.DirectoryTarget{key: target}
	dirty := map[string]bool{}
	pacer := &catalogRootPacer{}
	baseRequested := probe.requested.Load()
	start := time.Now()
	// A root-level invalidation every 3s for ten minutes: the shape of the
	// reported flapping row, whatever produces it.
	var last time.Time
	for tick := range 200 {
		now := start.Add(time.Duration(tick) * 3 * time.Second)
		dirty[key] = true
		before := probe.requested.Load()
		next := admitCatalogWatchBatch(t.Context(), probe.catalog, targets, dirty, false, pacer, now)
		if probe.requested.Load() != before {
			last = now
		} else if !dirty[key] || next.IsZero() || !next.After(now) {
			t.Fatalf("deferred root lost its invalidation or due time: dirty=%v next=%v", dirty, next)
		}
	}
	probe.settle(t)
	admissions := probe.requested.Load() - baseRequested
	if admissions > 20 {
		t.Fatalf("persistent trigger was not backed off: %d full scans in ten minutes", admissions)
	}
	if admissions < 5 {
		t.Fatalf("pacing starved a dirty root: %d full scans in ten minutes", admissions)
	}
	// A root quiet for longer than the backoff window is admitted at once again.
	quiet := last.Add(3 * catalogRootPaceMax)
	dirty[key] = true
	before := probe.requested.Load()
	admitCatalogWatchBatch(t.Context(), probe.catalog, targets, dirty, false, pacer, quiet)
	if probe.requested.Load() == before || dirty[key] {
		t.Fatal("a settled root must be admitted without inherited backoff")
	}
}

func TestCatalogWatchSessionRemovalBypassesChurnBackoff(t *testing.T) {
	dir := writeLegacyCatalogDirectory(t)
	probe, target := openCatalogWatchProbe(t, dir)
	key := canonicalWorkspaceRoot(dir)
	targets := map[string]sessioncatalog.DirectoryTarget{key: target}
	watched, dirty := map[string]bool{key: true}, map[string]bool{}
	pacer := &catalogRootPacer{}
	now := time.Now()
	// Drive the root to the backoff cap with root-level invalidations.
	for range 40 {
		now = now.Add(3 * time.Second)
		dirty[key] = true
		admitCatalogWatchBatch(t.Context(), probe.catalog, targets, dirty, false, pacer, now)
	}
	if due := pacer.due(key); due.Sub(now) > catalogRootPaceMax {
		t.Fatalf("root-level change deferred past the documented maximum: %v", due.Sub(now))
	}
	if dirty[key] && !now.Add(catalogRootPaceMax).After(pacer.due(key)) {
		t.Fatal("deferred root has no due time within the maximum")
	}
	if err := os.Remove(filepath.Join(dir, "s3.jsonl")); err != nil {
		t.Fatal(err)
	}
	admitCatalogWatchEvent(probe.catalog, fsnotify.Event{Name: filepath.Join(key, "s3.jsonl"), Op: fsnotify.Remove}, targets, watched, dirty, nil, false, pacer)
	before := probe.requested.Load()
	now = now.Add(time.Millisecond)
	admitCatalogWatchBatch(t.Context(), probe.catalog, targets, dirty, false, pacer, now)
	if probe.requested.Load() == before || dirty[key] {
		t.Fatal("a removed transcript waited behind unrelated churn backoff")
	}
	probe.settle(t)
	if record, found, err := probe.catalog.GetSession(t.Context(), filepath.Join(dir, "s3.jsonl")); err != nil || (found && record.Health != sessioncatalog.HealthMissing) {
		t.Fatalf("removed transcript still listed as present: %+v found=%v err=%v", record, found, err)
	}
	// The expedited scan must not reset or grow the churn backoff either way.
	dirty[key] = true
	if next := admitCatalogWatchBatch(t.Context(), probe.catalog, targets, dirty, false, pacer, now.Add(time.Second)); next.IsZero() || next.Sub(now) > catalogRootPaceMax {
		t.Fatalf("churn after an expedited scan lost its bounded backoff: next=%v", next)
	}
}
