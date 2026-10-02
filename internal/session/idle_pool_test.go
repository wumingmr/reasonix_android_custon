package session

import "testing"

func TestOldIdleRetirementCannotRemoveNewIdleEpoch(t *testing.T) {
	r := &Runtime{}
	s := &Service{idleOrder: map[*Runtime]uint64{r: 2}}
	if err := s.retireIfUnboundAt(t.Context(), r, 1); err != nil {
		t.Fatal(err)
	}
	if s.idleOrder[r] != 2 {
		t.Fatal("stale timer removed new idle retention")
	}
}

func TestIdlePoolSharesBudgetAcrossRootsAndProtectsReboundEntries(t *testing.T) {
	var pool IdlePool
	a, b := &Service{}, &Service{}
	one, two, three := &Runtime{}, &Runtime{}, &Runtime{}
	if victims := pool.add(a, one, 180<<20); len(victims) != 0 {
		t.Fatal("early eviction")
	}
	victims := pool.add(b, two, 100<<20)
	if len(victims) != 1 || victims[0].service != a || victims[0].runtime != one {
		t.Fatalf("global LRU=%+v", victims)
	}
	pool.remove(two) // Rebinding removes an entry before another root admits work.
	if victims := pool.add(a, three, 200<<20); len(victims) != 0 {
		t.Fatal("active entry remained charged")
	}
	pool.remove(two)
	if pool.used != 200<<20 {
		t.Fatalf("budget=%d", pool.used)
	}
}
