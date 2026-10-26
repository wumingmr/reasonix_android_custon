package goal

import "math"

// AddWorkDuration records completed active work without changing tool authority.
// The host serializes and persists this candidate before publishing it.
func (m *Machine) AddWorkDuration(ref Ref, durationMs int64) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if durationMs > 0 {
		current.WorkDurationMs += min(durationMs, math.MaxInt64-current.WorkDurationMs)
	}
	return *m.viewLocked(), nil
}
