package agent

// compactionThreshold keeps construction-time values immutable. A live source
// must be concurrency-safe; children snapshot it before their Agent is built.
type compactionThreshold struct {
	initial float64
	current func() float64
}

func (s compactionThreshold) ratio() float64 {
	if s.current != nil {
		return s.current()
	}
	return s.initial
}

// CompactRatio reports the same live threshold used by each compaction decision.
// Standalone agents without a source retain their construction-time behavior.
func (a *Agent) CompactRatio() float64 {
	if a.compactRatioSource == nil {
		return a.compactRatio
	}
	if ratio := a.compactRatioSource(); ratio > 0 {
		return ratio
	}
	return defaultCompactRatio
}
