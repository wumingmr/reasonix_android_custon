package tool

import (
	"context"
	"fmt"
	"strings"
)

// ContextualAvailabilityReason optionally explains a failed ContextualTool
// check. Implementations must be observational and return no private data.
type ContextualAvailabilityReason interface {
	UnavailableReason(context.Context) string
}

// ContextualUnavailableReason is shared by discovery and the execution gate.
// It does not change registry membership or provider-visible tool schemas.
func ContextualUnavailableReason(ctx context.Context, target Tool) string {
	contextual, ok := target.(ContextualTool)
	if !ok || contextual.ProviderVisible(ctx) {
		return ""
	}
	if diagnostic, ok := target.(ContextualAvailabilityReason); ok {
		if reason := strings.TrimSpace(diagnostic.UnavailableReason(ctx)); reason != "" {
			return strings.TrimPrefix(reason, "blocked: ")
		}
	}
	return fmt.Sprintf("tool %q is unavailable in the current workflow context", target.Name())
}
