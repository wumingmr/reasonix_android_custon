package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// newSourceService builds the service and one live source session the same way
// service_test.go does.
func newSourceService(t *testing.T, sessionID string) (*Service, string, *Runtime) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	return service, root, runtime
}

// appendCompletedTurn commits one whole turn as a single batch and returns that
// commit. The reply is an assistant message because only an assistant message
// becomes the turn's MessageID.
func appendCompletedTurn(t *testing.T, runtime *Runtime, turnID, messageID string) Commit {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: messageID, Role: provider.RoleAssistant, Content: messageID}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := runtime.Session().Append(t.Context(), Batch{OperationID: turnID, TurnID: turnID, Events: []Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: payload},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

// completedTurnTarget is the fork target of a turn committed by
// appendCompletedTurn. Its closing commit ends at turn/end, so the turn's end
// sequence is also its boundary sequence.
func completedTurnTarget(commit Commit, turnID, messageID string, number int) ForkTarget {
	return ForkTarget{
		TurnID: turnID, TurnNumber: number,
		StartSequence: commit.FirstSequence, EndSequence: commit.LastSequence(),
		BoundarySequence: commit.LastSequence(),
		Status:           event.TurnCompleted, MessageID: messageID, Available: true,
	}
}

// turnSource is a source session with two completed turns, closed before a test
// reads it, so every read is a cold read with no runtime in this process.
type turnSource struct {
	root    string
	service *Service
	ref     SessionRef
	first   Commit
	second  Commit
}

func newClosedTurnSource(t *testing.T) turnSource {
	t.Helper()
	service, root, runtime := newSourceService(t, "source")
	source := turnSource{root: root, service: service, ref: runtime.Ref()}
	source.first = appendCompletedTurn(t, runtime, "turn-1", "message-1")
	source.second = appendCompletedTurn(t, runtime, "turn-2", "message-2")
	if err := service.Close(t.Context(), source.ref); err != nil {
		t.Fatal(err)
	}
	return source
}

// openTurnSource is a source session whose second turn has started without
// ending. Its runtime stays live so a test can fork while the source runs.
type openTurnSource struct {
	root    string
	service *Service
	ref     SessionRef
	first   Commit
	open    Commit
}

func newOpenTurnSource(t *testing.T) openTurnSource {
	t.Helper()
	service, root, runtime := newSourceService(t, "source")
	source := openTurnSource{root: root, service: service, ref: runtime.Ref()}
	source.first = appendCompletedTurn(t, runtime, "turn-1", "message-1")
	open, err := runtime.Session().Append(t.Context(), Batch{OperationID: "turn-2-start", TurnID: "turn-2", Events: []Event{{Kind: "turn/start"}}})
	if err != nil {
		t.Fatal(err)
	}
	source.open = open
	return source
}

// forkChildDirs names the published child sessions under a sessions root. The
// source session and the dot-prefixed derived caches are not children.
func forkChildDirs(t *testing.T, root, sourceID string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	children := []string{}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != sourceID && !strings.HasPrefix(entry.Name(), ".") {
			children = append(children, entry.Name())
		}
	}
	slices.Sort(children)
	return children
}

func TestForkTargetsListCompletedTurnForClosedSession(t *testing.T) {
	source := newClosedTurnSource(t)
	set, err := source.service.ForkTargetSetFor(t.Context(), source.ref)
	if err != nil {
		t.Fatal(err)
	}
	if !set.Verifiable {
		t.Fatal("closed source with turn records reported unverifiable history")
	}
	if len(set.Targets) != 2 {
		t.Fatalf("targets = %+v", set.Targets)
	}
	if want := completedTurnTarget(source.first, "turn-1", "message-1", 1); set.Targets[0] != want {
		t.Fatalf("first target = %+v, want %+v", set.Targets[0], want)
	}
	if want := completedTurnTarget(source.second, "turn-2", "message-2", 2); set.Targets[1] != want {
		t.Fatalf("second target = %+v, want %+v", set.Targets[1], want)
	}
	snapshot, err := source.service.Query().Snapshot(t.Context(), source.ref)
	if err != nil {
		t.Fatal(err)
	}
	turns := snapshot.Projection.Turns
	if len(turns) != 2 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].BoundarySequence == 0 || turns[0].BoundarySequence != source.first.LastSequence() {
		t.Fatalf("first boundary sequence = %d, want %d", turns[0].BoundarySequence, source.first.LastSequence())
	}
	if turns[1].BoundarySequence != source.second.LastSequence() {
		t.Fatalf("second boundary sequence = %d, want %d", turns[1].BoundarySequence, source.second.LastSequence())
	}
}

