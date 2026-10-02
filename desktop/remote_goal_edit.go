package main

import "encoding/json"

func (a *App) EditRemoteTabGoal(tabID, objective string, maxGoalRounds *uint64) error {
	if err := a.requireRemoteExecutionProtocol(tabID); err != nil {
		return err
	}
	if err := a.requireRemoteGoalLifecycle(tabID); err != nil {
		return err
	}
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"objective": objective, "maxGoalRounds": maxGoalRounds})
	return servePostForSession(ctx, client, serveURL(base, "/goal/edit"), body, expectedPath)
}
