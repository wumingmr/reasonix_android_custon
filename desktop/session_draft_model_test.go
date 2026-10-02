package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/draftstate"
	"reasonix/internal/config"
)

func writeDraftDefaultModelConfig(t *testing.T, model string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`default_model = "fixture/%s"

[desktop]
provider_access = ["fixture"]

[[providers]]
name = "fixture"
kind = "openai"
base_url = "https://example.invalid/v1"
models = ["model-a", "model-b"]
default = "model-a"
api_key_env = "DRAFT_DEFAULT_MODEL_KEY"
`, model)
	if err := os.WriteFile(config.UserConfigPath(), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.SetCredential("DRAFT_DEFAULT_MODEL_KEY", "test-key"); err != nil {
		t.Fatal(err)
	}
}

func TestInheritedDraftModelFollowsDefaultUntilExplicitlySelected(t *testing.T) {
	isolateDesktopUserDirs(t)
	writeDraftDefaultModelConfig(t, "model-a")
	a := newDraftTestApp(t)
	root := t.TempDir()

	draft, err := a.seedPreviousDraftForTarget("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Settings.Model != "fixture/model-a" || draft.Settings.ModelSource != draftModelSourceDefault {
		t.Fatalf("initial model = %+v, want inherited fixture/model-a", draft.Settings)
	}

	writeDraftDefaultModelConfig(t, "model-b")
	reopened, err := a.OpenSessionDraftForTarget("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != draft.ID || reopened.Settings.Model != "fixture/model-b" || reopened.Settings.ModelSource != draftModelSourceDefault {
		t.Fatalf("reopened draft = %+v, want same draft following fixture/model-b", reopened)
	}

	explicit := reopened.Settings
	explicit.Model = "fixture/model-a"
	explicit.ModelSource = draftModelSourceExplicit
	saved, err := a.SaveSessionDraft(SessionDraftSaveRequest{
		DraftID: reopened.ID, Revision: reopened.Revision, ContentJSON: reopened.ContentJSON, Settings: explicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := a.OpenSessionDraftForTarget("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if final.Revision != saved.Draft.Revision || final.Settings.Model != "fixture/model-a" || final.Settings.ModelSource != draftModelSourceExplicit {
		t.Fatalf("explicit model was not retained: %+v", final.Settings)
	}
}

func TestUntouchedLegacyDraftMigratesToLiveDefaultButEditedLegacyDraftDoesNot(t *testing.T) {
	isolateDesktopUserDirs(t)
	writeDraftDefaultModelConfig(t, "model-a")
	a := newDraftTestApp(t)

	openLegacy := func(root, workspaceID, draftID string) draftstate.Draft {
		t.Helper()
		settings := a.defaultDraftSettings("project", root)
		settings.ModelSource = ""
		payload, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		record, _, err := a.draftStore().Open(t.Context(), workspaceID, "project", root, draftID, string(payload))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}

	untouchedRoot := t.TempDir()
	untouchedWorkspace, err := a.ensureDesktopWorkspace(t.Context(), "project", untouchedRoot)
	if err != nil {
		t.Fatal(err)
	}
	untouched := openLegacy(untouchedRoot, untouchedWorkspace, "legacy-untouched")

	editedRoot := t.TempDir()
	editedWorkspace, err := a.ensureDesktopWorkspace(t.Context(), "project", editedRoot)
	if err != nil {
		t.Fatal(err)
	}
	edited := openLegacy(editedRoot, editedWorkspace, "legacy-edited")
	edited, err = a.draftStore().Save(t.Context(), edited.ID, edited.Revision, `{"text":"saved"}`, edited.SettingsJSON, false)
	if err != nil {
		t.Fatal(err)
	}

	writeDraftDefaultModelConfig(t, "model-b")
	migrated, err := a.OpenSessionDraftForTarget("project", untouchedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.ID != untouched.ID || migrated.Settings.Model != "fixture/model-b" || migrated.Settings.ModelSource != draftModelSourceDefault {
		t.Fatalf("untouched legacy draft = %+v, want migrated live default", migrated)
	}
	preserved, err := a.OpenSessionDraftForTarget("project", editedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ID != edited.ID || preserved.Settings.Model != "fixture/model-a" || preserved.Settings.ModelSource != "" {
		t.Fatalf("edited legacy draft = %+v, want preserved concrete model", preserved)
	}
}

func TestLegacyDraftWithOperationKeepsFrozenModelAndStillOpens(t *testing.T) {
	isolateDesktopUserDirs(t)
	writeDraftDefaultModelConfig(t, "model-a")
	a := newDraftTestApp(t)
	root := t.TempDir()
	workspaceID, err := a.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	settings := a.defaultDraftSettings("project", root)
	settings.ModelSource = ""
	payload, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	draft, _, err := a.draftStore().Open(t.Context(), workspaceID, "project", root, "legacy-operation", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(SessionDraftSubmissionRequest{
		SnapshotVersion: 3,
		Settings:        settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.draftStore().BeginOperation(t.Context(), draftstate.Operation{
		ID: "legacy-operation-op", DraftID: draft.ID, WorkspaceID: workspaceID, DraftRevision: draft.Revision,
		SessionID: "legacy-operation-session", TopicID: "legacy-operation-topic",
		SubmissionID: "legacy-operation-submission", Fingerprint: "legacy-operation-fingerprint", RequestJSON: string(request),
	}); err != nil {
		t.Fatal(err)
	}

	writeDraftDefaultModelConfig(t, "model-b")
	reopened, err := a.OpenSessionDraftForTarget("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Revision != draft.Revision || reopened.Settings.Model != "fixture/model-a" || reopened.Settings.ModelSource != "" {
		t.Fatalf("operation-owned legacy draft changed during migration: %+v", reopened)
	}
	op, err := a.draftStore().Operation(t.Context(), "legacy-operation-op")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := a.draftOperationSettings(op)
	if err != nil || frozen.Model != "fixture/model-a" {
		t.Fatalf("frozen operation model = %+v, err %v", frozen, err)
	}
}

func TestDraftSubmissionFingerprintIgnoresInheritedDefaultMirror(t *testing.T) {
	base := SessionDraftSubmissionRequest{DraftID: "draft", Revision: 1, Display: "hi", Input: "hi",
		Settings: SessionDraftSettings{Model: "fixture/model-a", ModelSource: draftModelSourceDefault}}
	first, _, err := draftSubmissionFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Settings.Model = "fixture/model-b"
	second, _, err := draftSubmissionFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("inherited mirror changed request identity: %s != %s", first, second)
	}
	base.Settings.ModelSource = draftModelSourceExplicit
	explicit, _, err := draftSubmissionFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if explicit == second {
		t.Fatal("explicit model did not participate in request identity")
	}
}

func TestDraftAdmissionPreservesModelAliasRequestIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	writeDraftDefaultModelConfig(t, "model-a")
	a := newDraftTestApp(t)
	draft, err := a.seedPreviousDraftForTarget("project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := draft.Settings
	settings.Model, settings.ModelSource = "model-a", draftModelSourceExplicit
	saved, err := a.SaveSessionDraft(SessionDraftSaveRequest{
		DraftID: draft.ID, Revision: draft.Revision, ContentJSON: `{"text":"hello"}`, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := SessionDraftSubmissionRequest{
		SnapshotVersion: draftstate.SnapshotVersion, DraftID: draft.ID, Revision: saved.Draft.Revision,
		SourceDigest: saved.Draft.SnapshotDigest, Display: "hello", Input: "hello", Settings: settings,
	}
	fingerprint, _, err := draftSubmissionFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	// Seed an already admitted operation to exercise admission without starting a
	// provider. Its execution snapshot is canonical, but identity belongs to the
	// original wire request, including its explicit /model alias.
	frozen := request
	frozen.Settings.Model = "fixture/model-a"
	_, payload, err := draftSubmissionFingerprint(frozen)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := a.draftStore().BeginOperation(t.Context(), draftstate.Operation{
		ID: "alias-operation", RequestID: "original-request", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID,
		DraftRevision: saved.Draft.Revision, SourceDigest: saved.Draft.SnapshotDigest,
		SessionID: "alias-session", TopicID: "alias-topic", SubmissionID: "alias-submission",
		Fingerprint: fingerprint, RequestJSON: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.draftStore().SetOperationPhase(t.Context(), op.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"original-request", "second-equivalent-request"} {
		request.RequestID = requestID
		got, err := a.BeginDraftSubmission(request)
		if err != nil || got.OperationID != op.ID {
			t.Fatalf("equivalent request %s: %+v, %v", requestID, got, err)
		}
	}
	request.RequestID, request.Input = "original-request", "different input"
	if _, err := a.BeginDraftSubmission(request); !errors.Is(err, draftstate.ErrOperationConflict) {
		t.Fatalf("changed retry payload = %v, want operation conflict", err)
	}
}

func TestDraftRecoveryProjectsFrozenModelUntilEditingResumes(t *testing.T) {
	isolateDesktopUserDirs(t)
	writeDraftDefaultModelConfig(t, "model-a")
	a := newDraftTestApp(t)
	draft, err := a.seedPreviousDraftForTarget("project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := SessionDraftSubmissionRequest{SnapshotVersion: draftstate.SnapshotVersion, Settings: draft.Settings}
	_, payload, err := draftSubmissionFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := a.draftStore().BeginOperation(t.Context(), draftstate.Operation{
		ID: "frozen-view-operation", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision,
		SessionID: "frozen-view-session", TopicID: "frozen-view-topic", SubmissionID: "frozen-view-submission",
		Fingerprint: "frozen-view", RequestJSON: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeDraftDefaultModelConfig(t, "model-b")
	for _, phase := range []string{"reserved", "resume_required", "runtime_failed", "terminal_failed", "cancelled"} {
		if _, err := a.draftStore().SetOperationPhase(t.Context(), op.ID, phase, ""); err != nil {
			t.Fatal(err)
		}
		want := "fixture/model-a"
		if phase == "terminal_failed" || phase == "cancelled" {
			want = "fixture/model-b"
		}
		state, err := a.GetSessionDraftState(draft.ID)
		if err != nil || state.Draft.Settings.Model != want {
			t.Fatalf("%s state model = %q, %v; want %s", phase, state.Draft.Settings.Model, err, want)
		}
		current := ""
		for _, model := range a.ModelsForDraft(draft.ID) {
			if model.Current {
				current = model.Ref
			}
		}
		if current != want {
			t.Fatalf("%s picker model = %q; want %s (catalog: %+v)", phase, current, want, a.ModelsForDraft(draft.ID))
		}
	}
}
