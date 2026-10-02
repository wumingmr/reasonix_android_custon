package provider

import (
	"context"
	"errors"
	"strings"
)

// RequestGate belongs to one runtime's frozen connection snapshot. Keeping it
// in the request context preserves the concrete Provider and optional interfaces.
type RequestGate interface {
	BeforeModelRequest(string) error
	ModelRequestFailed(string, error)
}

type requestGateKey struct{}

func WithRequestGate(ctx context.Context, gate RequestGate) context.Context {
	return context.WithValue(ctx, requestGateKey{}, gate)
}

func Stream(ctx context.Context, p Provider, req Request) (<-chan Chunk, error) {
	return StreamForModel(ctx, p, req, "")
}

func requestModelRef(p Provider) string {
	ref := p.Name() + "/"
	if metadata, ok := p.(ModelInfoProvider); ok {
		if model := metadata.ModelInfo().ID; model != "" {
			ref += model
		}
	}
	return ref
}

// StreamForModel admits and observes each actual request, including stream
// errors, before a caller can retry. It never changes the wire request.
func StreamForModel(ctx context.Context, p Provider, req Request, ref string) (<-chan Chunk, error) {
	gate, _ := ctx.Value(requestGateKey{}).(RequestGate)
	if gate == nil {
		return p.Stream(ctx, req)
	}
	if strings.TrimSpace(ref) == "" {
		ref = requestModelRef(p)
	}
	if err := gate.BeforeModelRequest(ref); err != nil {
		return nil, err
	}
	record := func(err error) error {
		var auth *AuthError
		if errors.As(err, &auth) && auth != nil {
			copy := *auth
			if copy.ModelRef == "" {
				copy.ModelRef = strings.TrimSpace(ref)
			}
			gate.ModelRequestFailed(copy.ModelRef, &copy)
			return &copy
		}
		return err
	}
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return nil, record(err)
	}
	out := make(chan Chunk)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-ch:
				if !ok {
					return
				}
				chunk.Err = record(chunk.Err)
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
