package browserops

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"time"
)

// DiagnosticOperation deliberately omits grants, document tokens, argument
// digests and free-form errors. Tool errors remain in the redacted transcript.
type DiagnosticOperation struct {
	OperationID string    `json:"operationId"`
	TabID       string    `json:"tabId"`
	Action      string    `json:"action"`
	State       State     `json:"state"`
	ReservedAt  time.Time `json:"reservedAt"`
	SettledAt   time.Time `json:"settledAt"`
}
type DiagnosticSnapshot struct {
	Available       bool                  `json:"available"`
	Truncated       bool                  `json:"truncated"`
	FieldsTruncated bool                  `json:"fieldsTruncated"`
	Matched         int                   `json:"matched"`
	Limit           int                   `json:"limit"`
	Operations      []DiagnosticOperation `json:"operations"`
	Coverage        string                `json:"coverage"`
}

func diagnosticSnapshot(file ledgerFile, scope string) DiagnosticSnapshot {
	const limit = 200
	out := DiagnosticSnapshot{Available: true, Limit: limit, Operations: []DiagnosticOperation{}, Coverage: "Only records with explicit diagnostic attribution; pre-upgrade or downgraded records cannot be attributed. Reserved states are observed without recovery or replay."}
	if scope == "" {
		out.Available = false
		return out
	}
	for _, op := range file.Operations {
		if op == nil || op.DiagnosticScope != scope {
			continue
		}
		out.Matched++
		bounded := func(value string, limit int) string {
			if len(value) > limit {
				out.FieldsTruncated = true
				return value[:limit]
			}
			return value
		}
		out.Operations = append(out.Operations, DiagnosticOperation{bounded(op.ID, 100), bounded(op.TabID, 160), bounded(op.Action, 64), State(bounded(string(op.State), 32)), op.ReservedAt, op.SettledAt})
		// Keep memory bounded even when the on-disk ledger is large.
		sort.Slice(out.Operations, func(i, j int) bool {
			if out.Operations[i].ReservedAt.Equal(out.Operations[j].ReservedAt) {
				return out.Operations[i].OperationID < out.Operations[j].OperationID
			}
			return out.Operations[i].ReservedAt.After(out.Operations[j].ReservedAt)
		})
		if len(out.Operations) > limit {
			out.Operations = out.Operations[:limit]
		}
	}
	out.Truncated = out.Matched > limit
	return out
}

func (l *Ledger) Diagnostics(scope string) DiagnosticSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return diagnosticSnapshot(l.file, scope)
}

// ReadDiagnostics never calls Open: an export must not settle reserved writes.
func ReadDiagnostics(path, scope string) (DiagnosticSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return DiagnosticSnapshot{}, err
	}
	defer f.Close()
	const budget = 64 << 20
	data, err := io.ReadAll(io.LimitReader(f, budget+1))
	if err != nil {
		return DiagnosticSnapshot{}, err
	}
	if len(data) > budget {
		return DiagnosticSnapshot{}, errors.New("browser ledger exceeds diagnostic read budget")
	}
	var file ledgerFile
	if err = json.Unmarshal(data, &file); err != nil {
		return DiagnosticSnapshot{}, err
	}
	if file.Version != ledgerVersion {
		return DiagnosticSnapshot{}, errors.New("unsupported browser ledger version")
	}
	return diagnosticSnapshot(file, scope), nil
}
