package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/transcript"
)

// rendererFollowCursor mirrors TranscriptFollowClient.validate in
// desktop/frontend/src/lib/transcriptFollowClient.ts (lines 174-220): any
// error it returns is one that makes the renderer drop the subscription.
type rendererFollowCursor struct {
	revision, coverage uint64
	indexes            map[string]uint64
	attempts           map[string]string
	results            map[string]uint64
	resultOrder        []string
	deltas             map[string]string
}

func newRendererFollowCursor(snapshot *transcript.Snapshot) *rendererFollowCursor {
	c := &rendererFollowCursor{revision: snapshot.ProjectionRevision, coverage: snapshot.CoveredThroughSeq,
		indexes: map[string]uint64{}, attempts: map[string]string{}, results: map[string]uint64{}, deltas: map[string]string{}}
	for _, attempt := range snapshot.ActiveAttempts {
		c.indexes[attempt.ID], c.attempts[attempt.ID] = attempt.NextIndex, attempt.MessageID
	}
	return c
}

func (c *rendererFollowCursor) noteResult(messageID string, sequence uint64) {
	if _, ok := c.results[messageID]; !ok {
		c.resultOrder = append(c.resultOrder, messageID)
	}
	c.results[messageID] = sequence
	for len(c.resultOrder) > 192 {
		delete(c.results, c.resultOrder[0])
		c.resultOrder = c.resultOrder[1:]
	}
}

func (c *rendererFollowCursor) accept(changes []transcript.Change) error {
	ordered := slices.Clone(changes)
	slices.SortFunc(ordered, func(a, b transcript.Change) int { return cmp.Compare(a.Revision, b.Revision) })
	for _, change := range ordered {
		if change.Revision <= c.revision {
			continue
		}
		if change.ResetRequired || change.Revision != c.revision+1 {
			return fmt.Errorf("revision_gap at revision %d", change.Revision)
		}
		if change.FirstSeq != 0 {
			if change.FirstSeq != c.coverage+1 || change.CommitSeq < change.FirstSeq {
				return fmt.Errorf("business_gap at revision %d: first=%d coverage=%d", change.Revision, change.FirstSeq, c.coverage)
			}
			c.coverage = change.CommitSeq
		} else if change.CommitSeq != c.coverage {
			return fmt.Errorf("frame_cut_mismatch at revision %d: commit=%d coverage=%d", change.Revision, change.CommitSeq, c.coverage)
		}
		e := change.Event
		for _, record := range change.Records {
			if record.MessageID != "" {
				c.noteResult(record.MessageID, change.CommitSeq)
			}
		}
		if e != nil && e.Kind == "stream_attempt" && e.StreamAttempt != nil && e.StreamAttempt.Action == "begin" {
			if e.MessageID == "" {
				return fmt.Errorf("sampling_identity_missing at revision %d", change.Revision)
			}
			c.indexes[e.StreamAttempt.ID], c.attempts[e.StreamAttempt.ID] = 0, e.MessageID
		}
		if change.AttemptID != "" && change.ResultSeq == 0 && (e == nil || e.Kind != "stream_attempt") {
			if c.indexes[change.AttemptID] != change.Index {
				return fmt.Errorf("sampling_gap at revision %d", change.Revision)
			}
			c.indexes[change.AttemptID] = change.Index + 1
		}
		if change.ResultSeq != 0 && (change.ResultSeq > c.coverage || (change.ResultKind != "message/complete" && change.ResultKind != "message/interrupted")) {
			return fmt.Errorf("settlement_not_committed at revision %d", change.Revision)
		}
		if change.ResultSeq != 0 {
			message := c.attempts[change.AttemptID]
			recorded, known := c.results[message]
			if message == "" || e == nil || message != e.MessageID || known && recorded != change.ResultSeq {
				return fmt.Errorf("settlement_identity_mismatch at revision %d", change.Revision)
			}
		}
		if e != nil && e.Kind == "stream_attempt" && e.StreamAttempt != nil && e.StreamAttempt.Action != "begin" {
			delete(c.indexes, e.StreamAttempt.ID)
			delete(c.attempts, e.StreamAttempt.ID)
		}
		if e != nil && (e.Kind == "text" || e.Kind == "reasoning") {
			c.deltas[e.Kind] += e.Text
		}
		c.revision = change.Revision
	}
	return nil
}

// legacyStreamProvider answers each turn from a script: a streamed reply, a
// tool-call-only round followed by a reply, or a stream that stalls until the
// request is cancelled.
type legacyStreamProvider struct {
	mu       sync.Mutex
	mode     string
	toolSent bool
}

