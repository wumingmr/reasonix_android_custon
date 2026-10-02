package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// Measures the existing runtime-lock scope as well as elapsed deletion time.
// All content and registry writes are confined to the benchmark's temporary root.
func BenchmarkPurgeRuntimeLock(b *testing.B) {
	for _, count := range []int{1, 8} {
		b.Run(fmt.Sprintf("sessions-%d", count), func(b *testing.B) {
			b.StopTimer()
			root := b.TempDir()
			var held time.Duration
			for n := range b.N {
				a := NewApp()
				a.ctx = b.Context()
				a.desktopSessions.root = filepath.Join(root, fmt.Sprint(n), "sessions")
				store := workspacestate.NewStore(filepath.Join(root, fmt.Sprint(n), "registry.json"))
				a.desktopSessions.workspaceState = store
				if err := store.EnsureWorkspace(b.Context(), workspacestate.Workspace{ID: workspacestate.GlobalWorkspaceID, Visible: true}); err != nil {
					b.Fatal(err)
				}
				refs := make([]session.SessionRef, count)
				for i := range refs {
					id := fmt.Sprintf("benchmark-%d", i)
					refs[i] = session.SessionRef{HostID: localDesktopHostID, SessionID: id}
					dir := filepath.Join(a.desktopSessions.root, id)
					if err := os.MkdirAll(dir, 0700); err != nil {
						b.Fatal(err)
					}
					for j := range 128 / count {
						if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(j)), make([]byte, 4096), 0600); err != nil {
							b.Fatal(err)
						}
					}
					if err := store.AttachSession(b.Context(), "", workspacestate.GlobalWorkspaceID, id, ""); err != nil {
						b.Fatal(err)
					}
					if err := store.ArchiveSession(b.Context(), id); err != nil {
						b.Fatal(err)
					}
				}
				state, err := store.Load(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				// Keep service construction outside the measured runtime lock.
				a.desktopSessionService("")
				b.StartTimer()
				for _, ref := range refs {
					release := a.lockRuntimeMutation("benchmark purge")
					start := time.Now()
					err := a.purgeCanonicalSession(b.Context(), ref, state.Generation)
					held += time.Since(start)
					release()
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				a.closeSessionServices()
			}
			b.ReportMetric(float64(held.Nanoseconds())/float64(b.N), "lock-ns/op")
		})
	}
}
