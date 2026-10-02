package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/tool"
)

type goalLifecycleStub struct {
	view          *goaldomain.View
	createRequest goaldomain.CreateRequest
	updateRequest tool.GoalUpdateRequest
	authority     tool.GoalAuthority
	err           error
}

func (s *goalLifecycleStub) GetGoal(context.Context) (*goaldomain.View, error) { return s.view, s.err }
func (s *goalLifecycleStub) CreateGoal(_ context.Context, request goaldomain.CreateRequest, authority tool.GoalAuthority) (goaldomain.View, error) {
	s.createRequest, s.authority = request, authority
	if s.err != nil {
		return goaldomain.View{}, s.err
	}
	return *s.view, nil
}
func (s *goalLifecycleStub) UpdateGoal(_ context.Context, request tool.GoalUpdateRequest, authority tool.GoalAuthority) (goaldomain.View, error) {
	s.updateRequest, s.authority = request, authority
	if s.err != nil {
		return goaldomain.View{}, s.err
	}
	return *s.view, nil
}

func goalLifecycleContext(stub *goalLifecycleStub, source tool.GoalSource) context.Context {
	return tool.WithGoalLifecycle(context.Background(), stub, tool.GoalAuthority{
		Source: source, SessionID: "session-1", RuntimeEpoch: "epoch-1", ActivityID: 4,
		GoalID: "goal-1", Revision: 3, Round: 2,
	})
}

func goalView() *goaldomain.View {
	return &goaldomain.View{Snapshot: goaldomain.Snapshot{
		ID: "goal-1", Revision: 3, Objective: "ship", Phase: goaldomain.PhaseActive, RoundsStarted: 2,
	}, Activation: goaldomain.ActivationArmed}
}

func TestGetGoalReturnsCurrentViewOrNull(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	got, err := (getGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), nil)
	if err != nil || !strings.Contains(got, `"id":"goal-1"`) || !strings.Contains(got, `"maxGoalRounds":null`) {
		t.Fatalf("result = %q, err = %v", got, err)
	}
	stub.view = nil
	got, err = (getGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), nil)
	if err != nil || got != `{"goal":null}` {
		t.Fatalf("empty result = %q, err = %v", got, err)
	}
}

func TestGoalMutationToolsAreSerializedWithOtherSideEffects(t *testing.T) {
	if (createGoal{}).ReadOnly() || (updateGoal{}).ReadOnly() {
		t.Fatal("goal lifecycle mutations must not enter the read-only parallel tool lane")
	}
	if !(getGoal{}).ReadOnly() {
		t.Fatal("get_goal should remain read-only")
	}
}

func TestCreateGoalDefaultsToUnlimited(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	got, err := (createGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), json.RawMessage(`{"objective":" ship "}`))
	if err != nil {
		t.Fatal(err)
	}
	if stub.createRequest.Objective != "ship" || stub.createRequest.MaxGoalRounds != nil {
		t.Fatalf("request = %+v", stub.createRequest)
	}
	if !strings.Contains(got, `"activation":"armed"`) {
		t.Fatalf("result = %s", got)
	}
}

func TestCreateGoalAcceptsExplicitLimit(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	_, err := (createGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), json.RawMessage(`{"objective":"ship","max_goal_rounds":12}`))
	if err != nil || stub.createRequest.MaxGoalRounds == nil || *stub.createRequest.MaxGoalRounds != 12 {
		t.Fatalf("request = %+v, err = %v", stub.createRequest, err)
	}
}

func TestUpdateGoalForwardsExactRevisionAndAction(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	_, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceGoalRound), json.RawMessage(`{"goal_id":"goal-1","revision":3,"action":"blocked","blocked_reason":"dependency unavailable"}`))
	if err != nil {
		t.Fatal(err)
	}
	request := stub.updateRequest
	if request.Ref.ID != "goal-1" || request.Ref.Revision != 3 || request.Action != tool.GoalActionBlocked {
		t.Fatalf("request = %+v", request)
	}
	if request.BlockedReason == nil || request.BlockedReason.Code != "model-blocked" || request.BlockedReason.Message != "dependency unavailable" {
		t.Fatalf("blocked reason = %+v", request.BlockedReason)
	}
}

