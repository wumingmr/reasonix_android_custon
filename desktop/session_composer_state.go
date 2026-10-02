package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"reasonix/desktop/internal/sessionui"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

type SessionComposerState struct {
	Ref                   session.SessionRef `json:"ref"`
	Revision              string             `json:"revision"`
	ContentJSON           string             `json:"contentJson"`
	ContentVersion        int                `json:"contentVersion"`
	Baseline              string             `json:"baseline"`
	SubmissionID          string             `json:"submissionId,omitempty"`
	SubmissionPhase       string             `json:"submissionPhase,omitempty"`
	SubmissionRequest     string             `json:"submissionRequest,omitempty"`
	SubmissionFingerprint string             `json:"submissionFingerprint,omitempty"`
	SubmissionRevision    string             `json:"submissionRevision,omitempty"`
	HistoryChanged        bool               `json:"historyChanged"`
	Conflict              bool               `json:"conflict"`
}

type SessionComposerSaveRequest struct {
	Ref                session.SessionRef `json:"ref"`
	ExpectedRevision   string             `json:"expectedRevision"`
	ContentJSON        string             `json:"contentJson"`
	ContentVersion     int                `json:"contentVersion"`
	AcknowledgeHistory bool               `json:"acknowledgeHistory,omitempty"`
}

func composerRecordKey(ref session.SessionRef) string { return ref.HostID + ":" + ref.SessionID }

func (a *App) readSessionComposer(ref session.SessionRef) (SessionComposerState, error) {
	if err := validateLocalSessionRef(ref); err != nil {
		return SessionComposerState{}, err
	}
	r, err := a.sessionUIStore().Get(a.bootContext(), "composer", composerRecordKey(ref))
	view := SessionComposerState{Ref: ref, Revision: "0", ContentJSON: "{}", ContentVersion: 1}
	if err != nil {
		return view, err
	}
	if r.Revision != "0" {
		if err := json.Unmarshal(r.Payload, &view); err != nil {
			return view, err
		}
		if view.ContentVersion != 1 {
			return view, errors.New("unsupported composer content version")
		}
	}
	view.Revision = r.Revision
	return view, nil
}

// Follow user history as well as receipts: imported and older submit paths can
// advance history without a SubmissionID. Model settings and streaming assistant
// output must not invalidate input while the user composes the next turn.
func (a *App) composerBaseline(ref session.SessionRef) (string, error) {
	snapshot, err := a.desktopSessionService("").Query().Snapshot(a.bootContext(), ref)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(snapshot.Projection.Submissions)
	if err != nil {
		return "", err
	}
	var receipts map[string]json.RawMessage
	if err := json.Unmarshal(payload, &receipts); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(receipts))
	for key := range receipts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	users := []json.RawMessage{}
	for _, message := range snapshot.Projection.Messages {
		if message.Role == "user" {
			encoded, err := json.Marshal(message)
			if err != nil {
				return "", err
			}
			users = append(users, encoded)
		}
	}
	encoded, _ := json.Marshal(struct {
		Receipts []string
		Users    []json.RawMessage
	}{keys, users})
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func (a *App) GetSessionComposerState(ref session.SessionRef) (result SessionComposerState, err error) {
	defer func() { err = sessionUIError(err, composerRecordKey(ref), "") }()
	defer a.lockRuntimeMutation("read composer")()
	return a.getSessionComposerState(ref)
}

func (a *App) getSessionComposerState(ref session.SessionRef) (SessionComposerState, error) {
	view, err := a.readSessionComposer(ref)
	if err != nil {
		return view, err
	}
	baseline, err := a.composerBaseline(ref)
	if err != nil {
		return view, err
	}
	if view.SubmissionID != "" {
		snapshot, snapshotErr := a.desktopSessionService("").Query().Snapshot(a.bootContext(), ref)
		if snapshotErr != nil {
			return view, snapshotErr
		}
		_, accepted := snapshot.Projection.Submissions.Lookup(ref.SessionID, view.SubmissionID)
		if !accepted {
			accepted, err = a.composerGuidanceAccepted(view)
			if err != nil {
				return view, err
			}
		}
		if accepted && snapshot.DurableSequence >= snapshot.EventSequence {
			submission := view
			submission.SubmissionPhase = "accepted"
			view.ContentJSON, view.SubmissionID, view.SubmissionPhase = "{}", "", ""
			view.SubmissionRequest, view.SubmissionFingerprint = "", ""
			view.Baseline = baseline
			return a.saveComposerSettlement(view, submission)
		}
		view.SubmissionPhase = "unknown"
	}
	view.HistoryChanged = view.Revision != "0" && view.Baseline != "" && view.Baseline != baseline && draftHasContent(view.ContentJSON)
	if view.Revision == "0" {
		view.Baseline = baseline
	}
	return view, nil
}

func (a *App) saveComposerView(view SessionComposerState) (SessionComposerState, error) {
	return a.saveComposerSettlement(view, view)
}

