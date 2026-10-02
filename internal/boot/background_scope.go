package boot

import (
	"fmt"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/workspacelease"
	"time"
)

func acquireBackgroundScope(scope *jobs.SessionBackgroundScope, root string, sink event.Sink, stalledSeconds int) (*jobs.SessionBackgroundScope, error) {
	if scope != nil {
		if err := scope.Acquire(); err != nil {
			return nil, err
		}
		return scope, nil
	}
	lease, err := workspacelease.New(root, config.WorkspaceLeaseDir(), func() {
		notice := event.Event{Kind: event.Notice, Level: event.LevelInfo, Code: event.NoticeCodeWorkspaceLease,
			Text:   "Another session is writing to this workspace; this session will continue automatically when it is safe.",
			Detail: "workspace write lease is busy; read-only work remains concurrent"}
		if scope != nil {
			scope.Manager.Emit(notice)
		} else {
			sink.Emit(notice)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("initialize workspace write lease: %w", err)
	}
	scope = jobs.NewSessionBackgroundScope(jobs.NewManager(sink,
		jobs.WithStalledWarningAfter(time.Duration(stalledSeconds)*time.Second),
		jobs.WithSessionOwnershipProbe(agent.SessionLeaseHeldByCurrentRuntime),
		jobs.WithJobStartObserver(lease.RetainUntil)), lease)
	return scope, nil
}

func releaseBackgroundBuild(scope *jobs.SessionBackgroundScope, controller *control.Controller) {
	if controller != nil {
		controller.ReleaseResources()
	} else {
		scope.Release(false)
	}
}

func stageModelRuntimePublication(res *BuildResult, opts Options) {
	_ = res.Owner.Gate.SweepAndForceExpire()
	res.Controller.StageReplacementPublication(func() {
		publishPreparedBuildResult(res)
		if opts.Extensions != nil && res.Plan != nil {
			go opts.Extensions.DrainPlan(res.Plan)
		}
	})
}
