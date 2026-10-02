package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"reasonix/internal/config"
	"reasonix/internal/fileutil"
)

const forkOperationsSchemaVersion = 1

var forkOperationsMu sync.Mutex

type forkOperation struct {
	OperationID      string `json:"operationId"`
	Surface          string `json:"surface"`
	TabID            string `json:"tabId"`
	SourceHostID     string `json:"sourceHostId,omitempty"`
	SourceSessionID  string `json:"sourceSessionId"`
	TurnID           string `json:"turnId"`
	BoundarySequence uint64 `json:"boundarySequence"`
	State            string `json:"state"`
	ChildSessionID   string `json:"childSessionId,omitempty"`
}

type forkOperationJournal struct {
	SchemaVersion int             `json:"schemaVersion"`
	Operations    []forkOperation `json:"operations"`
}

func forkOperationsPath() string {
	return filepath.Join(config.MemoryUserDir(), "fork-operations.json")
}

func loadForkOperations(path string) (forkOperationJournal, error) {
	journal := forkOperationJournal{SchemaVersion: forkOperationsSchemaVersion, Operations: []forkOperation{}}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	if err := json.Unmarshal(body, &journal); err != nil {
		return journal, fmt.Errorf("decode fork operation journal: %w", err)
	}
	if journal.SchemaVersion != forkOperationsSchemaVersion {
		return journal, fmt.Errorf("unsupported fork operation journal schema %d", journal.SchemaVersion)
	}
	if journal.Operations == nil {
		journal.Operations = []forkOperation{}
	}
	return journal, nil
}

func saveForkOperations(path string, journal forkOperationJournal) error {
	journal.SchemaVersion = forkOperationsSchemaVersion
	if journal.Operations == nil {
		journal.Operations = []forkOperation{}
	}
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return fileutil.AtomicWriteFile(path, body, 0o600)
}

func sameForkOperation(left, right forkOperation) bool {
	return left.SourceHostID == right.SourceHostID && left.SourceSessionID == right.SourceSessionID &&
		left.TurnID == right.TurnID && left.BoundarySequence == right.BoundarySequence
}

func (a *App) beginForkOperation(template forkOperation) (forkOperation, error) {
	forkOperationsMu.Lock()
	defer forkOperationsMu.Unlock()
	path := forkOperationsPath()
	journal, err := loadForkOperations(path)
	if err != nil {
		return forkOperation{}, err
	}
	for _, existing := range journal.Operations {
		if sameForkOperation(existing, template) {
			return existing, nil
		}
	}
	template.OperationID = "fork_" + strings.TrimPrefix(newTabID(), "tab_")
	template.State = "pending"
	journal.Operations = append(journal.Operations, template)
	if err := saveForkOperations(path, journal); err != nil {
		return forkOperation{}, err
	}
	return template, nil
}

func (a *App) completeForkOperation(operationID, childSessionID string) error {
	forkOperationsMu.Lock()
	defer forkOperationsMu.Unlock()
	path := forkOperationsPath()
	journal, err := loadForkOperations(path)
	if err != nil {
		return err
	}
	for index := range journal.Operations {
		if journal.Operations[index].OperationID == operationID {
			journal.Operations[index].State = "completed"
			journal.Operations[index].ChildSessionID = childSessionID
			return saveForkOperations(path, journal)
		}
	}
	return fmt.Errorf("fork operation %q is missing", operationID)
}

func (a *App) discardForkOperation(operationID string) error {
	return removeForkOperation(operationID, false)
}

// AcknowledgeForkOperation removes a durable result only after the UI has
// opened or adopted the child. Operation ids are host-unique, so acknowledgement
// remains valid when Desktop restart restored the source under another tab id.
// A pending operation cannot be acknowledged away, and repeating an
// acknowledgement is harmless.
func (a *App) AcknowledgeForkOperation(tabID, operationID string) error {
	return removeForkOperation(operationID, true)
}

func removeForkOperation(operationID string, completedOnly bool) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil
	}
	forkOperationsMu.Lock()
	defer forkOperationsMu.Unlock()
	path := forkOperationsPath()
	journal, err := loadForkOperations(path)
	if err != nil {
		return err
	}
	next := journal.Operations[:0]
	for _, operation := range journal.Operations {
		if operation.OperationID == operationID && (!completedOnly || operation.State == "completed") {
			continue
		}
		next = append(next, operation)
	}
	if len(next) == len(journal.Operations) {
		return nil
	}
	journal.Operations = next
	return saveForkOperations(path, journal)
}
