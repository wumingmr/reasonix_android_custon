package sessioncatalog

// DiscoveryEvent correlates admission with actual iterator dispatch. Root is
// a process-local number, not a hash of a private path; failure is a category,
// never a filesystem/database error string that could include content.
type DiscoveryEvent struct {
	Root     uint64
	Sequence uint64
	Phase    string
	Origin   string
	Failure  string
}

func (c *Catalog) observeDiscovery(target DirectoryTarget, phase, origin, failure string) {
	if c.opts.OnDiscovery == nil {
		return
	}
	key := queuePathKey(target.Path)
	id, exists := c.discoveryIDs.Load(key)
	if !exists {
		id, _ = c.discoveryIDs.LoadOrStore(key, c.discoverySeq.Add(1))
	}
	c.opts.OnDiscovery(DiscoveryEvent{Root: id.(uint64), Sequence: target.mutationSeq, Phase: phase, Origin: origin, Failure: failure})
}
