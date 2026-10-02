package main

import (
	"reasonix/internal/session"
	"strconv"
)

// SessionActivityBaseline is an explicit owner observation. Query.Stat validates
// cold metadata against the log revision and schedules bounded asynchronous
// rebuilding when needed; the sidebar never replays the transcript itself.
type SessionActivityBaseline struct {
	Ref                 session.SessionRef `json:"ref"`
	ResultSequence      uint64             `json:"resultSequence"`
	EventVersion        string             `json:"eventVersion"`
	LifecycleGeneration uint64             `json:"lifecycleGeneration"`
	Complete            bool               `json:"complete"`
}

func (a *App) GetSessionActivityBaseline(selector SessionSelector) (SessionActivityBaseline, error) {
	target, err := a.resolveSessionTarget(selector)
	if err != nil {
		return SessionActivityBaseline{}, err
	}
	if target.SessionRef.SessionID == "" {
		return SessionActivityBaseline{}, newSessionOperationError("unsupported", "This history source has no canonical result sequence.")
	}
	info, err := a.desktopSessionService("").Query().Stat(a.bootContext(), target.SessionRef)
	if err != nil {
		return SessionActivityBaseline{}, err
	}
	return SessionActivityBaseline{Ref: target.SessionRef, ResultSequence: info.ResultSequence,
		EventVersion: strconv.FormatUint(info.EventSequence, 10), LifecycleGeneration: target.LifecycleGeneration,
		Complete: info.MetadataStatus == session.MetadataReady && info.Error == ""}, nil
}
