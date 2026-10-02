package main

import "testing"

func TestGlobalFolderAfterProjectCreatedFollowsGlobalConversations(t *testing.T) {
	cases := []struct {
		name       string
		openGlobal func(*App) error
		wantGlobal bool
	}{
		{name: "untouched", openGlobal: func(*App) error { return nil }},
		{name: "draft target without a conversation", openGlobal: func(a *App) error {
			_, _ = a.OpenSessionDraftForTarget("global", "")
			return nil
		}},
		{name: "blank conversation", wantGlobal: true, openGlobal: func(a *App) error {
			_, err := a.EnsureBlankSurface("global", "")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			app := NewApp()
			if err := tc.openGlobal(app); err != nil {
				t.Fatalf("open global surface: %v", err)
			}
			if _, err := app.SwitchWorkspace(t.TempDir()); err != nil {
				t.Fatalf("SwitchWorkspace: %v", err)
			}
			gotGlobal := false
			for _, node := range mustListProjectTree(t, app) {
				gotGlobal = gotGlobal || node.Kind == "global_folder"
			}
			if gotGlobal != tc.wantGlobal {
				t.Fatalf("Global folder listed = %v, want %v after a project was created", gotGlobal, tc.wantGlobal)
			}
		})
	}
}
