package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"reasonix/internal/historywork"
	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

type cancelEventRead struct {
	source io.Reader
	cancel context.CancelFunc
	reads  int
}

func stageEventDisplayMessages(ctx context.Context, db *sql.DB, decoder *json.Decoder) (int, error) {
	scanner := &eventPagerScanner{ctx: ctx, db: db, decoder: decoder}
	defer func() {
		if scanner.tx != nil {
			_ = scanner.tx.Rollback()
		}
	}()
	if err := scanner.transaction(); err != nil {
		return 0, err
	}
	if err := scanner.messages(); err != nil {
		return scanner.progress.Pending, err
	}
	return scanner.progress.Pending, scanner.tx.Commit()
}

func (r *cancelEventRead) Read(p []byte) (int, error) {
	n, err := r.source.Read(p[:min(len(p), 512)])
	r.reads++
	if r.reads == 2 {
		r.cancel()
	}
	return n, err
}

func TestDisplayPagerSchemaOneCancellationDiscardsUncommittedBatch(t *testing.T) {
	handle, err := projectiondb.Open(t.Context(), projectiondb.OpenOptions{Path: filepath.Join(t.TempDir(), "partial.sqlite"), Migrations: displayPagerMigrations, RequireDisk: true, MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.DB.Close()
	if _, err := handle.DB.ExecContext(t.Context(), `CREATE TABLE event_pending(position INTEGER PRIMARY KEY,offset INTEGER NOT NULL,length INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	array := "[" + strings.Repeat(`{"role":"user","content":"question"},`, 499) + `{"role":"user","content":"last"}]`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &cancelEventRead{source: strings.NewReader(array), cancel: cancel}
	_, err = stageEventDisplayMessages(ctx, handle.DB, json.NewDecoder(&historywork.Reader{Context: ctx, Source: source}))
	if !errors.Is(err, context.Canceled) || source.reads != 2 {
		t.Fatalf("read checkpoint failed: calls=%d err=%v", source.reads, err)
	}
	var rows int
	if err := handle.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_pending`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("canceled transaction leaked: %d %v", rows, err)
	}
	count, err := stageEventDisplayMessages(t.Context(), handle.DB, json.NewDecoder(strings.NewReader(array)))
	if err != nil || count != 500 {
		t.Fatalf("retry: %d %v", count, err)
	}
}

func eventDisplayFixture(t *testing.T, text string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"stale checkpoint\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionEventLog(path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Join(t.TempDir(), "display.sqlite")
}

func TestDisplayPagerSchemaOneMatchesReplayAndReusesCache(t *testing.T) {
	var messages []provider.Message
	for i := range 300 {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("question %d", i)}, provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("answer %d", i)})
	}
	created := time.Unix(1700000000, 0).UTC()
	records := []sessionEventRecord{
		{SchemaVersion: 1, Type: sessionEventTypeReplace, Messages: []provider.Message{{Role: provider.RoleUser, Content: "obsolete"}}},
		{SchemaVersion: 1, Type: sessionEventTypeAppend, MessageIndex: 1, Messages: []provider.Message{{Role: provider.RoleAssistant, Content: "obsolete answer"}}},
		{SchemaVersion: 1, Type: sessionEventTypeReplace, Messages: messages[:500]},
		{SchemaVersion: 1, Type: sessionEventTypeAppend, MessageIndex: 500, Messages: messages[500:], CreatedAt: created},
	}
	var log strings.Builder
	for _, record := range records {
		if err := json.NewEncoder(&log).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	path, cache := eventDisplayFixture(t, log.String())
	checkpoint, _ := os.ReadFile(path)
	p, err := OpenDisplayPager(t.Context(), path, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !p.SchemaOne || p.DAG || !p.Built || len(p.Header.Entries) != 0 || p.Header.MessageCount != 600 {
		t.Fatalf("event projection: %+v", p.Header)
	}
	replay, err := replaySessionEventLog(store.SessionEventLog(path))
	if err != nil || replay.damaged {
		t.Fatalf("fixture replay: %+v %v", replay, err)
	}
	var got []provider.Message
	for lo := 0; lo < 600; lo += 32 {
		page, err := p.EventMessages(lo, min(lo+32, 600))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
	}
	for i := range replay.msgs {
		if replay.msgs[i].CreatedAt <= 0 && !replay.times[i].IsZero() {
			replay.msgs[i].CreatedAt = replay.times[i].UnixMilli()
		}
	}
	if !reflect.DeepEqual(got, replay.msgs) {
		t.Fatal("paged event messages differ from replace/append replay")
	}
	turns, err := p.TurnEntries(257, 3)
	if err != nil || len(turns) != 3 || turns[0].Index != 512 || turns[0].AuthoredTurn != 257 {
		t.Fatalf("event outline: %+v %v", turns, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	var meter historywork.Coordinator
	reopened, err := OpenDisplayPager(meter.Context(t.Context()), path, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Built || !reopened.SchemaOne || meter.Diagnostics().InstrumentedReadBytes >= int64(len(log.String())) {
		t.Fatal("cached reopen replayed the entire log")
	}
	after, _ := os.ReadFile(path)
	events, _ := os.ReadFile(store.SessionEventLog(path))
	if string(checkpoint) != string(after) || log.String() != string(events) {
		t.Fatal("cold event reading rewrote authoritative files")
	}
	if _, err := os.Stat(store.SessionDisplayIndex(path)); !os.IsNotExist(err) {
		t.Fatal("event reading created a compatibility display sidecar")
	}
}

func TestDisplayPagerSchemaOneAcceptsFieldOrderAndEmptyReplacement(t *testing.T) {
	for _, suffix := range []string{"", `{"messages":null,"type":"replace","schema_version":1}`, `{"schema_version":1,"type":"replace"}`} {
		t.Run(fmt.Sprint(len(suffix)), func(t *testing.T) {
			// The header is beyond the fast probe. A valid field order must not
			// cause a full-record allocation or fallback to the stale checkpoint.
			text := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 10000) + `"}],"extra":{"array":[1,{"two":[true,null]}]},"type":"replace","schema_version":1}` + "\n" + suffix
			path, cache := eventDisplayFixture(t, text)
			p, err := OpenDisplayPager(t.Context(), path, cache)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			want := 1
			if suffix != "" {
				want = 0
			}
			if !p.SchemaOne || p.Header.MessageCount != want {
				t.Fatalf("empty replacement: %+v", p.Header)
			}
			p.Close()
			var meter historywork.Coordinator
			reopened, err := OpenDisplayPager(meter.Context(t.Context()), path, cache)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.Built || meter.Diagnostics().InstrumentedReadBytes != 0 {
				t.Fatal("cached reopen rescanned a late schema header")
			}
		})
	}
}

func TestDisplayPagerSchemaOneDamagedTailNeverPublishesPrefix(t *testing.T) {
	base := `{"schema_version":1,"type":"replace","messages":[{"role":"user","content":"one"}]}` + "\n"
	for _, tail := range []string{
		`{"schema_version":1,"type":"append","message_index":1,"messages":[`,
		`{"schema_version":1,"type":"append","message_index":7,"messages":[]}`,
		`{"schema_version":1,"type":"rewrite","messages":[]}`,
		`{"schema_version":4,"type":"replace","messages":[]}`,
	} {
		t.Run(tail, func(t *testing.T) {
			path, cache := eventDisplayFixture(t, base)
			p, err := OpenDisplayPager(t.Context(), path, cache)
			if err != nil {
				t.Fatal(err)
			}
			p.Close()
			if err := os.WriteFile(store.SessionEventLog(path), []byte(base+tail), 0600); err != nil {
				t.Fatal(err)
			}
			if p, err := OpenDisplayPager(t.Context(), path, cache); !errors.Is(err, ErrSessionDisplayReadModelDamaged) {
				if p != nil {
					p.Close()
				}
				t.Fatalf("damaged prefix accepted: %v", err)
			}
			actual, _ := os.ReadFile(store.SessionEventLog(path))
			if string(actual) != base+tail {
				t.Fatal("display preparation repaired the damaged source")
			}
		})
	}
}

func TestDisplayPagerNewWriterCannotBeHiddenByFreshSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.jsonl")
	session := NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "old"})
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionEventLog(path), []byte("{\"schema_version\":99,\"type\":\"replace\",\"messages\":[]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Satisfy the old sidecar timestamp gate deliberately: a newer authority
	// must still win over offsets that only describe the previous checkpoint.
	stamp := SessionContentModTime(path).Add(time.Second)
	if err := os.Chtimes(store.SessionDisplayIndex(path), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if p, err := OpenDisplayPager(t.Context(), path, filepath.Join(t.TempDir(), "cache.sqlite")); !errors.Is(err, ErrDisplayFormatUnsupported) {
		if p != nil {
			p.Close()
		}
		t.Fatalf("future log ignored: %v", err)
	}
}