func TestUpdateGoalEditDistinguishesOmittedAndNullLimit(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	ctx := goalLifecycleContext(stub, tool.GoalSourceDirectHuman)
	_, err := (updateGoal{}).Execute(ctx, json.RawMessage(`{"goal_id":"goal-1","revision":3,"action":"edit","objective":"ship safely"}`))
	if err != nil || stub.updateRequest.MaxGoalRounds.Set {
		t.Fatalf("omitted request = %+v, err = %v", stub.updateRequest, err)
	}
	_, err = (updateGoal{}).Execute(ctx, json.RawMessage(`{"goal_id":"goal-1","revision":3,"action":"edit","max_goal_rounds":null}`))
	if err != nil || !stub.updateRequest.MaxGoalRounds.Set || stub.updateRequest.MaxGoalRounds.Value != nil {
		t.Fatalf("null request = %+v, err = %v", stub.updateRequest, err)
	}
}

func TestGoalToolsFailClosedWithoutHostBinding(t *testing.T) {
	for _, candidate := range []struct {
		name string
		tool tool.Tool
		args string
	}{
		{"get_goal", getGoal{}, `{}`},
		{"create_goal", createGoal{}, `{"objective":"ship"}`},
		{"update_goal", updateGoal{}, `{"goal_id":"g","revision":1,"action":"complete"}`},
	} {
		if _, err := candidate.tool.Execute(context.Background(), json.RawMessage(candidate.args)); err == nil || !strings.Contains(err.Error(), "host-attested goal context") {
			t.Fatalf("%s error = %v", candidate.name, err)
		}
	}
}

func TestUpdateGoalRejectsLegacyContinueProtocol(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	_, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceGoalRound), json.RawMessage(`{"status":"continue","reason":"working"}`))
	if err == nil || !strings.Contains(err.Error(), "legacy update_goal protocol") {
		t.Fatalf("error = %v", err)
	}
}

func TestUpdateGoalSurfacesStructuredDomainError(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView(), err: &goaldomain.Error{Code: goaldomain.ErrStaleRevision, Message: "stale"}}
	_, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), json.RawMessage(`{"goal_id":"goal-1","revision":3,"action":"complete"}`))
	if err == nil || !strings.Contains(err.Error(), string(goaldomain.ErrStaleRevision)) || !errors.Is(err, stub.err) {
		t.Fatalf("error = %v", err)
	}
}

func TestUpdateGoalTerminalResultRequestsFinalSummary(t *testing.T) {
	stub := &goalLifecycleStub{view: &goaldomain.View{Snapshot: goaldomain.Snapshot{
		ID: "goal-1", Revision: 4, Objective: "ship", Phase: goaldomain.PhaseComplete,
	}, Activation: goaldomain.ActivationDisarmed}}
	got, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceGoalRound), json.RawMessage(`{"goal_id":"goal-1","revision":3,"action":"complete"}`))
	if err != nil || !strings.Contains(got, `"instruction":"Finish the current turn`) {
		t.Fatalf("terminal result = %q, err = %v", got, err)
	}
}

func TestUpdateGoalValidatesActionSpecificFields(t *testing.T) {
	stub := &goalLifecycleStub{view: goalView()}
	ctx := goalLifecycleContext(stub, tool.GoalSourceDirectHuman)
	for _, args := range []string{
		`{"goal_id":"goal-1","revision":3,"action":"blocked"}`,
		`{"goal_id":"goal-1","revision":3,"action":"blocked","blocked_reason":"  "}`,
		`{"goal_id":"goal-1","revision":3,"action":"edit","objective":""}`,
		`{"goal_id":"goal-1","revision":3,"action":"edit","max_goal_rounds":0}`,
		`{"goal_id":"goal-1","revision":3,"action":"edit","objective":null}`,
		`{"goal_id":"goal-1","revision":3,"action":"blocked","blocked_reason":null}`,
		`{"goal_id":"goal-1","revision":3,"action":"resume","unknown":"field"}`,
		`{"goal_id":"goal-1","revision":3,"action":"resume","objective":123}`,
		`{"goal_id":"goal-1","revision":3,"action":"edit"}`,
		`{"goal_id":"goal-1","revision":3,"action":"continue"}`,
	} {
		if _, err := (updateGoal{}).Execute(ctx, json.RawMessage(args)); err == nil {
			t.Fatalf("accepted invalid args %s", args)
		}
		if stub.updateRequest != (tool.GoalUpdateRequest{}) {
			t.Fatalf("invalid arguments reached owner: %+v", stub.updateRequest)
		}
	}
}
