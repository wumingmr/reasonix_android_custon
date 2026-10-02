package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"reasonix/internal/fileutil"
)

var errTabsSnapshotChanged = errors.New("desktop tabs changed during startup reconciliation")

func (a *App) loadTabsForRestore() (desktopTabsFile, uint64) {
	version := a.tabsSnapshotVersion()
	file := loadTabsFile()
	a.rememberTabsFileExtra(file.extra)
	return file, version
}

// saveTabsWrite writes the tab snapshot outside a.mu. tabsSaveMu serializes
// writes because every save shares one destination and fixed temporary path.
func (a *App) saveTabsWrite(dir string, entries []desktopTabEntry, activeID string, version uint64) {
	a.tabsSaveMu.Lock()
	defer a.tabsSaveMu.Unlock()
	if version < a.tabsLastWrittenVersion {
		return
	}
	localIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		localIDs = append(localIDs, entry.ID)
	}
	remoteEntries, remoteOrder, tabOrder, remoteActive := a.remoteTabsFileEntries(localIDs)
	if remoteActive != "" {
		activeID = remoteActive
	}
	file := desktopTabsFile{
		Tabs: entries, ActiveTab: activeID, RemoteTabs: remoteEntries,
		RemoteTabOrder: remoteOrder, TabOrder: tabOrder,
		extra: cloneDesktopJSONFields(a.tabsFileExtra),
	}
	_ = a.writeTabsFileLocked(dir, file, version)
}

func (a *App) rememberTabsFileExtra(extra map[string]json.RawMessage) {
	a.tabsSaveMu.Lock()
	a.tabsFileExtra = cloneDesktopJSONFields(extra)
	a.tabsSaveMu.Unlock()
}

func (a *App) tabsSnapshotVersion() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tabsSaveVersion
}

func (a *App) tabsSnapshotCurrent(version uint64) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tabsSaveVersion == version
}

func (a *App) persistReconciledTabsFile(file desktopTabsFile, expectedVersion uint64) (uint64, error) {
	a.mu.Lock()
	if a.tabsSaveVersion != expectedVersion {
		a.mu.Unlock()
		return 0, errTabsSnapshotChanged
	}
	a.tabsSaveVersion++
	version := a.tabsSaveVersion
	a.mu.Unlock()

	a.tabsSaveMu.Lock()
	defer a.tabsSaveMu.Unlock()
	if version < a.tabsLastWrittenVersion {
		return 0, errTabsSnapshotChanged
	}
	file.extra = cloneDesktopJSONFields(a.tabsFileExtra)
	if err := a.writeTabsFileLocked(desktopConfigDir(), file, version); err != nil {
		return version, err
	}
	return version, nil
}

// writeTabsFileLocked writes an already-reconciled snapshot under tabsSaveMu.
func (a *App) writeTabsFileLocked(dir string, file desktopTabsFile, version uint64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, tabsFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := fileutil.ReplaceFile(tmp, path); err != nil {
		return err
	}
	a.tabsLastWrittenVersion = version
	return nil
}
