package agent

import (
	"testing"

	"golang.org/x/net/http2"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestHTTP2FailureDoesNotReplayCommittedOrSpeculativeTools(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "speculative", true: "committed"}[committed], func(t *testing.T) {
			writer := &countingWriterTool{}
			reg := tool.NewRegistry()
			reg.Add(writer)
			call := provider.ToolCall{ID: "write-once", Name: "write_file", Arguments: `{"path":"x.txt","content":"once"}`}
			failure := provider.StreamInterrupt(http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}, provider.StreamInterruptPrematureEOF)
			turns := []testutil.Turn{}
			if committed {
				turns = append(turns, testutil.Turn{ToolCalls: []provider.ToolCall{call}})
			}
			turns = append(turns, testutil.Turn{Chunks: []provider.Chunk{
				{Type: provider.ChunkToolCall, ToolCall: &call},
				{Type: provider.ChunkError, Err: failure},
			}}, testutil.Turn{Text: "manual continuation complete"})
			p := testutil.NewMock("m", turns...)
			sink := &recordSink{}
			a := New(p, reg, NewSession(""), Options{}, sink)
			err := a.Run(withNoClosedLoop(t.Context()), "write once")
			if provider.HTTP2TransportCode(err) != "INTERNAL_ERROR" {
				t.Fatalf("failure was hidden: %v", err)
			}
			wantWrites, wantCalls := int32(0), 1
			if committed {
				wantWrites, wantCalls = 1, 2
			}
			if writer.calls.Load() != wantWrites || p.CallCount() != wantCalls || len(sink.kinds(event.Retrying)) != 0 {
				t.Fatalf("tool/request replayed: writes=%d calls=%d", writer.calls.Load(), p.CallCount())
			}
			if err := a.Run(withNoClosedLoop(t.Context()), "continue"); err != nil {
				t.Fatal(err)
			}
			if writer.calls.Load() != wantWrites || p.CallCount() != wantCalls+1 {
				t.Fatalf("manual continuation replayed a tool: writes=%d calls=%d", writer.calls.Load(), p.CallCount())
			}
		})
	}
}
