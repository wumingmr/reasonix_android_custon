package persistentshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type stagingConn struct{ write func([]byte) (int, error) }

func (c stagingConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (c stagingConn) Write(p []byte) (int, error) { return c.write(p) }
func (c stagingConn) Close() error                { return nil }

func TestCommandStagingConsumesSplitAcknowledgementBeforeNextWrite(t *testing.T) {
	s := &session{pendingRead: make(chan readChunkResult, 4)}
	writes, executed := 0, false
	s.conn = stagingConn{write: func(p []byte) (int, error) {
		if len(s.pendingRead) != 0 {
			t.Fatal("advanced on echoed source or an earlier acknowledgement")
		}
		if strings.Contains(string(p), "; eval -- ") {
			executed = true
			return len(p), nil
		}
		ack := fmt.Sprintf("S_INPUT_%d", writes*commandWordLimit/4)
		writes++
		s.pendingRead <- readChunkResult{data: p}
		s.pendingRead <- readChunkResult{data: []byte("S_INPUT_OLD\n")}
		s.pendingRead <- readChunkResult{data: []byte(ack[:3])}
		s.pendingRead <- readChunkResult{data: []byte(ack[3:] + "\n")}
		return len(p), nil
	}}
	if err := s.writeCommand(t.Context(), strings.Repeat("x", 4096), "S", "E:"); err != nil {
		t.Fatal(err)
	}
	if writes != 32 || !executed {
		t.Fatalf("stages=%d executed=%v", writes, executed)
	}
}

func TestCommandStagingCancellationDoesNotExecutePartialCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &session{pendingRead: make(chan readChunkResult, 1)}
	writes := 0
	s.conn = stagingConn{write: func(p []byte) (int, error) {
		writes++
		if strings.Contains(string(p), "; eval -- ") {
			t.Fatal("executed an unacknowledged command")
		}
		s.pendingRead <- readChunkResult{data: p}
		cancel()
		return len(p), nil
	}}
	if err := s.writeCommand(ctx, strings.Repeat("x", 4096), "S", "E:"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if writes != 1 {
		t.Fatalf("wrote %d stages after cancellation", writes)
	}
}
