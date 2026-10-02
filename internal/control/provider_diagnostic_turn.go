package control

type providerDiagnosticTurn struct {
	id      string
	dropped uint64
}

// Track bounded turn summaries independently of retained requests so a turn
// whose requests were all evicted still reports loss. Older metadata is unknown.
func (c *Controller) beginProviderDiagnosticTurn(turnID string) {
	b := &c.providerDiagnostics
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, turn := range b.turns {
		if turn.id == turnID {
			return
		}
	}
	if len(b.turns) == 128 {
		copy(b.turns, b.turns[1:])
		b.turns = b.turns[:127]
	}
	b.turns = append(b.turns, providerDiagnosticTurn{id: turnID})
}

func (c *Controller) providerDiagnosticTurnSnapshot(turnID string) ([]providerDiagnostic, *uint64, bool) {
	b := &c.providerDiagnostics
	b.mu.Lock()
	defer b.mu.Unlock()
	requests := []providerDiagnostic{}
	for _, request := range b.requests {
		if turnID != "" && request.TurnID == turnID {
			requests = append(requests, request)
		}
	}
	for _, turn := range b.turns {
		if turn.id == turnID && turnID != "" {
			dropped := turn.dropped
			return requests, &dropped, dropped > 0
		}
	}
	return requests, nil, true
}
