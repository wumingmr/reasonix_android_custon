package main

import "errors"

// registerProjectRoot indexes workspaceRoot, realigns open tabs to its
// canonical spelling, and discovers existing sessions once per process.
func (a *App) registerProjectRoot(workspaceRoot string) error {
	if err := addProject(workspaceRoot, ""); errors.Is(err, errProjectStateCollisionAssignment) {
		return err
	}
	a.syncTabWorkspaceRootSpellings()
	root := normalizeProjectRoot(workspaceRoot)
	if root == "" {
		return nil
	}
	registrationKey := projectRootKey(root)
	if _, loaded := a.catalogRegisteredProjectRoots.LoadOrStore(registrationKey, struct{}{}); loaded {
		return nil
	}
	if !a.requestSessionCatalogReconcile(desktopSessionDir(root)) {
		a.catalogRegisteredProjectRoots.Delete(registrationKey)
	}
	return nil
}

func (a *App) beginRegisteredProjectRuntimeAdmission(tab *WorkspaceTab, scope, workspaceRoot string) (func(), error) {
	release, err := a.beginChangedProjectRuntimeAdmission(tab, scope, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if err := a.registerProjectRoot(workspaceRoot); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
