package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/jobs"
	"reasonix/internal/session"
)

type rejectingGoalAPI struct {
	control.SessionAPI
	err error
}

func (a *rejectingGoalAPI) SetGoalDurable(string) error { return a.err }

func postRuntimeJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestGoalPauseAndResumeRoutes(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc})
	ctrl.SetGoal("ship the remote surface")
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()

	resp := postRuntimeJSON(t, srv.URL+"/goal/pause", `{}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || ctrl.GoalStatus() != control.GoalStatusBlocked {
		t.Fatalf("pause status/goal = %d/%q", resp.StatusCode, ctrl.GoalStatus())
	}
	resp = postRuntimeJSON(t, srv.URL+"/goal/resume", `{}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || ctrl.GoalStatus() != control.GoalStatusRunning {
		t.Fatalf("resume status/goal = %d/%q", resp.StatusCode, ctrl.GoalStatus())
	}
}

func TestGoalRouteReportsPersistenceFailureBeforeChangingPlanMode(t *testing.T) {
	bc := NewBroadcaster()
	base := control.New(control.Options{Sink: bc})
	base.SetPlanMode(true)
	api := &rejectingGoalAPI{SessionAPI: base, err: errors.New("disk full")}
	srv := httptest.NewServer(operatorHandler(New(api, bc, config.ServeConfig{})))
	defer srv.Close()
	defer base.Close()

	resp := postRuntimeJSON(t, srv.URL+"/goal", `{"goal":"ship it"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("goal status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if !base.PlanMode() || base.Goal() != "" {
		t.Fatalf("failed goal mutation changed runtime: plan=%v goal=%q", base.PlanMode(), base.Goal())
	}
}

func TestGoalEditRoutePreservesGoalIdentity(t *testing.T) {
	bc := NewBroadcaster()
	service, err := session.NewService("serve", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown session service: %v", err)
		}
	})
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-edit-route"})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := control.New(control.Options{Sink: bc, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	defer ctrl.ReleaseResources()
	if err := ctrl.SetGoalDurable("original"); err != nil {
		t.Fatal(err)
	}
	before, err := ctrl.GetGoal(t.Context())
	if err != nil || before == nil {
		t.Fatalf("goal before edit = %+v, %v", before, err)
	}
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()

	resp := postRuntimeJSON(t, srv.URL+"/goal/edit", `{"objective":"revised","maxGoalRounds":12}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("edit status = %d", resp.StatusCode)
	}
	after, _ := ctrl.GetGoal(t.Context())
	if after == nil || after.ID != before.ID || after.Revision != before.Revision+1 || after.Objective != "revised" || after.MaxGoalRounds == nil || *after.MaxGoalRounds != 12 {
		t.Fatalf("goal after edit = %+v, before = %+v", after, before)
	}
}

func TestQualityFloorRouteAcceptsLegacyValueWithoutChangingStatus(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc})
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()

	resp := postRuntimeJSON(t, srv.URL+"/quality-floor", `{"floor":"delivery"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || ctrl.QualityFloor() != control.QualityFloorStandard {
		t.Fatalf("quality floor status/value = %d/%q", resp.StatusCode, ctrl.QualityFloor())
	}
	status, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer status.Body.Close()
	var payload struct {
		QualityFloor string `json:"qualityFloor"`
	}
	if err := json.NewDecoder(status.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.QualityFloor != control.QualityFloorStandard {
		t.Fatalf("status qualityFloor = %q", payload.QualityFloor)
	}

	bad := postRuntimeJSON(t, srv.URL+"/quality-floor", `{"floor":"turbo"}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid quality floor status = %d", bad.StatusCode)
	}
}

func TestJobsCancelRouteCancelsOwnedJobs(t *testing.T) {
	bc := NewBroadcaster()
	manager := jobs.NewManager(bc)
	ctrl := control.New(control.Options{Sink: bc, Jobs: manager})
	defer ctrl.Close()
	job := manager.Start("task", "verify", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()

	resp := postRuntimeJSON(t, srv.URL+"/jobs/cancel", `{"ids":["`+job.ID+`","`+job.ID+`","missing"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
	var result struct {
		Cancelled  []string `json:"cancelled"`
		NotRunning []string `json:"notRunning"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Cancelled) != 1 || result.Cancelled[0] != job.ID {
		t.Fatalf("cancelled = %v", result.Cancelled)
	}
	if len(result.NotRunning) != 1 || result.NotRunning[0] != "missing" {
		t.Fatalf("not running = %v", result.NotRunning)
	}
}
