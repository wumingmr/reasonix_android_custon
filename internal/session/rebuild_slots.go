package session

import (
	"context"
	"sync"
)

// The shared background-index budget stays at two concurrent build tasks.
// Slots are handed out by priority: user-requested history pages and recovery
// win before search, and both win before catalog/prefetch metadata work.
// Catalog workers wait at prefetch priority; saturated slots never discard
// pending session metadata work.
type rebuildPriority int

const (
	rebuildPriorityUser rebuildPriority = iota
	rebuildPrioritySearch
	rebuildPriorityPrefetch
	rebuildPriorityCount
)

type rebuildWaiter struct {
	notify  chan struct{}
	granted bool
}

type rebuildSlots struct {
	capacity int
	held     int
	mu       sync.Mutex
	queues   [rebuildPriorityCount][]*rebuildWaiter
}

func newRebuildSlots(capacity int) *rebuildSlots {
	return &rebuildSlots{capacity: capacity}
}

func (s *rebuildSlots) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held >= s.capacity {
		return false
	}
	s.held++
	return true
}

// acquire waits for a slot. When one frees, the highest-priority waiter wins;
// within one priority the earliest waiter wins. A granted slot must be
// returned through release, including when ctx was cancelled in the same
// select round that granted it.
func (s *rebuildSlots) acquire(ctx context.Context, prio rebuildPriority) error {
	if prio < rebuildPriorityUser || prio >= rebuildPriorityCount {
		prio = rebuildPriorityPrefetch
	}
	s.mu.Lock()
	if s.held < s.capacity {
		s.held++
		s.mu.Unlock()
		return nil
	}
	w := &rebuildWaiter{notify: make(chan struct{}, 1)}
	s.queues[prio] = append(s.queues[prio], w)
	s.mu.Unlock()
	select {
	case <-w.notify:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		granted := w.granted
		if !granted {
			queue := s.queues[prio]
			for i, candidate := range queue {
				if candidate == w {
					s.queues[prio] = append(queue[:i:i], queue[i+1:]...)
					break
				}
			}
		}
		s.mu.Unlock()
		if granted {
			return nil
		}
		return ctx.Err()
	}
}

func (s *rebuildSlots) release() {
	s.mu.Lock()
	s.held--
	if s.held < 0 {
		s.held = 0
	}
	for offset := range int(rebuildPriorityCount - rebuildPriorityUser) {
		prio := rebuildPriorityUser + rebuildPriority(offset)
		if len(s.queues[prio]) == 0 {
			continue
		}
		w := s.queues[prio][0]
		s.queues[prio] = s.queues[prio][1:]
		w.granted = true
		w.notify <- struct{}{}
		break
	}
	s.mu.Unlock()
}
