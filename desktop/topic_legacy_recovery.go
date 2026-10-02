package main

import (
	"os"
	"path/filepath"

	"reasonix/internal/agent"
)

func sessionOrderInfoIsHiddenRecovery(info agent.SessionOrderInfo, parentDir string) bool {
	return legacyRecoveryParentPresent(info.Path, parentDir) ||
		sessionOrderInfoIsUnmodifiedRecoveryCopy(info, parentDir)
}

func legacyRecoveryParentPresent(path, parentDir string) bool {
	parentID, ok := agent.RecoveryFilenameParentID(path)
	if !ok {
		return false
	}
	parentPath := filepath.Join(parentDir, parentID+".jsonl")
	if sameDesktopPath(path, parentPath) {
		return false
	}
	info, err := os.Stat(parentPath)
	return err == nil && !info.IsDir()
}
