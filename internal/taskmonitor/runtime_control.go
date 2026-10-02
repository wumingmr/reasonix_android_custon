package taskmonitor

func killTaskRuntime(killer JobKiller, snap *TaskSnapshot) bool {
	if owned, ok := killer.(interface {
		KillOwned(string, string, string) bool
	}); ok {
		return owned.KillOwned(snap.SessionID, runtimeJobID(snap), snap.RuntimeOwnerID)
	}
	return killer.Kill(snap.SessionID, runtimeJobID(snap))
}
