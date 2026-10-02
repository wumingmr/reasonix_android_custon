package main

import (
	"encoding/json"
	"strings"
)

// seedPreviousDraftForTarget models an installation that created a draft before
// the manual-new rollback. Production APIs may only reopen this stored record.
func (a *App) seedPreviousDraftForTarget(scope, root string) (SessionDraftView, error) {
	id, err := a.ensureDesktopWorkspace(a.bootContext(), scope, root)
	if err != nil {
		return SessionDraftView{}, err
	}
	settings, err := json.Marshal(a.defaultDraftSettings(scope, root))
	if err != nil {
		return SessionDraftView{}, err
	}
	_, _, err = a.draftStore().Open(a.bootContext(), id, scope, root, "draft-"+strings.TrimPrefix(newTabID(), "tab_"), string(settings))
	if err != nil {
		return SessionDraftView{}, err
	}
	return a.OpenSessionDraftForTarget(scope, root)
}
