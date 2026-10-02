package acp

// sessionConfigDelta names exactly one config axis a caller asked to change
// (tool approval never rebuilds the controller, so it has no delta here).
// rebuildSession queues these — instead of a fully resolved SessionConfigState
// — while a turn or rebuild is in flight, one queue entry per axis, and
// applyPendingSessionConfig re-resolves the queued set against the session's
// live baseline once the session is idle. That way a queued change to one axis
// can never restore a stale value on another axis that changed in the
// meantime, whether that axis rebuilt already or is queued alongside.
type sessionConfigDelta struct {
	axis           string
	model          string
	effortOverride *string
}

func (d sessionConfigDelta) clone() sessionConfigDelta {
	d.effortOverride = cloneStringPtr(d.effortOverride)
	return d
}

// mergePendingConfig queues delta with last-write-wins per axis: it replaces a
// queued entry for the same axis and appends otherwise, so a change queued for
// one axis can never restore stale intent on another. A replacement model
// change follows older effort intent so applyTo clears it. Callers hold sess.mu.
func mergePendingConfig(queue []sessionConfigDelta, delta sessionConfigDelta) []sessionConfigDelta {
	for i := range queue {
		if queue[i].axis == delta.axis {
			if delta.axis == "model" && queue[i].model != delta.model {
				queue = append(queue[:i], queue[i+1:]...)
				return append(queue, delta.clone())
			}
			queue[i] = delta.clone()
			return queue
		}
	}
	return append(queue, delta.clone())
}

// removePendingAxes drops the queue entries whose axis a rebuild is applying,
// keeping entries other requests queued in the meantime so the post-maintenance
// drain still applies them. Callers hold sess.mu.
func removePendingAxes(queue, applied []sessionConfigDelta) []sessionConfigDelta {
	if len(queue) == 0 {
		return nil
	}
	kept := queue[:0]
	for _, q := range queue {
		drop := false
		for _, d := range applied {
			if q.axis == d.axis {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, q)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func clonePendingConfig(queue []sessionConfigDelta) []sessionConfigDelta {
	if len(queue) == 0 {
		return nil
	}
	out := make([]sessionConfigDelta, len(queue))
	for i := range queue {
		out[i] = queue[i].clone()
	}
	return out
}

func (d sessionConfigDelta) applyTo(p *SessionConfigStateParams) {
	switch d.axis {
	case "model":
		if p.Model != d.model {
			p.EffortOverride = nil
		}
		p.Model = d.model
	case "thought_level":
		p.EffortOverride = cloneStringPtr(d.effortOverride)
	}
}
