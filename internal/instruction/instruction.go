package instruction

import (
	"context"
)

// VerifyCheck preserves the retired structured-check option for old callers.
// Current execution reads project instructions as normal model context.
type VerifyCheck struct {
	Command    string
	SourcePath string
	Line       int
}

type contextKey struct{}

func WithChecks(ctx context.Context, checks []VerifyCheck) context.Context {
	if len(checks) == 0 {
		return ctx
	}
	cp := append([]VerifyCheck(nil), checks...)
	return context.WithValue(ctx, contextKey{}, cp)
}

func FromContext(ctx context.Context) []VerifyCheck {
	checks, ok := ctx.Value(contextKey{}).([]VerifyCheck)
	if !ok || len(checks) == 0 {
		return nil
	}
	return append([]VerifyCheck(nil), checks...)
}
