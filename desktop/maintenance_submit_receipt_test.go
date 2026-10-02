package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type compactReceiptProvider struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *compactReceiptProvider) Name() string { return "compact-receipt" }
func (p *compactReceiptProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.calls.Add(1)
	p.started <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	chunks := make(chan provider.Chunk, 2)
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: "Preserve the user's task and the completed work."}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)
	return chunks, nil
}

func TestCompactDuplicateReceiptKeepsExistingOperation(t *testing.T) {
	prov := &compactReceiptProvider{started: make(chan struct{}, 2), release: make(chan struct{})}
	defer close(prov.release)
	sess := agent.NewSession("system")
	for range 8 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("question ", 300)})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 300)})
	}
	exec := agent.New(prov, nil, sess, agent.Options{ContextWindow: 32_000}, event.Discard)
	dir := t.TempDir()
	ctrl := control.New(control.Options{Executor: exec, Sink: event.Discard, SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl")})
	cleanupExactTurnController(t, ctrl)
	tab := &WorkspaceTab{ID: "source", Scope: "global", Ready: true, Ctrl: ctrl}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	first, err := app.StartTurnForTab(tab.ID, "/compact", "first")
	if err != nil || first.OperationID == "" || first.ManagementErrorCode != "" {
		t.Fatalf("first compact = %+v, %v", first, err)
	}
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not enter provider")
	}
	// A late response still belongs to the source tab after the user navigates away.
	app.activeTabID = "other"
	for _, input := range []string{"/compact", "/compact preserve decisions"} {
		duplicate, err := app.StartTurnForTab(tab.ID, input, "duplicate:"+input)
		if err != nil || duplicate.Disposition != control.SubmitManagementHandled || duplicate.ManagementErrorCode != "maintenance_busy" ||
			duplicate.OperationID != first.OperationID || duplicate.TurnID != "" {
			t.Fatalf("duplicate compact = %+v, %v", duplicate, err)
		}
	}
	if _, err := app.StartTurnForTab(tab.ID, "ordinary question", "ordinary"); !errors.Is(err, control.ErrTurnRunning) {
		t.Fatalf("ordinary turn bypassed maintenance guard: %v", err)
	}
	if prov.calls.Load() != 1 || ctrl.ActiveMaintenanceOperationID() != first.OperationID {
		t.Fatal("duplicate request replaced or restarted the existing operation")
	}
}

type maintenanceReceiptController struct {
	*tabScopedActionController
	maintenance          *event.MaintenanceState
	admissionMu          *sync.Mutex
	readOutsideAdmission bool
}

func (c *maintenanceReceiptController) ClassifySubmitRoute(string) control.SubmitDisposition {
	return control.SubmitManagementHandled
}
func (c *maintenanceReceiptController) RuntimeStateSnapshot() event.RuntimeStateSnapshot {
	if c.admissionMu != nil && c.admissionMu.TryLock() {
		c.readOutsideAdmission = true
		c.admissionMu.Unlock()
	}
	return event.RuntimeStateSnapshot{Maintenance: c.maintenance}
}

func TestCompactRecoveryReceiptAndLegacyShape(t *testing.T) {
	ctrl := &maintenanceReceiptController{tabScopedActionController: newTabScopedActionController(),
		maintenance: &event.MaintenanceState{OperationID: "recover-operation", Kind: "compact", Activity: "recovery_required"}}
	tab := &WorkspaceTab{ID: "source", Scope: "global", Ready: true, Ctrl: ctrl}
	ctrl.admissionMu = &tab.turnStartMu
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}
	receipt, err := app.StartTurnForTab(tab.ID, "/compact", "retry")
	if err != nil || receipt.ManagementErrorCode != "maintenance_recovery_required" || receipt.OperationID != "recover-operation" {
		t.Fatalf("recovery refusal was lost: %+v, %v", receipt, err)
	}
	if ctrl.readOutsideAdmission {
		t.Fatal("management conflict read its owner outside the admission lock")
	}
	// Existing readers retain the management disposition and operation identity.
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Disposition control.SubmitDisposition `json:"disposition"`
		OperationID string                    `json:"operationId"`
	}
	if err := json.Unmarshal(encoded, &legacy); err != nil || legacy.Disposition != control.SubmitManagementHandled || legacy.OperationID != receipt.OperationID {
		t.Fatalf("legacy receipt decode = %+v, %v", legacy, err)
	}
	var old TurnStartView
	if err := json.Unmarshal([]byte(`{"disposition":"management_handled"}`), &old); err != nil || old.ManagementErrorCode != "" {
		t.Fatalf("old successful receipt changed meaning: %+v, %v", old, err)
	}
}

