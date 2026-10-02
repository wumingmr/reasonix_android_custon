package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"reasonix/internal/eventwire"
	"reasonix/internal/fileutil"
	"reasonix/internal/store"
)

// Checkpoint is the durable display state, separate from provider messages.
// Its digest binds it to the terminal transcript that produced its coverage.
type Checkpoint struct {
	Version           int                          `json:"version"`
	Identity          Identity                     `json:"identity"`
	CoveredThroughSeq uint64                       `json:"coveredThroughSeq"`
	TranscriptDigest  string                       `json:"transcriptDigest"`
	ProviderCount     int                          `json:"providerCount"`
	Records           []Message                    `json:"records"`
	Runtime           Runtime                      `json:"runtime"`
	ActiveAttempts    []ActiveAttempt              `json:"activeAttempts"`
	Completion        *eventwire.CompletionSummary `json:"completion,omitempty"`
}

// ToolResultRepairStats reports legacy identity recovery without including
// tool arguments or result bodies in diagnostics.
type ToolResultRepairStats struct {
	Repaired  int
	Missing   int
	Conflicts int
}

// NeedsToolResultRepair keeps the common restore path from rebuilding
// canonical history when every tool-result display row already has identity.
func NeedsToolResultRepair(records []Message) bool {
	for _, message := range records {
		if message.Role == "tool" && message.MessageID == "" && message.ToolCallID != "" {
			return true
		}
	}
	return false
}

func checkpointMessageIDs(records []Message) map[string]bool {
	occupied := make(map[string]bool)
	for _, message := range records {
		if message.MessageID != "" {
			occupied[message.MessageID] = true
		}
	}
	return occupied
}

// RepairCheckpointToolResults joins legacy display rows that lost MessageID
// with authoritative persisted history. ToolCallID is the only cross-stream
// join key; known turn boundaries must also agree. Ambiguous or conflicting
// rows are deliberately left unchanged.
func RepairCheckpointToolResults(records, canonical []Message) ([]Message, ToolResultRepairStats) {
	repaired := append([]Message(nil), records...)
	byCall := make(map[string][]Message)
	occupied := checkpointMessageIDs(records)
	historyTurn := 0
	for _, message := range canonical {
		if message.Role == "user" {
			historyTurn++
		}
		if message.HistoryTurn == 0 {
			message.HistoryTurn = historyTurn
		}
		if message.Role == "tool" && message.ToolCallID != "" && message.MessageID != "" {
			byCall[message.ToolCallID] = append(byCall[message.ToolCallID], message)
		}
	}
	stats := ToolResultRepairStats{}
	for index := range repaired {
		legacy := &repaired[index]
		if legacy.Role != "tool" || legacy.MessageID != "" || legacy.ToolCallID == "" {
			continue
		}
		candidates := make([]Message, 0, len(byCall[legacy.ToolCallID]))
		for _, candidate := range byCall[legacy.ToolCallID] {
			if legacy.TurnID != "" && candidate.TurnID != "" && legacy.TurnID != candidate.TurnID {
				continue
			}
			if legacy.HistoryTurn != 0 && candidate.HistoryTurn != 0 && legacy.HistoryTurn != candidate.HistoryTurn {
				continue
			}
			candidates = append(candidates, candidate)
		}
		if len(candidates) == 0 {
			stats.Missing++
			continue
		}
		if len(candidates) != 1 {
			stats.Conflicts++
			continue
		}
		formal := candidates[0]
		if occupied[formal.MessageID] {
			stats.Conflicts++
			continue
		}
		// Keep the checkpoint row's display location and event-formatted result,
		// while restoring fields owned by the persisted message.
		legacy.MessageID = formal.MessageID
		occupied[formal.MessageID] = true
		if legacy.RecordID == "" {
			legacy.RecordID = formal.RecordID
		}
		if legacy.ToolName == "" {
			legacy.ToolName = formal.ToolName
		}
		if legacy.CreatedAt == 0 {
			legacy.CreatedAt = formal.CreatedAt
		}
		if legacy.Execution == nil {
			legacy.Execution = formal.Execution
		}
		legacy.ToolResultArchived = legacy.ToolResultArchived || formal.ToolResultArchived
		if len(legacy.PresentedFiles) == 0 {
			legacy.PresentedFiles = append(legacy.PresentedFiles, formal.PresentedFiles...)
		}
		if legacy.ReadCompletion == nil {
			legacy.ReadCompletion = formal.ReadCompletion
		}
		if legacy.Diagnostic == nil {
			legacy.Diagnostic = formal.Diagnostic
		}
		stats.Repaired++
	}
	return repaired, stats
}

