package cli

import (
	"context"
	"testing"
	"time"
)

// The catalog listing used to end in an unconditional 100-row cap, so a picker
// configured to show more than 100 conversations still stopped at the 100th
// newest — and because the resume search filters this same list, searching for
// anything older matched nothing at all.
func TestCanonicalResumeEntriesFromHonorsDisplayCap(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	catalog := newFakeSessionCatalog(250, func(i int) time.Time {
		return base.Add(time.Duration(i) * time.Minute)
	})

	cases := []struct {
		name string
		cap  int
		want int
	}{
		{"默认内置上限行为", canonicalResumeScanCap, canonicalResumeScanCap},
		{"配置放大到200", 200, 200},
		{"配置缩小到5", 5, 5},
		{"负值不限制", -1, 250},
		{"零值不限制", 0, 250},
	}
	for _, tc := range cases {
		entries := canonicalResumeEntriesFrom(context.Background(), catalog, tc.cap)
		if len(entries) != tc.want {
			t.Fatalf("%s: listed %d rows, want %d", tc.name, len(entries), tc.want)
		}
		if len(entries) == 0 {
			continue
		}
		// Newest first, whatever the cap is.
		for i := 1; i < len(entries); i++ {
			if entries[i].session.ModTime.After(entries[i-1].session.ModTime) {
				t.Fatalf("%s: rows not newest-first at %d", tc.name, i)
			}
		}
	}
}

// A cap larger than the walk budget still yields every row the walk reached, so
// a user asking for "no limit" is bounded by the catalog walk rather than
// silently losing the tail.
func TestCanonicalResumeEntriesFromCapBeyondCatalogKeepsAllWalked(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	total := 150
	catalog := newFakeSessionCatalog(total, func(i int) time.Time {
		return base.Add(time.Duration(i) * time.Minute)
	})

	entries := canonicalResumeEntriesFrom(context.Background(), catalog, -1)
	if len(entries) != total {
		t.Fatalf("listed %d rows, want all %d", len(entries), total)
	}
	if got := entries[0].target.ref.SessionID; got != "s00149" {
		t.Fatalf("newest row = %q, want s00149", got)
	}
}
