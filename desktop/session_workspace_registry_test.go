package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

type registrySessionCreator struct{ service *session.Service }

func (c registrySessionCreator) BindFreshSession(ctx context.Context, sessionID string) (session.SessionRef, error) {
	runtime, err := c.service.Create(ctx, session.CreateOptions{SessionID: sessionID})
	if err != nil {
		return session.SessionRef{}, err
	}
	return runtime.Ref(), nil
}

func TestGlobalWorkspaceEquivalentRootAllowsCreateAndRestart(t *testing.T) {
	aliases := []string{"cleaned"}
	if runtime.GOOS == "windows" {
		aliases = append(aliases, "case", "separators")
	}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := globalWorkspaceRoot()
			savedRoot := root + string(os.PathSeparator) + "."
			switch alias {
			case "case":
				savedRoot = strings.ToUpper(root)
			case "separators":
				savedRoot = strings.ReplaceAll(root, `\`, "/")
			}
			var ref session.SessionRef
			for attempt := range 2 {
				app := NewApp()
				app.ctx = t.Context()
				t.Cleanup(app.closeSessionServices)
				if attempt == 0 {
					if err := app.workspaceRegistry().EnsureWorkspace(t.Context(), workspacestate.Workspace{
						ID: workspacestate.GlobalWorkspaceID, Root: savedRoot, Title: "My Global", Visible: true,
					}); err != nil {
						t.Fatal(err)
					}
				}
				ctrl := control.New(control.Options{
					Executor:       agent.New(nil, nil, agent.NewSession("test"), agent.Options{}, event.Discard),
					SessionService: app.desktopSessionService(""), ExclusiveSession: true,
				})
				t.Cleanup(ctrl.Close)
				got, workspaceID, err := app.bindTabCanonicalSession(t.Context(), ctrl, &config.Config{}, "global", "", ref.SessionID, "", "", false)
				if err != nil || workspaceID != workspacestate.GlobalWorkspaceID {
					t.Fatalf("create/reopen attempt %d: workspace=%q, err=%v", attempt, workspaceID, err)
				}
				if attempt == 0 {
					ref = got
					live, ok := app.desktopSessionService("").Runtime(ref)
					if !ok {
						t.Fatal("created session has no runtime")
					}
					appendSessionTestMessage(t, live, "user-message", provider.Message{ID: "user-message", Role: provider.RoleUser, Content: "preserved Global history"})
				} else {
					if got != ref {
						t.Fatalf("restart replaced session: got=%v want=%v", got, ref)
					}
					page, err := app.ReadSessionHistory(ref, "", 10)
					if err != nil || len(page.Messages) == 0 || page.Messages[len(page.Messages)-1].Content != "preserved Global history" {
						t.Fatalf("reopened history=%+v err=%v", page, err)
					}
					if _, err := app.canonicalSessionWorkspace(t.Context(), ref); err != nil {
						t.Fatalf("navigation membership: %v", err)
					}
				}
				state, err := app.workspaceRegistry().Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				workspace := state.Workspaces[workspaceID]
				if workspace.Root != savedRoot || workspace.Title != "My Global" || len(workspace.SessionIDs) != 1 || workspace.SessionIDs[0] != ref.SessionID || len(state.PendingCreates) != 0 {
					t.Fatalf("unexpected persisted workspace: %+v, pending=%+v", workspace, state.PendingCreates)
				}
				ctrl.Close()
				app.closeSessionServices()
			}
		})
	}
}

func (c registrySessionCreator) BindFreshSessionWithOptions(ctx context.Context, options session.CreateOptions) (session.SessionRef, error) {
	runtime, err := c.service.Create(ctx, options)
	if err != nil {
		return session.SessionRef{}, err
	}
	return runtime.Ref(), nil
}

func TestFreshDesktopSessionIsDurableRegistryMemberBeforeReturn(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	service := app.desktopSessionService(filepath.Join(root, "old-project-sessions"))
	project := filepath.Join(root, "project")

	ref, workspaceID, err := app.bindFreshDesktopSession(t.Context(), "project", project, registrySessionCreator{service: service})
	if err != nil {
		t.Fatalf("bindFreshDesktopSession: %v", err)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatalf("Load workspace state: %v", err)
	}
	workspace := state.Workspaces[workspaceID]
	if len(workspace.SessionIDs) != 1 || workspace.SessionIDs[0] != ref.SessionID {
		t.Fatalf("workspace sessions = %#v, ref = %#v", workspace.SessionIDs, ref)
	}
	if len(state.PendingCreates) != 0 {
		t.Fatalf("pending creates = %#v", state.PendingCreates)
	}
	page, err := service.Query().List(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("List sessions: %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != ref.SessionID || page.Sessions[0].CWD != project {
		t.Fatalf("sessions = %#v", page.Sessions)
	}
}

func TestWorkspaceRegistryRejectsSessionHeaderFromDifferentWorkspace(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	service := app.desktopSessionService("")
	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	runtime, err := service.Create(t.Context(), session.CreateOptions{
		SessionID: "belongs-to-a", CWD: projectA, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := app.attachDesktopSession(t.Context(), "project", projectB, runtime.Ref()); err == nil {
		t.Fatal("attaching a session to a workspace that disagrees with its immutable header succeeded")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace := state.Workspaces[desktopWorkspaceID("project", projectB)]
	if len(workspace.SessionIDs) != 0 {
		t.Fatalf("mismatched workspace members = %#v", workspace.SessionIDs)
	}
}

// A portable build or REASONIX_HOME move leaves Global's stored root and every
// global session's recorded CWD at the old data directory (#10638, #10659).
func TestGlobalWorkspaceSurvivesMovedDataDirectory(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	oldGlobal := filepath.Join(t.TempDir(), "old-home", "global-workspace")
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	store := app.desktopSessions.workspaceState
	if err := store.EnsureWorkspace(t.Context(), workspacestate.Workspace{ID: workspacestate.GlobalWorkspaceID, Root: oldGlobal, Visible: true}); err != nil {
		t.Fatal(err)
	}
	created, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{
		SessionID: "moved-global", CWD: oldGlobal, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(t.Context(), "", workspacestate.GlobalWorkspaceID, created.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}

	if id, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil || id != workspacestate.GlobalWorkspaceID {
		t.Fatalf("ensure global = %q, %v", id, err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Workspaces[workspacestate.GlobalWorkspaceID].Root; got != globalWorkspaceRoot() {
		t.Fatalf("global root = %q, want %q", got, globalWorkspaceRoot())
	}
	owner, err := app.canonicalSessionWorkspace(t.Context(), created.Ref())
	if err != nil || owner.ID != workspacestate.GlobalWorkspaceID {
		t.Fatalf("session recorded under the old global root = %q, %v", owner.ID, err)
	}
}
