package control

import (
	"sync"
	"testing"

	"reasonix/internal/taskcontract"
)

func TestSetQualityFloorNormalizesVocabulary(t *testing.T) {
	c := newOwnedTestController(t, Options{Label: "floor"})
	cases := map[string]string{
		"":           QualityFloorStandard,
		"standard":   QualityFloorStandard,
		"balanced":   QualityFloorStandard,
		"full":       QualityFloorStandard,
		"light":      QualityFloorStandard,
		"economy":    QualityFloorStandard,
		"eco":        QualityFloorStandard,
		"lite":       QualityFloorStandard,
		"minimal":    QualityFloorStandard,
		"delivery":   QualityFloorStandard,
		"deliver":    QualityFloorStandard,
		"quality":    QualityFloorStandard,
		" DELIVERY ": QualityFloorStandard,
	}
	for raw, want := range cases {
		if err := c.SetQualityFloor(raw); err != nil {
			t.Fatalf("SetQualityFloor(%q): %v", raw, err)
		}
		if got := c.QualityFloor(); got != want {
			t.Fatalf("SetQualityFloor(%q) = %q, want %q", raw, got, want)
		}
	}
	if err := c.SetQualityFloor(QualityFloorDelivery); err != nil {
		t.Fatalf("SetQualityFloor(delivery): %v", err)
	}
	if err := c.SetQualityFloor("turbo"); err == nil {
		t.Fatal("unknown floor value must error")
	}
	if got := c.QualityFloor(); got != QualityFloorStandard {
		t.Fatalf("rejected value must not change state, got %q", got)
	}
}

func TestRetiredQualityFloorNeverReachesTurnConstraints(t *testing.T) {
	c := newOwnedTestController(t, Options{Label: "floor"})
	if got := c.qualityFloorConstraint(); got != taskcontract.PolicyFloorNone {
		t.Fatalf("default floor constraint = %v, want none", got)
	}
	if err := c.SetQualityFloor(QualityFloorDelivery); err != nil {
		t.Fatalf("SetQualityFloor: %v", err)
	}
	if got := c.qualityFloorConstraint(); got != taskcontract.PolicyFloorNone {
		t.Fatalf("floor constraint = %v, want none", got)
	}
}

func TestSetQualityFloorConcurrentWithReads(t *testing.T) {
	c := newOwnedTestController(t, Options{Label: "floor"})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			floor := QualityFloorStandard
			if i%2 == 0 {
				floor = QualityFloorDelivery
			}
			_ = c.SetQualityFloor(floor)
		}(i)
		go func() {
			defer wg.Done()
			if got := c.QualityFloor(); got != QualityFloorStandard {
				t.Errorf("QualityFloor returned %q, want standard", got)
			}
			_ = c.qualityFloorConstraint()
		}()
	}
	wg.Wait()
}
