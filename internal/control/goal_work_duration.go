package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
)

// goalWorkSpan belongs to one foreground Run, not to the lifetime of a goal.
// Its process-local clock is never restored, so pauses and downtime add no work.
type goalWorkSpan struct {
	runtime *session.Runtime
	epoch   string
	token   uint64
	goalID  string
	started time.Time
	once    sync.Once
	err     error
}

func (c *Controller) beginGoalWork() *goalWorkSpan {
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil {
		return nil
	}
	view, err := c.goalLifecycleView()
	if err != nil || view == nil || view.Phase != goaldomain.PhaseActive || view.Activation != goaldomain.ActivationArmed {
		return nil
	}
	snapshot := runtime.StateSnapshot()
	if snapshot.Phase != session.RuntimeRunning {
		return nil
	}
	// Runtime activity revisions also advance on cancellation; the turn token
	// stays stable until this body returns, including its cancelled exit.
	token, _, active := c.currentTurnToken()
	if !active {
		return nil
	}
	return &goalWorkSpan{runtime: runtime, epoch: snapshot.Epoch, token: token, goalID: view.ID, started: time.Now()}
}

func (c *Controller) finishGoalWork(span *goalWorkSpan, ended time.Time) error {
	if span == nil {
		return nil
	}
	span.once.Do(func() {
		duration := max(int64(1), ended.Sub(span.started).Milliseconds())
		_, span.err = c.applyHostGoalMutationForRuntime(context.Background(), "work-duration", span.runtime, func(machine *goaldomain.Machine) (*goaldomain.View, error) {
			_, runtime, exclusive := c.v3Binding()
			if !exclusive || runtime != span.runtime {
				return nil, errStaleGoalWork
			}
			snapshot := runtime.StateSnapshot()
			if snapshot.Epoch != span.epoch ||
				(snapshot.Phase != session.RuntimeRunning && snapshot.Phase != session.RuntimeCancelling) {
				return nil, errStaleGoalWork
			}
			token, _, active := c.currentTurnToken()
			if !active || token != span.token {
				return nil, errStaleGoalWork
			}
			current := machine.Get()
			if current == nil || current.ID != span.goalID {
				return nil, errStaleGoalWork
			}
			// Pausing/completing may change the lifecycle revision inside this Run.
			// It must still receive its work, but a replacement goal must never do so.
			view, err := machine.AddWorkDuration(current.Ref(), duration)
			return &view, err
		})
		if errors.Is(span.err, errStaleGoalWork) {
			span.err = nil
		}
	})
	return span.err
}

var errStaleGoalWork = errors.New("goal work belongs to a retired activity")

// recordGoalWork keeps observational persistence failures out of the Run result.
// The failed candidate stays unpublished; core turn durability is unchanged.
func (c *Controller) recordGoalWork(span *goalWorkSpan, ended time.Time) {
	if err := c.finishGoalWork(span, ended); err != nil {
		slog.Warn("controller: persist goal work duration", "err", err)
	}
}
