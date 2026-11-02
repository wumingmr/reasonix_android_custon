package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// unnumberedOverflowProvider answers a bare "prompt is too long" — the shape
// Zhipu GLM and several relays use — which states no window and no counts.
type unnumberedOverflowProvider struct {
	mu    sync.Mutex
	errs  []error
	reqs  []provider.Request
	texts []string
}

func (p *unnumberedOverflowProvider) Name() string { return "unnumbered-overflow" }

func (p *unnumberedOverflowProvider) ContextBudgetPolicy() provider.ContextBudgetPolicy {
	return provider.ContextBudgetPolicy{
		WindowMode: provider.ContextWindowShared,
		LimitMode:  provider.OutputLimitOmitWhenSafe,
	}
}

func (p *unnumberedOverflowProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	idx := len(p.reqs) - 1
	var err error
	if idx < len(p.errs) {
		err = p.errs[idx]
	}
	text := "done"
	if idx < len(p.texts) && p.texts[idx] != "" {
		text = p.texts[idx]
	}
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: text}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

func unnumberedOverflow() error {
	return &provider.ContextLimitError{
		APIError: &provider.APIError{Provider: "p", Status: 400, Body: `{"error":{"message":"prompt is too long"}}`},
	}
}

// newUnnumberedBudgetAgent sits a realistically sized conversation under a
// 128k window. The size matters: a measurement below the fold floor is
// deliberately never adopted, so a two-message fixture would exercise the
// refusal path instead of the measurement path.
func newUnnumberedBudgetAgent(t *testing.T, p provider.Provider) *Agent {
	t.Helper()
	big := strings.Repeat("word ", 4_000)
	msgs := []provider.Message{{Role: provider.RoleSystem, Content: "sys"}}
	for range 20 {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleAssistant, Content: big},
			provider.Message{Role: provider.RoleUser, Content: "continue"},
		)
	}
	sess := NewSession("")
	sess.Replace(msgs)
	registry := tool.NewRegistry()
	registry.Add(budgetTestTool{})
	// A configured window is what a booted agent always carries, because boot
	// resolves one through config.EffectiveContextWindow.
	return New(p, registry, sess, Options{ContextWindow: 128_000, CompactRatio: 2, Temperature: 0.25}, event.Discard)
}

// A provider that reports no numbers still told us something: the prompt it
// refused is larger than its window. The retry is sized from that measurement
// instead of resending the request that was just rejected.
func TestUnnumberedOverflowSizesTheRetryFromTheRefusedPrompt(t *testing.T) {
	prov := &unnumberedOverflowProvider{errs: []error{unnumberedOverflow(), nil}}
	a := newUnnumberedBudgetAgent(t, prov)

	got := a.streamWithSamplingRecovery(context.Background(), 1)
	if got.err != nil {
		t.Fatalf("recovery failed: %v", got.err)
	}
	prov.mu.Lock()
	defer prov.mu.Unlock()
	if len(prov.reqs) < 2 {
		t.Fatalf("provider requests = %d, want a retry after the refusal", len(prov.reqs))
	}
	if prov.reqs[1].MaxTokens <= 0 {
		t.Fatalf("retry MaxTokens = %d, the measurement did not size the retry", prov.reqs[1].MaxTokens)
	}
	// The retry is sized from the measurement, so it must fit inside the bound
	// the refusal established: resending the same oversized request buys nothing.
	if bound := a.probedWindow(); prov.reqs[1].MaxTokens >= bound {
		t.Fatalf("retry MaxTokens = %d does not fit the probed window %d", prov.reqs[1].MaxTokens, bound)
	}
	if a.probedWindow() <= 0 {
		t.Fatal("the refused prompt did not establish a window bound")
	}
}

// The refusal is recorded even when the provider named no counts of its own,
// so a later accepted measurement can tighten the bound.
func TestUnnumberedOverflowRecordsTheEstimateItRejected(t *testing.T) {
	prov := &unnumberedOverflowProvider{errs: []error{unnumberedOverflow()}}
	a := newUnnumberedBudgetAgent(t, prov)
	_ = a.streamWithSamplingRecovery(context.Background(), 1)
	if got := a.probedWindow(); got <= 0 {
		t.Fatalf("probed window = %d, want the size of the refused request", got)
	}
}

// A provider that states its own window keeps it: the measurement is only the
// fallback for an unstated one, never a replacement for a real answer.
func TestAReportedWindowIsNotReplacedByTheMeasurement(t *testing.T) {
	prov := &scriptedBudgetProvider{
		policy: provider.ContextBudgetPolicy{
			WindowMode: provider.ContextWindowShared, AutoOutputTokens: 384_000,
			MaxOutputTokens: 384_000, LimitMode: provider.OutputLimitOmitWhenSafe,
		},
		errs: []error{issue8909Limit(), nil},
	}
	a := newUnnumberedBudgetAgent(t, prov)
	if got := a.streamWithSamplingRecovery(context.Background(), 1); got.err != nil {
		t.Fatalf("recovery failed: %v", got.err)
	}
	if a.lastAdmission().ObservedWindow != issue8909Limit().WindowTokens {
		t.Fatalf("observed window = %d, want the provider's %d",
			a.lastAdmission().ObservedWindow, issue8909Limit().WindowTokens)
	}
}
