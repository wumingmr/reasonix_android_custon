package sessioncatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeferredAuditDoesNotBlockPageAndClosesEveryLeaseOnCorruption(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true,
		DeferredMetadataIntegrity: true, StartPaused: true, RevisionFloor: 90,
		verifyMetadata: func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				return errors.New("projection integrity check: damaged index")
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if c.Status().Revision < 90 {
		t.Fatal("replacement lost the revision fence")
	}
	select {
	case <-entered:
		t.Fatal("audit ignored discovery admission")
	default:
	}
	c.ResumeDiscovery()
	<-entered
	lease, err := c.OpenReadLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.ListOrdinarySessions(lease.Context(t.Context()), OrdinaryPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page) != 0 {
		t.Fatalf("page waited for audit: %v %v", page, err)
	}
	close(release)
	<-c.Invalidated()
	if _, err := c.OpenReadLease(t.Context()); !errors.Is(err, ErrCatalogInvalidated) {
		t.Fatalf("accepted invalid generation: %v", err)
	}
	if _, err := c.ListOrdinarySessions(lease.Context(t.Context()), OrdinaryPageRequest{Scope: "global"}); !errors.Is(err, ErrCatalogInvalidated) {
		t.Fatalf("old snapshot remained readable: %v", err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lease.db.Ping(); err == nil {
		t.Fatal("catalog shutdown left a dedicated lease connection open")
	}
	lease.Close()
}

func TestDeferredAuditCancellationAndTransientErrorsDoNotRevokeCatalog(t *testing.T) {
	for _, failure := range []error{errors.New("database is locked (5) (SQLITE_BUSY)"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			attempts := 0
			var waits []time.Duration
			c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, DeferredMetadataIntegrity: true,
				verifyMetadata:    func(context.Context) error { attempts++; return failure },
				waitMetadataRetry: func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(context.Background())
			<-c.integrityDone
			if errors.Is(failure, context.Canceled) {
				if attempts != 1 || len(waits) != 0 {
					t.Fatal("cancellation counted as a failed audit")
				}
			} else if attempts != 4 || len(waits) != 3 || waits[0] != time.Second || waits[1] != 5*time.Second || waits[2] != 30*time.Second {
				t.Fatalf("unexpected retry schedule: %d %v", attempts, waits)
			}
			select {
			case <-c.Invalidated():
				t.Fatal("transient/canceled audit revoked catalog")
			default:
			}
		})
	}
}

func TestCatalogReadReportsCorruptionToLifecycleOwner(t *testing.T) {
	revisions := 0
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true,
		OnRevision: func(uint64, []string, string) { revisions++ }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	broken := filepath.Join(t.TempDir(), "broken.sqlite")
	if err := os.WriteFile(broken, []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := c.readDB(t.Context()).QueryContext(t.Context(), `ATTACH DATABASE ? AS damaged`, broken)
	if rows != nil {
		rows.Close()
	}
	if err == nil {
		t.Fatal("broken database did not fail")
	}
	select {
	case <-c.Invalidated():
	default:
		t.Fatal("query corruption did not revoke the generation")
	}
	if _, err := os.Stat(broken); err != nil {
		t.Fatal("reader modified source before lifecycle cleanup")
	}
	c.publishRevision(30, nil, "late_publication")
	if revisions != 0 {
		t.Fatal("revoked catalog emitted a late revision")
	}
}

func TestDeferredIntegrityCannotCertifyContentCatalog(t *testing.T) {
	if c, err := Open(t.Context(), Options{InMemory: true, DeferredMetadataIntegrity: true}); err == nil {
		c.Close(context.Background())
		t.Fatal("non-advisory catalog skipped initial integrity validation")
	}
}

func TestCloseCancelsAnAdmittedIntegrityAudit(t *testing.T) {
	entered := make(chan struct{})
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, DeferredMetadataIntegrity: true,
		verifyMetadata: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.integrityDone:
	default:
		t.Fatal("close did not join integrity audit")
	}
	select {
	case <-c.Invalidated():
		t.Fatal("shutdown treated cancellation as corruption")
	default:
	}
}