func (p *legacyStreamProvider) set(mode string) {
	p.mu.Lock()
	p.mode, p.toolSent = mode, false
	p.mu.Unlock()
}

func (p *legacyStreamProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	mode, toolRound := p.mode, p.mode == "tool" && !p.toolSent
	if toolRound {
		p.toolSent = true
	}
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(delta string) {
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{`+delta+`}}]}`+"\n\n")
		flusher.Flush()
	}
	switch {
	case toolRound:
		send(`"tool_calls":[{"index":0,"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"note.txt\"}"}}]`)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
	case mode == "stall":
		send(`"reasoning_content":"pondering"`)
		<-r.Context().Done()
		return
	default:
		for _, chunk := range []string{`"reasoning_content":"weigh "`, `"reasoning_content":"it"`, `"content":"stream"`, `"content":"ed"`} {
			send(chunk)
			time.Sleep(20 * time.Millisecond)
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

type legacyFollowTurn struct{ name, mode, reasoning, text string }

func TestLegacyTopicTranscriptFollowStreamsDeltasTheRendererAccepts(t *testing.T) {
	reply := legacyFollowTurn{"streamed reply", "stream", "weigh it", "streamed"}
	for _, first := range []legacyFollowTurn{
		reply,
		{"tool-call-only round", "tool", "weigh it", "streamed"},
		{"cancelled round", "stall", "pondering", ""},
	} {
		t.Run(first.name, func(t *testing.T) { followLegacyTopicTurns(t, first, reply) })
	}
}

func followLegacyTopicTurns(t *testing.T, turns ...legacyFollowTurn) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "TEST_MODEL_KEY", "sk-test")
	script := &legacyStreamProvider{}
	stub := httptest.NewServer(script)
	t.Cleanup(stub.Close)
	cfg := config.Default()
	cfg.DefaultModel = "test/test-model"
	cfg.Desktop.ProviderAccess = []string{"test"}
	cfg.Providers = []config.ProviderEntry{{Name: "test", Kind: "openai", BaseURL: stub.URL, Model: "test-model", APIKeyEnv: "TEST_MODEL_KEY"}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, "Project"); err != nil {
		t.Fatal(err)
	}
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTopicSession(t, dir, "legacy.jsonl", "legacy-topic", "Legacy", root)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	t.Cleanup(func() { app.shutdown(context.Background()) })
	events := newActivationEventRecorder(app)
	ticket, err := app.StartTopicActivation(TopicActivationRequest{Scope: "project", WorkspaceRoot: root, TopicID: "legacy-topic", SessionPath: path, RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := events.waitFor(t, func(ev TopicActivationEvent) bool {
		return ev.RequestID == "open" && (ev.Phase == "ready" || ev.Phase == "failed")
	}); ev.Phase != "ready" {
		t.Fatalf("activation failed: %+v", ev)
	}

	base, err := app.TranscriptFollowForTab(ticket.TabID, transcript.FollowRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = app.TranscriptFollowForTab(ticket.TabID, transcript.FollowRequest{Subscription: base.Subscription, Close: true})
	})
	if base.StorageBackend != "legacy" {
		t.Fatalf("fixture must follow a legacy-backed session, got %q", base.StorageBackend)
	}
	cursor := newRendererFollowCursor(base.Snapshot)
	for _, turn := range turns {
		script.set(turn.mode)
		clear(cursor.deltas)
		if err := app.SubmitToTab(ticket.TabID, turn.name); err != nil {
			t.Fatal(err)
		}
		cancelled := false
		deadline := time.Now().Add(15 * time.Second)
		for done := false; !done; {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not finish", turn.name)
			}
			resp, err := app.TranscriptFollowForTab(ticket.TabID, transcript.FollowRequest{Subscription: base.Subscription, AfterRevision: cursor.revision})
			if err != nil {
				t.Fatal(err)
			}
			if resp.ResetRequired {
				t.Fatalf("%s: follower was reset", turn.name)
			}
			if err := cursor.accept(resp.Changes); err != nil {
				t.Fatalf("%s: renderer would discard the follow and re-baseline: %v", turn.name, err)
			}
			for _, change := range resp.Changes {
				done = done || change.Event != nil && change.Event.Kind == "turn_done"
			}
			if turn.mode == "stall" && !cancelled && cursor.deltas["reasoning"] != "" {
				app.CancelTab(ticket.TabID)
				cancelled = true
			}
		}
		if cursor.deltas["reasoning"] != turn.reasoning || cursor.deltas["text"] != turn.text {
			t.Fatalf("%s: renderer received deltas %+v", turn.name, cursor.deltas)
		}
	}
}
