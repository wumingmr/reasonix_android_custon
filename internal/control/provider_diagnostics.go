package control

import (
	"sync"

	"reasonix/internal/provider"
)

type providerDiagnostic struct {
	provider.RequestObservation
	TurnID string `json:"turnId,omitempty"`
}

type providerDiagnosticBuffer struct {
	mu       sync.Mutex
	requests []providerDiagnostic
	dropped  uint64
	turns    []providerDiagnosticTurn
}

func (c *Controller) recordProviderRequest(turnID string, observation provider.RequestObservation) {
	b := &c.providerDiagnostics
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.requests {
		if b.requests[i].ID == observation.ID {
			b.requests[i] = providerDiagnostic{observation, turnID}
			return
		}
	}
	// Updates for an evicted request cannot displace newer requests or inflate
	// the dropped count. One entry per HTTP attempt, including all heartbeat reads.
	if observation.Phase != "request_started" {
		return
	}
	if len(b.requests) == 128 {
		for i := range b.turns {
			if b.turns[i].id == b.requests[0].TurnID {
				b.turns[i].dropped++
				break
			}
		}
		copy(b.requests, b.requests[1:])
		b.requests = b.requests[:127]
		b.dropped++
	}
	b.requests = append(b.requests, providerDiagnostic{observation, turnID})
}

func (c *Controller) providerDiagnosticSnapshot() any {
	b := &c.providerDiagnostics
	b.mu.Lock()
	defer b.mu.Unlock()
	return struct {
		Requests []providerDiagnostic `json:"requests"`
		Dropped  uint64               `json:"dropped"`
		Scope    string               `json:"scope"`
	}{append([]providerDiagnostic{}, b.requests...), b.dropped, "current controller lifetime; transport bytes include heartbeats, not proof of model progress; durable per-turn snapshots are in diagnostic/provider commit events"}
}
