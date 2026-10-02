package main

import "reasonix/internal/session"

type WorkspaceSessionSummary struct {
	Ref             session.SessionRef `json:"ref"`
	WorkspaceID     string             `json:"workspaceId"`
	Title           string             `json:"title"`
	Preview         string             `json:"preview"`
	Turns           int                `json:"turns"`
	CreatedAt       int64              `json:"createdAt"`
	UpdatedAt       int64              `json:"updatedAt"`
	ResultSequence  uint64             `json:"resultSequence,omitempty"`
	ModelRef        string             `json:"modelRef,omitempty"`
	ParentSessionID string             `json:"parentSessionId,omitempty"`
	Origin          string             `json:"origin,omitempty"`
	Blank           bool               `json:"blank"`
	Archived        bool               `json:"archived"`
	Running         bool               `json:"running"`
	MetadataStatus  string             `json:"metadataStatus"`
	Health          string             `json:"health"`
}
