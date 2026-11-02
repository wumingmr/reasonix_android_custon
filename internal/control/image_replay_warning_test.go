package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/attachment"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

type imageReplayRoundProvider struct {
	scriptedTurns
	requests []provider.Request
}

func (p *imageReplayRoundProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.requests = append(p.requests, req)
	return p.scriptedTurns.Stream(ctx, req)
}

func TestImageReplayWarningOncePerTurnAcrossToolRounds(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "image-understanding", true: "native-attachment"}[native], func(t *testing.T) {
			root := t.TempDir()
			reg := tool.NewRegistry()
			reg.Add(minimalFakeTool{name: "read_file", readOnly: true})
			p := &imageReplayRoundProvider{scriptedTurns: scriptedTurns{turns: [][]provider.Chunk{
				{toolCallChunk("read-1", "read_file", `{}`), {Type: provider.ChunkDone}}, textTurn("finished first turn"),
				{toolCallChunk("read-2", "read_file", `{}`), {Type: provider.ChunkDone}}, textTurn("finished second turn"),
			}}}
			exec := agent.New(p, reg, agent.NewSession("stable system"), agent.Options{ModelRef: "fixture/main"}, event.Discard)
			var notices atomic.Int32
			resolutions := 0
			c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl"), Runner: exec, Executor: exec, ModelRef: "fixture/main",
				FrozenImageInput: &native, ImageRouteConfig: config.Default(), VisionModel: "fixture/vision",
				VisionProviderResolver: func(string) (provider.Provider, error) {
					resolutions++
					return nil, errors.New("unknown vision model fixture/vision")
				}, Sink: event.FuncSink(func(e event.Event) {
					if e.Code == event.NoticeCodeImageReplayUnavailable {
						notices.Add(1)
					}
				}),
			})
			image := replayFixtureMessages(provider.RoleUser)[0]
			if native {
				draft, err := c.StageImage(t.Context(), "old.png", "image/png", "data:image/png;base64,"+tinyPNG)
				if err != nil {
					t.Fatal(err)
				}
				digest := draft.Ref.Content.Digest
				if err := os.Remove(filepath.Join(c.attachmentService().Store().Root(), "objects", digest[:2], digest[2:4], digest)); err != nil {
					t.Fatal(err)
				}
				image.ImageInputs = []attachment.ImageInput{{Kind: attachment.KindAttachment, Attachment: &draft.Ref}}
			}
			if err := c.RecordSessionMessages(t.Context(), "historical-image", []provider.Message{image}); err != nil {
				t.Fatal(err)
			}
			exec.Session().Add(image)
			for turn := range 2 {
				if err := c.RunTurn(t.Context(), "continue with text"); err != nil {
					t.Fatal(err)
				}
				if got := notices.Load(); got != int32(turn+1) {
					t.Fatalf("turn %d: notices=%d, want %d", turn, got, turn+1)
				}
				persisted := 0
				for _, commit := range terminationCommitHistory(t, c) {
					for _, e := range commit.Events {
						if e.Kind == "diagnostic" && strings.Contains(string(e.Payload), event.NoticeCodeImageReplayUnavailable) {
							persisted++
						}
					}
				}
				if persisted != turn+1 || len(p.requests) != (turn+1)*2 {
					t.Fatalf("turn %d: persisted=%d requests=%d", turn, persisted, len(p.requests))
				}
				for round, req := range p.requests[turn*2:] {
					assertImageReplayRound(t, req, round == 1)
				}
			}
			if !native && resolutions != 4 {
				t.Fatalf("image failure was cached: resolutions=%d, want 4", resolutions)
			}
		})
	}
}

func assertImageReplayRound(t *testing.T, req provider.Request, wantTool bool) {
	t.Helper()
	degraded, toolResult := false, false
	for _, m := range req.Messages {
		degraded = degraded || strings.Contains(m.Content, "original text") && strings.Contains(m.Content, "unavailable")
		toolResult = toolResult || m.Role == provider.RoleTool
		if len(m.ImageInputs)+len(m.Images) != 0 {
			t.Fatal("broken image reached provider")
		}
	}
	if !degraded || (wantTool && !toolResult) {
		t.Fatalf("degraded=%v toolResult=%v, wantTool=%v", degraded, toolResult, wantTool)
	}
}

func TestImageReplayWarningDeduplicatesRedactedCausesConcurrently(t *testing.T) {
	var notices atomic.Int32
	c := &Controller{sink: event.FuncSink(func(event.Event) { notices.Add(1) })}
	ctx := withImageReplayWarnings(t.Context())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { c.emitImageReplayWarning(ctx, errors.New("image failed API_KEY=sk-fixture-secret-123456")) })
	}
	wg.Wait()
	c.emitImageReplayWarning(ctx, errors.New("image failed API_KEY=sk-fixture-other-123456"))
	if notices.Load() != 1 {
		t.Fatalf("equivalent redacted causes emitted %d notices", notices.Load())
	}
	c.emitImageReplayWarning(ctx, errors.New("different image failure"))
	if notices.Load() != 2 {
		t.Fatal("distinct failure cause was hidden")
	}
	c.emitImageReplayWarning(withImageReplayWarnings(ctx), errors.New("different image failure"))
	if notices.Load() != 3 {
		t.Fatal("new turn inherited warning suppression")
	}
}