func (a *App) saveComposerSettlement(view, submission SessionComposerState) (SessionComposerState, error) {
	payload, err := json.Marshal(view)
	if err != nil {
		return view, err
	}
	companions := []sessionui.Record{}
	if submission.SubmissionID != "" {
		receipt, marshalErr := json.Marshal(submission)
		if marshalErr != nil {
			return view, marshalErr
		}
		companions = append(companions, sessionui.Record{Key: composerRecordKey(view.Ref) + "/" + submission.SubmissionID, Payload: receipt})
	}
	r, err := a.sessionUIStore().Save(a.bootContext(), "composer", composerRecordKey(view.Ref), view.Revision, payload, companions...)
	if errors.Is(err, sessionui.ErrConflict) {
		if decodeErr := json.Unmarshal(r.Payload, &view); decodeErr != nil {
			return view, decodeErr
		}
		view.Revision, view.Conflict = r.Revision, true
		return view, nil
	}
	if err == nil {
		view.Revision = r.Revision
	}
	return view, err
}

func (a *App) SaveSessionComposerState(req SessionComposerSaveRequest) (result SessionComposerState, err error) {
	defer func() { err = sessionUIError(err, composerRecordKey(req.Ref), "") }()
	defer a.lockRuntimeMutation("save composer")()
	if req.ContentVersion != 1 || !json.Valid([]byte(req.ContentJSON)) {
		return SessionComposerState{}, errors.New("invalid composer content")
	}
	view, err := a.readSessionComposer(req.Ref)
	if err != nil {
		return view, err
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return view, err
	}
	if state.SessionStates[req.Ref.SessionID].Lifecycle == workspacestate.Deleted {
		return view, session.ErrSessionNotFound
	}
	if view.SubmissionID != "" {
		return view, errors.New("check the pending submission before editing")
	}
	baseline, err := a.composerBaseline(req.Ref)
	if err != nil {
		return view, err
	}
	if view.Revision != "0" && view.Baseline != baseline && draftHasContent(view.ContentJSON) && !req.AcknowledgeHistory {
		view.HistoryChanged = true
		return view, nil
	}
	view.Revision, view.ContentJSON, view.ContentVersion = req.ExpectedRevision, req.ContentJSON, req.ContentVersion
	view.Baseline, view.HistoryChanged, view.Conflict = baseline, false, false
	return a.saveComposerView(view)
}

// BeginSessionComposerSubmission associates an already-saved source with the
// ordinary identified submission. It does not admit or execute work.
func (a *App) BeginSessionComposerSubmission(ref session.SessionRef, revision, submissionID, requestJSON string) (result SessionComposerState, err error) {
	defer func() { err = sessionUIError(err, composerRecordKey(ref), submissionID) }()
	defer a.lockRuntimeMutation("begin composer submission")()
	view, err := a.getSessionComposerState(ref)
	if err != nil {
		return view, err
	}
	if view.SubmissionID != "" {
		if view.SubmissionID == submissionID && view.SubmissionRevision == revision && view.SubmissionRequest == requestJSON {
			return view, nil
		}
		return view, errors.New("another submission owns this input")
	}
	if view.Revision != revision || view.HistoryChanged || view.Conflict {
		return view, sessionui.ErrConflict
	}
	if strings.TrimSpace(submissionID) == "" {
		return view, errors.New("submission identity is required")
	}
	if !json.Valid([]byte(requestJSON)) {
		return view, errors.New("invalid frozen submission request")
	}
	previous, err := a.sessionUIStore().Get(a.bootContext(), "submission", composerRecordKey(ref)+"/"+submissionID)
	if err != nil {
		return view, err
	}
	if previous.Revision != "0" {
		return view, errors.New("submission identity already belongs to another input version")
	}
	view.SubmissionRequest = requestJSON
	view.SubmissionRevision = revision
	view.SubmissionFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(requestJSON)))
	view.SubmissionID, view.SubmissionPhase = submissionID, "pending"
	return a.saveComposerView(view)
}

// CompleteSessionComposerSubmission is called only after the existing send
// command returns its admission result. A lost response is reconciled by Get;
// callers never infer non-acceptance from an absent transcript row.
func (a *App) CompleteSessionComposerSubmission(ref session.SessionRef, submissionID, outcome string) (result SessionComposerState, err error) {
	defer func() { err = sessionUIError(err, composerRecordKey(ref), submissionID) }()
	defer a.lockRuntimeMutation("complete composer submission")()
	view, err := a.readSessionComposer(ref)
	if err != nil {
		return view, err
	}
	if view.SubmissionID == "" {
		return view, nil
	}
	if view.SubmissionID != submissionID {
		return view, sessionui.ErrConflict
	}
	submission := view
	submission.SubmissionPhase = outcome
	switch outcome {
	case "accepted":
		view.ContentJSON, view.SubmissionID, view.SubmissionPhase = "{}", "", ""
		view.SubmissionRequest, view.SubmissionFingerprint = "", ""
	case "not_accepted":
		view.SubmissionID, view.SubmissionPhase = "", ""
	case "unknown":
		view.SubmissionPhase = "unknown"
	default:
		return view, errors.New("invalid submission outcome")
	}
	view.Baseline, err = a.composerBaseline(ref)
	if err != nil {
		return view, err
	}
	return a.saveComposerSettlement(view, submission)
}
