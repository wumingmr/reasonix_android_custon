package main

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/history"
	"reasonix/internal/historycatalog"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncatalog"
)

type HistorySessionPageRequest struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Status        string `json:"status"`
	TimeFilter    string `json:"timeFilter"`
	Query         string `json:"query"`
	Cursor        string `json:"cursor"`
	Limit         int    `json:"limit"`
}

type HistorySessionPage struct {
	SnapshotID        string        `json:"snapshotId,omitempty"`
	SnapshotExpiresAt int64         `json:"snapshotExpiresAt,omitempty"`
	ReadError         *ReadError    `json:"readError,omitempty"`
	Items             []SessionMeta `json:"items"`
	NextCursor        string        `json:"nextCursor"`
	Revision          uint64        `json:"revision"`
	Partial           bool          `json:"partial"`
	StaleCursor       bool          `json:"staleCursor"`
}

type HistorySearchRequest struct {
	Query         string   `json:"query"`
	Scope         string   `json:"scope"`
	WorkspaceRoot string   `json:"workspaceRoot,omitempty"`
	Status        string   `json:"status"`
	TimeFilter    string   `json:"timeFilter"`
	Kinds         []string `json:"kinds"`
	ToolName      string   `json:"toolName"`
	Cursor        string   `json:"cursor"`
	Limit         int      `json:"limit"`
}

type HistorySearchHit struct {
	PartIndex      int     `json:"partIndex,omitempty"`
	ContentDigest  string  `json:"contentDigest,omitempty"`
	SessionPath    string  `json:"sessionPath"`
	SessionID      string  `json:"sessionId"`
	Source         string  `json:"source"`
	MessageIndex   int     `json:"messageIndex"`
	Role           string  `json:"role"`
	Kind           string  `json:"kind"`
	ToolName       string  `json:"toolName,omitempty"`
	Snippet        string  `json:"snippet"`
	Score          float64 `json:"score"`
	SessionTitle   string  `json:"sessionTitle,omitempty"`
	TopicTitle     string  `json:"topicTitle,omitempty"`
	WorkspaceRoot  string  `json:"workspaceRoot,omitempty"`
	LastActivityAt int64   `json:"lastActivityAt"`
	Open           bool    `json:"open"`
	Running        bool    `json:"running"`
	Current        bool    `json:"current"`
}

type HistoryIndexStatus = historycatalog.Status

type HistoryIndexChangedV1 struct {
	Revision uint64   `json:"revision"`
	Indexed  int64    `json:"indexed"`
	Total    int64    `json:"total"`
	Pending  int64    `json:"pending"`
	Roots    []string `json:"roots"`
	Reason   string   `json:"reason"`
}

func (a *App) registerHistoryIndexEvents() {
	history.RegisterCatalogObserver(func(status historycatalog.Status, roots []string, reason string) {
		if roots == nil {
			roots = []string{}
		}
		a.emitRuntimeEvent("history-index:changed-v1", HistoryIndexChangedV1{Revision: status.Revision,
			Indexed: status.Indexed, Total: status.Total, Pending: status.Pending, Roots: roots, Reason: reason})
	})
}

type HistorySearchPage struct {
	SnapshotID        string             `json:"snapshotId,omitempty"`
	SnapshotExpiresAt int64              `json:"snapshotExpiresAt,omitempty"`
	ReadError         *ReadError         `json:"readError,omitempty"`
	Items             []HistorySearchHit `json:"items"`
	NextCursor        string             `json:"nextCursor"`
	Revision          uint64             `json:"revision"`
	Partial           bool               `json:"partial"`
	StaleCursor       bool               `json:"staleCursor"`
	Status            HistoryIndexStatus `json:"status"`
}

type HistorySearchContextRequest struct {
	ContentDigest string `json:"contentDigest,omitempty"`
	SessionPath   string `json:"sessionPath"`
	MessageIndex  int    `json:"messageIndex"`
	Before        int    `json:"before"`
	After         int    `json:"after"`
}

type HistorySearchContextLine struct {
	Index int    `json:"index"`
	Role  string `json:"role"`
	Text  string `json:"text"`
}

func historyCatalogRoots(targets []sessioncatalog.DirectoryTarget) []historycatalog.Root {
	roots := make([]historycatalog.Root, 0, len(targets)*2+1)
	for _, target := range targets {
		source := target.Scope
		if source == "" {
			source = "global"
		}
		root := historycatalog.Root{Path: target.Path, Source: source, Scope: target.Scope, WorkspaceRoot: target.WorkspaceRoot}
		roots = append(roots, root)
		root.Path = filepath.Join(target.Path, "subagents")
		root.Subagents = true
		roots = append(roots, root)
	}
	roots = append(roots, historycatalog.Root{Path: config.ArchiveDir(), Source: "archive", Scope: "global", Archive: true})
	return roots
}