type classifiedCompactController struct {
	*control.Controller
	classified chan struct{}
}

func (c *classifiedCompactController) ClassifySubmitRoute(input string) control.SubmitDisposition {
	result := c.Controller.ClassifySubmitRoute(input)
	close(c.classified)
	return result
}

func TestCompactDuplicateWaitsForAdmissionOwner(t *testing.T) {
	prov := &compactReceiptProvider{started: make(chan struct{}, 2), release: make(chan struct{})}
	defer close(prov.release)
	sess := agent.NewSession("system")
	for range 8 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("question ", 300)})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 300)})
	}
	exec := agent.New(prov, nil, sess, agent.Options{ContextWindow: 32_000}, event.Discard)
	dir := t.TempDir()
	ctrl := control.New(control.Options{Executor: exec, Sink: event.Discard, SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl")})
	cleanupExactTurnController(t, ctrl)
	wrapper := &classifiedCompactController{Controller: ctrl, classified: make(chan struct{})}
	tab := &WorkspaceTab{ID: "source", Scope: "global", Ready: true, Ctrl: wrapper}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}
	type response struct {
		receipt TurnStartView
		err     error
	}
	done := make(chan response, 1)
	// The first submit owns the tab lock while the duplicate classifies its route.
	tab.turnStartMu.Lock()
	go func() {
		receipt, err := app.StartTurnForTab(tab.ID, "/compact preserve decisions", "duplicate")
		done <- response{receipt, err}
	}()
	select {
	case <-wrapper.classified:
	case <-time.After(5 * time.Second):
		tab.turnStartMu.Unlock()
		t.Fatal("duplicate did not classify")
	}
	first := ctrl.SubmitDisplayWithResult("/compact", "/compact")
	tab.turnStartMu.Unlock()
	select {
	case got := <-done:
		if got.err != nil || got.receipt.ManagementErrorCode != "maintenance_busy" || got.receipt.OperationID != first.OperationID || first.OperationID == "" {
			t.Fatalf("duplicate lost the admission owner's identity: %+v, %v; first=%+v", got.receipt, got.err, first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate remained blocked")
	}
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first compaction did not enter provider")
	}
	if prov.calls.Load() != 1 {
		t.Fatalf("provider called %d times", prov.calls.Load())
	}
}

type compactDuringStatusController struct{ *maintenanceReceiptController }

func (c *compactDuringStatusController) RuntimeStatus() control.RuntimeStatus {
	// The synchronous Compact button also owns controller maintenance. Model its
	// registration while the slash request is entering desktop admission.
	c.maintenance = &event.MaintenanceState{OperationID: "button-compact", Kind: "compact", Activity: "running"}
	return control.RuntimeStatus{Running: true}
}

func TestCompactButtonConflictUsesAdmissionReceipt(t *testing.T) {
	ctrl := &compactDuringStatusController{&maintenanceReceiptController{tabScopedActionController: newTabScopedActionController()}}
	tab := &WorkspaceTab{ID: "source", Scope: "global", Ready: true, Ctrl: ctrl}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}
	receipt, err := app.StartTurnForTab(tab.ID, "/compact", "duplicate")
	if err != nil || receipt.ManagementErrorCode != "maintenance_busy" || receipt.OperationID != "button-compact" {
		t.Fatalf("button conflict lost its identity: %+v, %v", receipt, err)
	}
}