func (p *Projection) Checkpoint(digest string) (Checkpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	runtime, attempts := p.runtimeLocked()
	state := Checkpoint{Version: ProtocolVersion, Identity: p.identity, CoveredThroughSeq: p.covered,
		TranscriptDigest: digest, Records: p.buffer.Messages(), Runtime: runtime, ActiveAttempts: attempts, Completion: p.buffer.completion}
	// Detach mutable metadata while sharing immutable strings. Encoding and
	// decoding the full transcript here duplicates large bodies under p.mu;
	// SaveCheckpoint already owns the required encoding outside that lock.
	owned := mapContentStrings(reflect.ValueOf(state), nil, func(text string, _ []string) string { return text }).Interface().(Checkpoint)
	return owned, nil
}

func RestoreCheckpoint(state Checkpoint, identity Identity) (*Projection, error) {
	if state.Version != ProtocolVersion || state.Identity.SessionID != identity.SessionID ||
		state.Identity.HeadID != identity.HeadID || state.Identity.RewriteEpoch != identity.RewriteEpoch {
		return nil, errors.New("transcript checkpoint identity mismatch")
	}
	records := repairCheckpointRecordIdentities(state.Records, state.CoveredThroughSeq)
	p, err := NewProjection(identity, records, state.CoveredThroughSeq)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var owned Checkpoint
	if err = json.Unmarshal(b, &owned); err != nil {
		return nil, err
	}
	p.runtime = owned.Runtime
	if p.runtime.StartedAt > 0 {
		p.startedTurnID = p.runtime.TurnID
	}
	for _, attempt := range owned.ActiveAttempts {
		p.attempts[attempt.ID] = attempt
	}
	for _, prompt := range owned.Runtime.PendingEvents {
		id := prompt.PromptID
		if id != "" {
			p.prompts[id] = prompt
		}
	}
	p.buffer.completion = owned.Completion
	return p, nil
}

// Older builds could persist display-only rows without an identity when a
// frame was published outside the active turn. Repair only that legacy shape;
// non-empty duplicate identities remain corruption and are rejected by
// NewProjection. The generated value is deterministic for this checkpoint so
// repeated recovery cannot reshuffle mounted rows.
func repairCheckpointRecordIdentities(records []Message, covered uint64) []Message {
	repaired := append([]Message(nil), records...)
	used := make(map[string]bool, len(repaired))
	for _, record := range repaired {
		if record.RecordID != "" {
			used[record.RecordID] = true
		}
	}
	for index := range repaired {
		if repaired[index].RecordID != "" {
			continue
		}
		switch {
		case repaired[index].Role == "tool" && repaired[index].ToolCallID != "":
			repaired[index].RecordID = "tool:" + repaired[index].ToolCallID
		case repaired[index].MessageID != "":
			repaired[index].RecordID = "m:" + repaired[index].MessageID
		default:
			base := fmt.Sprintf("view:checkpoint:%d:%d", covered, index)
			repaired[index].RecordID = base
			for suffix := 1; used[repaired[index].RecordID]; suffix++ {
				repaired[index].RecordID = fmt.Sprintf("%s:%d", base, suffix)
			}
		}
		used[repaired[index].RecordID] = true
	}
	return repaired
}

func SaveCheckpoint(sessionPath string, state Checkpoint) error {
	path := store.SessionTranscriptProjection(sessionPath)
	if path == "" {
		return nil
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(path, b, 0o600)
}

func LoadCheckpoint(sessionPath string) (Checkpoint, bool, error) {
	path := store.SessionTranscriptProjection(sessionPath)
	if path == "" {
		return Checkpoint{}, false, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	var state Checkpoint
	if err = json.Unmarshal(b, &state); err != nil {
		return Checkpoint{}, false, err
	}
	if state.Version != ProtocolVersion {
		return Checkpoint{}, false, errors.New("unsupported transcript checkpoint version")
	}
	return state, true, nil
}