func sessionMetaFromCatalog(record sessioncatalog.SessionRecord, current, open bool) SessionMeta {
	title := strings.TrimSpace(record.CustomTitle)
	preview := record.Preview
	if strings.TrimSpace(preview) == "" && record.TurnsState == sessioncatalog.TurnsUnknown {
		preview = "History is being indexed — " + filepath.Base(record.Path)
	}
	recovered := record.Recovered || strings.TrimSpace(record.RecoveryDigest) != "" || isAutomaticRecoverySessionPath(record.Path)
	// RecoveryCopy comes from the catalog projection, which re-proves coverage
	// from real content at index time. History uses it for the dedicated
	// recovery-copy group and safe bulk cleanup entry points.
	return SessionMeta{Path: record.Path, Preview: preview, Title: title, Turns: record.Turns,
		TurnsState: string(record.TurnsState), CreatedAt: record.CreatedAt, LastActivityAt: record.LastActivityAt,
		ModTime: record.LastActivityAt, Current: current, Open: open, Scope: record.Scope,
		WorkspaceRoot: record.WorkspaceRoot, TopicID: record.TopicID, TopicTitle: record.TopicTitle,
		Recovered: recovered, RecoveryCopy: record.RecoveryCopy,
		RecoveryGroupID: record.RecoveryGroupID, RecoveryRole: record.RecoveryRole,
		RecoveryCanonical: record.RecoveryCanonical}
}

func (a *App) GetHistoryIndexStatus() HistoryIndexStatus {
	if catalog := history.SharedCatalog(); catalog != nil {
		return catalog.Status()
	}
	return HistoryIndexStatus{State: "opening", Pending: 1, Mode: "memory"}
}

func desktopHistoryText(messages []provider.Message, candidate historycatalog.Candidate) (string, bool) {
	if candidate.MessageIndex < 0 || candidate.MessageIndex >= len(messages) {
		return "", false
	}
	message := messages[candidate.MessageIndex]
	switch candidate.Kind {
	case "user_text", "assistant_text":
		return message.Content, true
	case "tool_input":
		if candidate.PartIndex < 0 || candidate.PartIndex >= len(message.ToolCalls) {
			return "", false
		}
		call := message.ToolCalls[candidate.PartIndex]
		return strings.TrimSpace(call.Name + " " + call.Arguments), true
	case "tool_error", "tool_output":
		return strings.TrimSpace(message.Name + " " + message.Content), true
	default:
		return "", false
	}
}

func historyTimeMatchesAt(timestamp int64, filter string, now time.Time) bool {
	if strings.TrimSpace(filter) == "" || filter == "all" {
		return true
	}
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	value := time.UnixMilli(timestamp)
	switch filter {
	case "today":
		return !value.Before(startToday)
	case "yesterday":
		return !value.Before(startToday.AddDate(0, 0, -1)) && value.Before(startToday)
	case "older":
		return value.Before(startToday.AddDate(0, 0, -1))
	default:
		return true
	}
}

func (a *App) GetHistorySearchContext(req HistorySearchContextRequest) []HistorySearchContextLine {
	var options history.Options
	bestLength := -1
	for _, root := range historyCatalogRoots(a.sessionCatalogTargets()) {
		if !desktopHistoryPathWithin(req.SessionPath, root.Path) || len(root.Path) <= bestLength {
			continue
		}
		bestLength = len(root.Path)
		options = history.Options{}
		switch {
		case root.Archive:
			options.ArchiveDir = root.Path
		case root.Subagents:
			options.SessionDir = filepath.Dir(root.Path)
		default:
			options.SessionDir = root.Path
		}
	}
	if bestLength < 0 {
		return []HistorySearchContextLine{}
	}
	if req.ContentDigest != "" {
		messages, state, intact, err := agent.LoadSessionDisplayMessages(req.SessionPath)
		if err != nil || !intact || state.DigestHex != req.ContentDigest {
			return []HistorySearchContextLine{}
		}
		out := []HistorySearchContextLine{}
		for i := max(0, req.MessageIndex-min(max(req.Before, 0), 20)); i < min(len(messages), req.MessageIndex+min(max(req.After, 0), 20)+1); i++ {
			out = append(out, HistorySearchContextLine{Index: i, Role: string(messages[i].Role), Text: messages[i].Content})
		}
		return out
	}
	searcher := history.NewSearcher(options)
	lines, err := searcher.Around(a.bootContext(), history.AroundRequest{SessionPath: req.SessionPath, MessageIndex: req.MessageIndex, Before: req.Before, After: req.After})
	if err != nil {
		return []HistorySearchContextLine{}
	}
	out := make([]HistorySearchContextLine, 0, len(lines))
	for _, line := range lines {
		role := ""
		if fields := strings.Fields(line.Text); len(fields) > 1 {
			role = strings.TrimSuffix(fields[1], "]")
		}
		out = append(out, HistorySearchContextLine{Index: line.Index, Role: role, Text: line.Text})
	}
	return out
}

func desktopHistoryPathWithin(path, root string) bool {
	absPath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func (a *App) RebuildHistoryIndex() error {
	if a == nil || a.shuttingDown.Load() {
		return errors.New("application is shutting down")
	}
	return history.RebuildSharedCatalog(a.bootContext(), historyCatalogRoots(a.sessionCatalogTargets()))
}