func TestCreateForkFromColdSourceInheritsOnlyPrefixThroughTurn(t *testing.T) {
	source := newClosedTurnSource(t)
	result, err := source.service.CreateFork(t.Context(), ForkRequest{
		Source: source.ref, TurnID: "turn-1", BoundarySequence: source.first.LastSequence(), ChildID: "child-after-turn-1", OperationID: "fork-turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := (SessionRef{HostID: "local", SessionID: "child-after-turn-1"}); result.Child != want {
		t.Fatalf("child ref = %+v, want %+v", result.Child, want)
	}
	if want := completedTurnTarget(source.first, "turn-1", "message-1", 1); result.Turn != want {
		t.Fatalf("fork turn = %+v, want %+v", result.Turn, want)
	}
	child, err := Open(filepath.Join(source.root, "child-after-turn-1"), "child-after-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(context.Background())
	projection := child.Snapshot().Projection
	if len(projection.Messages) != 1 || projection.Messages[0].ID != "message-1" {
		t.Fatalf("child messages = %+v", projection.Messages)
	}
	if len(projection.Turns) != 1 || projection.Turns[0].TurnID != "turn-1" {
		t.Fatalf("child turns = %+v", projection.Turns)
	}
	if projection.TurnID != "" {
		t.Fatalf("child inherited open turn %q", projection.TurnID)
	}
	if got := child.Manifest().InheritedEvents; got != source.first.LastSequence() {
		t.Fatalf("inherited events = %d, want %d", got, source.first.LastSequence())
	}
	if got := child.Snapshot().EventSequence; got != source.first.LastSequence() {
		t.Fatalf("child sequence = %d, want %d", got, source.first.LastSequence())
	}
}

func TestForkTargetsIncludeOpenTrailingTurnWhileEarlierTurnStaysForkable(t *testing.T) {
	source := newOpenTurnSource(t)
	set, err := source.service.ForkTargetSetFor(t.Context(), source.ref)
	if err != nil {
		t.Fatal(err)
	}
	if !set.Verifiable || len(set.Targets) != 2 {
		t.Fatalf("targets = %+v", set)
	}
	if want := completedTurnTarget(source.first, "turn-1", "message-1", 1); set.Targets[0] != want {
		t.Fatalf("completed target = %+v, want %+v", set.Targets[0], want)
	}
	wantOpen := ForkTarget{
		TurnID: "turn-2", TurnNumber: 2, StartSequence: source.open.FirstSequence,
		Status: event.TurnInProgress, Reason: ForkTurnOpen,
	}
	if set.Targets[1] != wantOpen {
		t.Fatalf("open target = %+v, want %+v", set.Targets[1], wantOpen)
	}
	result, err := source.service.CreateFork(t.Context(), ForkRequest{
		Source: source.ref, TurnID: "turn-1", BoundarySequence: source.first.LastSequence(), ChildID: "child-after-turn-1", OperationID: "fork-turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := Open(filepath.Join(source.root, result.Child.SessionID), result.Child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(context.Background())
	projection := child.Snapshot().Projection
	if len(projection.Messages) != 1 || len(projection.Turns) != 1 || projection.Turns[0].TurnID != "turn-1" {
		t.Fatalf("child projection = %+v", projection)
	}
	after, err := source.service.ForkTargetSetFor(t.Context(), source.ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Targets) != 2 || after.Targets[1] != wantOpen {
		t.Fatalf("source targets after fork = %+v", after.Targets)
	}
	runtime, ok := source.service.Runtime(source.ref)
	if !ok {
		t.Fatal("forking closed the live source runtime")
	}
	if turnID := runtime.Session().Snapshot().Projection.TurnID; turnID != "turn-2" {
		t.Fatalf("source open turn = %q", turnID)
	}
}

func TestForkSequenceRefusesUnknownTurnAndCreateForkRefusesOpenTurn(t *testing.T) {
	source := newOpenTurnSource(t)
	if err := source.service.Close(t.Context(), source.ref); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.service.Query().Snapshot(t.Context(), source.ref)
	if err != nil {
		t.Fatal(err)
	}
	projection := snapshot.Projection
	if sequence, availability, err := ForkSequence(projection, "turn-1"); err != nil || sequence != source.first.LastSequence() || availability != ForkAvailable {
		t.Fatalf("completed turn cut = %d, %s, %v", sequence, availability, err)
	}
	if sequence, availability, err := ForkSequence(projection, "turn-2"); err != nil || sequence != 0 || availability != ForkTurnOpen {
		t.Fatalf("open turn cut = %d, %s, %v", sequence, availability, err)
	}
	sequence, availability, err := ForkSequence(projection, "no-such-turn")
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 0 || availability != ForkHistoryUnverifiable {
		t.Fatalf("unknown turn cut = %d, %s", sequence, availability)
	}
	if number, availability, err := ForkSequenceForNumber(projection, 1); err != nil || number != source.first.LastSequence() || availability != ForkAvailable {
		t.Fatalf("turn number 1 cut = %d, %s, %v", number, availability, err)
	}
	if number, availability, err := ForkSequenceForNumber(projection, 3); err == nil || number != 0 || availability != ForkHistoryUnverifiable {
		t.Fatalf("out-of-range turn number cut = %d, %s, %v", number, availability, err)
	}
	childID := "child-from-open-turn"
	_, err = source.service.CreateFork(t.Context(), ForkRequest{Source: source.ref, TurnID: "turn-2", ChildID: childID, OperationID: "fork-open-turn"})
	var unavailable *ForkUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("open-turn fork error = %v", err)
	}
	if unavailable.TurnID != "turn-2" || unavailable.Reason != ForkTurnOpen {
		t.Fatalf("unavailable = %+v", unavailable)
	}
	if _, statErr := os.Stat(filepath.Join(source.root, childID)); !os.IsNotExist(statErr) {
		t.Fatalf("refused fork left a child directory: %v", statErr)
	}
}

func TestCreateForkInheritsWholeAtomicCommitThatClosedTurn(t *testing.T) {
	service, root, runtime := newSourceService(t, "source")
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "message-1", Role: provider.RoleAssistant, Content: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := runtime.Session().Append(t.Context(), Batch{OperationID: "turn-1", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: payload},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
		{Kind: "diagnostic", Payload: json.RawMessage(`{"note":"committed with the turn"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	turns := runtime.Session().Snapshot().Projection.Turns
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].EndSequence == commit.LastSequence() {
		t.Fatal("turn/end is the last event of this batch, so it cannot show the cut covering the whole commit")
	}
	if turns[0].BoundarySequence != commit.LastSequence() {
		t.Fatalf("boundary sequence = %d, want %d", turns[0].BoundarySequence, commit.LastSequence())
	}
	result, err := service.CreateFork(t.Context(), ForkRequest{Source: runtime.Ref(), TurnID: "turn-1", BoundarySequence: commit.LastSequence(), ChildID: "child-atomic", OperationID: "fork-atomic"})
	if err != nil {
		t.Fatal(err)
	}
	// turn/end is the third event of the batch and the diagnostic the fourth, so
	// the cut must be the batch's last sequence, not the turn/end sequence.
	if endEvent := commit.FirstSequence + 2; result.Turn.EndSequence != endEvent {
		t.Fatalf("fork turn end = %d, want the turn/end event %d", result.Turn.EndSequence, endEvent)
	}
	if result.Turn.EndSequence != turns[0].EndSequence {
		t.Fatalf("fork turn end = %d, want %d", result.Turn.EndSequence, turns[0].EndSequence)
	}
	inherited, err := Replay(filepath.Join(root, "child-atomic"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inherited) != 1 || inherited[0].LastSequence() != commit.LastSequence() || len(inherited[0].Events) != len(commit.Events) {
		t.Fatalf("inherited commits = %+v", inherited)
	}
	if kind := inherited[0].Events[len(inherited[0].Events)-1].Kind; kind != "diagnostic" {
		t.Fatalf("inherited last event = %q", kind)
	}
	child, err := Open(filepath.Join(root, "child-atomic"), "child-atomic")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(context.Background())
	if got := child.Snapshot().EventSequence; got != commit.LastSequence() {
		t.Fatalf("child sequence = %d, want %d", got, commit.LastSequence())
	}
	childTurns := child.Snapshot().Projection.Turns
	if len(childTurns) != 1 || childTurns[0].BoundarySequence != commit.LastSequence() {
		t.Fatalf("child turns = %+v", childTurns)
	}
}

func TestCreateForkIsIdempotentPerOperationID(t *testing.T) {
	source := newClosedTurnSource(t)
	request := ForkRequest{Source: source.ref, TurnID: "turn-1", BoundarySequence: source.first.LastSequence(), OperationID: "fork-retry"}
	first, err := source.service.CreateFork(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.service.CreateFork(t.Context(), request)
	if err != nil {
		t.Fatalf("retried fork: %v", err)
	}
	if first.Child != second.Child {
		t.Fatalf("retry published %+v, want %+v", second.Child, first.Child)
	}
	if children := forkChildDirs(t, source.root, source.ref.SessionID); len(children) != 1 || children[0] != first.Child.SessionID {
		t.Fatalf("child directories = %v", children)
	}
	other, err := source.service.CreateFork(t.Context(), ForkRequest{Source: source.ref, TurnID: "turn-1", BoundarySequence: source.first.LastSequence(), OperationID: "fork-other"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Child == first.Child {
		t.Fatal("a different operation id reused the published child")
	}
	if children := forkChildDirs(t, source.root, source.ref.SessionID); len(children) != 2 {
		t.Fatalf("child directories = %v", children)
	}
}

// TestCreateForkConcurrentSameOperationIDPublishesOneChild drives the race two
// callers of one operation id hit: neither holds a reservation, so both can pass
// the child-existence check before either publishes. Every caller must resolve
// as the same idempotent success rather than the losing rename reaching the
// surface as a hard failure, and the source must hold exactly one child.
func TestCreateForkConcurrentSameOperationIDPublishesOneChild(t *testing.T) {
	source := newClosedTurnSource(t)
	request := ForkRequest{Source: source.ref, TurnID: "turn-1", BoundarySequence: source.first.LastSequence(), OperationID: "fork-race"}
	const callers = 4
	start := make(chan struct{})
	results := make([]ForkResult, callers)
	errs := make([]error, callers)
	var wait sync.WaitGroup
	for caller := range callers {
		wait.Go(func() {
			<-start
			results[caller], errs[caller] = source.service.CreateFork(t.Context(), request)
		})
	}
	close(start)
	wait.Wait()
	for caller := range callers {
		if errs[caller] != nil {
			t.Fatalf("caller %d: %v", caller, errs[caller])
		}
		if results[caller].Child != results[0].Child {
			t.Fatalf("caller %d published %+v, want %+v", caller, results[caller].Child, results[0].Child)
		}
		if want := completedTurnTarget(source.first, "turn-1", "message-1", 1); results[caller].Turn != want {
			t.Fatalf("caller %d turn = %+v, want %+v", caller, results[caller].Turn, want)
		}
	}
	children := forkChildDirs(t, source.root, source.ref.SessionID)
	if len(children) != 1 || children[0] != results[0].Child.SessionID {
		t.Fatalf("child directories = %v, want exactly [%s]", children, results[0].Child.SessionID)
	}
}

func TestCreateForkConcurrentDifferentOperationIDsPublishDistinctChildren(t *testing.T) {
	source := newClosedTurnSource(t)
	const callers = 4
	start := make(chan struct{})
	results := make([]ForkResult, callers)
	errs := make([]error, callers)
	var wait sync.WaitGroup
	for caller := range callers {
		wait.Go(func() {
			<-start
			results[caller], errs[caller] = source.service.CreateFork(t.Context(), ForkRequest{
				Source: source.ref, TurnID: "turn-1", BoundarySequence: source.first.LastSequence(),
				OperationID: fmt.Sprintf("fork-distinct-%d", caller),
			})
		})
	}
	close(start)
	wait.Wait()
	children := map[string]bool{}
	for caller := range callers {
		if errs[caller] != nil {
			t.Fatalf("caller %d: %v", caller, errs[caller])
		}
		children[results[caller].Child.SessionID] = true
	}
	if len(children) != callers {
		t.Fatalf("distinct operations published %d children: %+v", len(children), results)
	}
}

func TestCreateForkFromReadOnlySourceYieldsWritableChildAndLeavesSourceLog(t *testing.T) {
	source := newClosedTurnSource(t)
	logPath := filepath.Join(source.root, source.ref.SessionID, currentLogName)
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("source log is empty")
	}
	result, err := source.service.CreateFork(t.Context(), ForkRequest{Source: source.ref, TurnID: "turn-2", BoundarySequence: source.second.LastSequence(), ChildID: "child-from-read-only", OperationID: "fork-read-only"})
	if err != nil {
		t.Fatal(err)
	}
	assertSourceLogUnchanged := func(when string) {
		t.Helper()
		after, readErr := os.ReadFile(logPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("source log bytes changed %s", when)
		}
	}
	assertSourceLogUnchanged("while forking the read-only source")
	child, err := Open(filepath.Join(source.root, result.Child.SessionID), result.Child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(context.Background())
	if _, err := child.Append(t.Context(), Batch{OperationID: "child-turn", TurnID: "child-turn", Events: []Event{
		{Kind: "turn/start"}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatalf("append to a child of a read-only source: %v", err)
	}
	if got := child.Snapshot().EventSequence; got != result.Turn.EndSequence+2 {
		t.Fatalf("child sequence after append = %d, want %d", got, result.Turn.EndSequence+2)
	}
	assertSourceLogUnchanged("after writing the child")
}

// TestCreateForkRefusesCutWhoseCommitLeavesAuthorityOpen covers the terminal but
// still unsafe boundary: the commit that closed the turn also opened an
// interaction nothing resolved. The cut covers that whole commit, so the child
// would inherit pending authority. The refusal must carry its own reason rather
// than being reported as an unverifiable boundary, and must publish nothing.
func TestCreateForkRefusesCutWhoseCommitLeavesAuthorityOpen(t *testing.T) {
	service, root, runtime := newSourceService(t, "source")
	message, err := json.Marshal(map[string]any{"message": provider.Message{ID: "message-1", Role: provider.RoleAssistant, Content: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := runtime.Session().Append(t.Context(), Batch{OperationID: "turn-1", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: message},
		{Kind: "interaction/created", Payload: json.RawMessage(`{"id":"interaction-1"}`)},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	set, setErr := service.ForkTargetSetFor(t.Context(), runtime.Ref())
	if setErr != nil || len(set.Targets) != 1 || set.Targets[0].Available || set.Targets[0].Reason != ForkActiveAuthority {
		t.Fatalf("unsafe target set = %+v, err=%v", set, setErr)
	}
	_, err = service.CreateFork(t.Context(), ForkRequest{Source: runtime.Ref(), TurnID: "turn-1", BoundarySequence: commit.LastSequence(), OperationID: "fork-open-authority"})
	var unavailable *ForkUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("fork error = %v, want a *ForkUnavailableError", err)
	}
	if unavailable.Reason != ForkActiveAuthority {
		t.Fatalf("reason = %q, want %q", unavailable.Reason, ForkActiveAuthority)
	}
	if children := forkChildDirs(t, root, "source"); len(children) != 0 {
		t.Fatalf("refused fork published %v", children)
	}
}

func TestForkAvailabilityUsesTheCompleteClosingCommit(t *testing.T) {
	service, _, runtime := newSourceService(t, "complete-commit")
	message, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "message-1", Role: provider.RoleAssistant, Content: "one"}})
	commit, err := runtime.Session().Append(t.Context(), Batch{OperationID: "turn-1", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: message},
		{Kind: "interaction/created", Payload: json.RawMessage(`{"id":"interaction-1","state":"pending"}`)},
		{Kind: "tool/start", Payload: json.RawMessage(`{"id":"tool-1","name":"bash"}`)},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
		// These records share the atomic commit with turn/end. Eligibility must be
		// computed after both have cleared their authority.
		{Kind: "interaction/resolved", Payload: json.RawMessage(`{"id":"interaction-1","state":"answered"}`)},
		{Kind: "tool/result", Payload: json.RawMessage(`{"id":"tool-1","name":"bash","output":"ok"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := service.ForkTargetSetFor(t.Context(), runtime.Ref())
	if err != nil || len(set.Targets) != 1 || !set.Targets[0].Available || set.Targets[0].BoundarySequence != commit.LastSequence() {
		t.Fatalf("complete closing commit = %+v, err=%v", set, err)
	}
}

func TestForkAvailabilityRejectsActiveToolAtClosingBoundary(t *testing.T) {
	service, _, runtime := newSourceService(t, "active-tool")
	_, err := runtime.Session().Append(t.Context(), Batch{OperationID: "turn-1", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"},
		{Kind: "tool/start", Payload: json.RawMessage(`{"id":"tool-1","name":"bash"}`)},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := service.ForkTargetSetFor(t.Context(), runtime.Ref())
	if err != nil || len(set.Targets) != 1 || set.Targets[0].Available || set.Targets[0].Reason != ForkActiveAuthority {
		t.Fatalf("active-tool closing commit = %+v, err=%v", set, err)
	}
}

func TestForkTargetSetUnverifiableForMessageOnlyHistory(t *testing.T) {
	service, _, runtime := newSourceService(t, "legacy")
	payload, err := json.Marshal(map[string]any{
		"source":   Source{Path: "/legacy/old.jsonl", Size: 128, SHA256: strings.Repeat("a", 64), Version: Codec},
		"messages": []provider.Message{{ID: "message-1", Role: provider.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "legacy-import", Events: []Event{{Kind: "legacy/import", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	set, err := service.ForkTargetSetFor(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if set.Verifiable || len(set.Targets) != 0 {
		t.Fatalf("message-only history reported %+v", set)
	}
	snapshot := runtime.Session().Snapshot()
	if len(snapshot.Projection.Messages) != 1 || snapshot.Projection.Messages[0].ID != "message-1" {
		t.Fatalf("messages = %+v", snapshot.Projection.Messages)
	}
	if sequence, availability, err := ForkSequence(snapshot.Projection, "message-1"); err != nil || sequence != 0 || availability != ForkHistoryUnverifiable {
		t.Fatalf("message identity resolved as a cut = %d, %s, %v", sequence, availability, err)
	}
}
