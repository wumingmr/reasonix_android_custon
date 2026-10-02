package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type requestGateFixture struct {
	blocked bool
	ref     string
	err     error
}

func (g *requestGateFixture) BeforeModelRequest(ref string) error {
	g.ref = ref
	if g.blocked {
		return errors.New("blocked")
	}
	return nil
}
func (g *requestGateFixture) ModelRequestFailed(ref string, err error) {
	g.ref = ref
	g.err = err
	g.blocked = true
}

type gatedProviderFixture struct {
	calls       int
	request     Request
	streamError bool
}

func (*gatedProviderFixture) Name() string         { return "connection" }
func (*gatedProviderFixture) ModelInfo() ModelInfo { return ModelInfo{ID: "model"} }
func (p *gatedProviderFixture) Stream(_ context.Context, req Request) (<-chan Chunk, error) {
	p.calls++
	p.request = req
	err := &AuthError{Provider: "connection", Status: 403}
	if !p.streamError {
		return nil, err
	}
	ch := make(chan Chunk, 1)
	ch <- Chunk{Type: ChunkError, Err: err}
	close(ch)
	return ch, nil
}

func TestRequestGateObservesHTTPAndStreamRejectionsWithoutChangingRequest(t *testing.T) {
	for _, streamError := range []bool{false, true} {
		p := &gatedProviderFixture{streamError: streamError}
		gate := &requestGateFixture{}
		ctx, cancel := context.WithCancel(WithRequestGate(context.Background(), gate))
		req := Request{Messages: []Message{{Role: RoleUser, Content: "unchanged"}}, MaxTokens: 7}
		ch, err := Stream(ctx, p, req)
		if err == nil {
			for chunk := range ch {
				err = chunk.Err
			}
		}
		var auth *AuthError
		if !errors.As(err, &auth) || auth.ModelRef != "connection/model" || gate.ref != auth.ModelRef {
			t.Fatalf("lost actual request identity: %v", err)
		}
		if _, err := Stream(ctx, p, req); err == nil || p.calls != 1 {
			t.Fatal("rejection allowed automatic retry")
		}
		if !reflect.DeepEqual(req, p.request) {
			t.Fatal("gate changed provider request")
		}
		cancel()
	}
}
