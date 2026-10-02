package session

import "sync"

// IdlePool bounds unbound runtime retention across historical storage roots.
// It never calls a service while holding its mutex. Victims are only candidates:
// the owning service rechecks bindings and execution before retiring them.
type IdlePool struct {
	mu      sync.Mutex
	entries map[*Runtime]idlePoolEntry
	used    int64
	clock   uint64
}

type idlePoolEntry struct {
	service *Service
	runtime *Runtime
	bytes   int64
	order   uint64
	epoch   uint64
}

const DesktopIdleBudgetBytes int64 = 256 << 20

// UseIdlePool must be called before publishing the service to clients.
func (s *Service) UseIdlePool(pool *IdlePool) { s.idlePool = pool }

// MetadataOnlyListings keeps listing observations free of content replay.
// Configure it before the service is published; history/search remain explicit.
func (s *Service) MetadataOnlyListings() { s.query.metadataOnlyListings = true }

func (p *IdlePool) add(s *Service, r *Runtime, bytes int64) []idlePoolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = make(map[*Runtime]idlePoolEntry)
	}
	if old, ok := p.entries[r]; ok {
		p.used -= old.bytes
	}
	p.clock++
	p.entries[r] = idlePoolEntry{service: s, runtime: r, bytes: bytes, order: p.clock, epoch: s.idleOrder[r]}
	p.used += bytes
	var victims []idlePoolEntry
	for p.used > DesktopIdleBudgetBytes && len(p.entries) > 0 {
		var oldest idlePoolEntry
		for _, entry := range p.entries {
			if oldest.runtime == nil || entry.order < oldest.order {
				oldest = entry
			}
		}
		delete(p.entries, oldest.runtime)
		p.used -= oldest.bytes
		victims = append(victims, oldest)
	}
	return victims
}

func (p *IdlePool) remove(r *Runtime) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.entries[r]; ok {
		p.used -= old.bytes
		delete(p.entries, r)
	}
}
