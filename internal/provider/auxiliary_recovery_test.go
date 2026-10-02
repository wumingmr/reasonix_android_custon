package provider

import (
	"context"
	"strings"
	"testing"
)

type auxiliaryScript struct {
	calls   int
	partial bool
	failed  bool
}

func (*auxiliaryScript) Name() string { return "auxiliary-test" }
func (p *auxiliaryScript) Stream(context.Context, Request) (<-chan Chunk, error) {
	p.calls++
	if p.failed {
		return nil, &APIError{Status: 503}
	}
	ch := make(chan Chunk, 3)
	ch <- Chunk{Type: ChunkText, Text: "candidate"}
	ch <- Chunk{Type: ChunkUsage, Usage: &Usage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}}
	if !p.partial {
		ch <- Chunk{Type: ChunkDone}
	}
	close(ch)
	return ch, nil
}
func TestAuxiliaryFailsWithoutRetryOrLeakingPartialText(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "upstream-error"} {
		t.Run(mode, func(t *testing.T) {
			p := &auxiliaryScript{partial: mode == "partial", failed: mode == "upstream-error"}
			ch, err := StreamAuxiliary(t.Context(), p, Request{})
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var usage *Usage
			var streamErr error
			for c := range ch {
				switch c.Type {
				case ChunkText:
					text.WriteString(c.Text)
				case ChunkUsage:
					usage = c.Usage
				case ChunkError:
					streamErr = c.Err
				}
			}
			if p.calls != 1 || usage == nil || usage.RequestCount != 1 {
				t.Fatalf("calls=%d usage=%+v", p.calls, usage)
			}
			if mode == "complete" {
				if text.String() != "candidate" || streamErr != nil || usage.PromptTokens != 10 {
					t.Fatalf("text=%q err=%v usage=%+v", text.String(), streamErr, usage)
				}
			} else if text.Len() != 0 || streamErr == nil {
				t.Fatalf("partial text=%q err=%v", text.String(), streamErr)
			}
		})
	}
}
