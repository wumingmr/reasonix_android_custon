package agent

import (
	"sort"
	"sync"

	"reasonix/internal/tool"
)

// turnLoopState groups per-turn loop-guard maps so parallel tool goroutines
// share one lock instead of unsynchronized maps on turnRuntime.
type turnLoopState struct {
	mu                      sync.Mutex
	dispatchClasses         map[string]tool.CallClass
	acceptedDecisions       map[string]acceptedDecision
	previousErrorCategories map[string]struct{}
}

func (s *turnLoopState) setDispatchClasses(classes map[string]tool.CallClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatchClasses = classes
}

func (s *turnLoopState) dispatchClass(id string) (tool.CallClass, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	class, ok := s.dispatchClasses[id]
	return class, ok
}

func (s *turnLoopState) rememberDecision(id, question, answer string) {
	s.rememberDecisionAmbiguity(id, question, answer, decisionAmbiguity{})
}

func (s *turnLoopState) rememberDecisionAmbiguity(id, question, answer string, ambiguity decisionAmbiguity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acceptedDecisions == nil {
		s.acceptedDecisions = map[string]acceptedDecision{}
	}
	s.acceptedDecisions[id] = acceptedDecision{ID: id, Question: question, Answer: answer, Ambiguity: ambiguity}
}

func (s *turnLoopState) decision(id string) (acceptedDecision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dec, ok := s.acceptedDecisions[id]
	return dec, ok
}

func (s *turnLoopState) snapshotDecisions() []acceptedDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]acceptedDecision, 0, len(s.acceptedDecisions))
	for _, decision := range s.acceptedDecisions {
		out = append(out, decision)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *turnLoopState) advanceErrorCategories(current map[string]int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit := false
	next := make(map[string]struct{}, len(current))
	for category, count := range current {
		if count >= 2 {
			hit = true
		}
		if _, repeated := s.previousErrorCategories[category]; repeated {
			hit = true
		}
		next[category] = struct{}{}
	}
	s.previousErrorCategories = next
	return hit
}
