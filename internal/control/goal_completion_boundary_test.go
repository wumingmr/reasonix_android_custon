package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInactiveLegacyGoalDoesNotWriteOrActivate(t *testing.T) {
	c, _, _ := goalRuntimeController(t, &scriptedTurns{}, nil)
	path := filepath.Join(t.TempDir(), "goal.json")
	c.goals.statePath = path
	c.LoadInactiveGoal("legacy metadata objective")
	if c.Goal() != "legacy metadata objective" || c.goals.active() {
		t.Fatal("legacy objective was dropped or activated")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only load wrote a sidecar: %v", err)
	}
	scope := c.goals.scopeID
	c.SetGoal("legacy metadata objective")
	if !c.goals.active() || c.goals.scopeID != scope {
		t.Fatal("explicit start did not preserve and activate restored objective")
	}
}
