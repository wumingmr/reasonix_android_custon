package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/secrets"
	"reasonix/internal/session"
)

type builtBotSession struct {
	state         *sessionState
	reusedLease   bool
	reusedRuntime bool
}

func botRuntimeSwitchBusyText() string {
	return "当前会话仍有正在运行、等待确认或后台执行的任务。请先完成或停止这些任务，再切换项目或 attach 会话。"
}

func botRuntimeSwitchFailedText(action string) string {
	return action + "失败，当前会话保持不变。请检查配置后重试。"
}

func (gw *BotGateway) buildBotController(ctx context.Context, opts boot.Options) (*control.Controller, error) {
	if opts.SessionService == nil {
		var err error
		opts.SessionService, err = gw.botSessionService(opts.SessionDir)
		if err != nil {
			return nil, err
		}
		opts.SessionHostID = "local"
	}
	if gw.buildController != nil {
		return gw.buildController(ctx, opts)
	}
	return boot.Build(ctx, opts)
}

func (gw *BotGateway) botSessionService(sessionDir string) (*session.Service, error) {
	root := session.RootForLegacyDir(sessionDir)
	if root == "" {
		return nil, nil
	}
	if gw.cfg.SessionServiceForRoot != nil {
		return gw.cfg.SessionServiceForRoot(root)
	}
	gw.sessionServicesMu.Lock()
	defer gw.sessionServicesMu.Unlock()
	if gw.sessionServices == nil {
		gw.sessionServices = make(map[string]*session.Service)
	}
	if service := gw.sessionServices[root]; service != nil {
		return service, nil
	}
	service, err := session.NewService("local", session.NewFilesystemPersistence(root))
	if err != nil {
		return nil, err
	}
	gw.sessionServices[root] = service
	return service, nil
}

// buildSessionState prepares a complete replacement without publishing it.
// When the transcript path is unchanged, the candidate reuses the old keeper
// so the session lease never has an unowned window during a model/profile swap.
func (gw *BotGateway) buildSessionState(ctx context.Context, key string, msg InboundMessage, profile sessionRuntimeProfile, previous *sessionState) (*builtBotSession, error) {
	leases := control.NewSessionLeaseKeeper()
	reusedLease := false
	if previous != nil && previous.leases != nil {
		heldPath := agent.CanonicalSessionPath(previous.leases.HeldPath())
		if heldPath != "" && heldPath == agent.CanonicalSessionPath(profile.sessionPath) {
			leases = previous.leases
			reusedLease = true
		}
	}

	sessionSink := &sessionEventSink{}
	state := &sessionState{
		sink:             sessionSink,
		leases:           leases,
		platform:         msg.Platform,
		connectionID:     strings.TrimSpace(msg.ConnectionID),
		model:            profile.model,
		workspaceRoot:    profile.workspaceRoot,
		toolApprovalMode: profile.toolApprovalMode,
		sessionPath:      profile.sessionPath,
		sessionRef:       profile.sessionRef,
		pendingAsks:      make(map[string][]event.AskQuestion),
		createdAt:        time.Now(),
		lastActive:       time.Now(),
	}
	state.onSessionTransition = gw.botSessionTransitionHandler(key, msg, state)
	buildOptions := boot.Options{
		Model:               profile.model,
		MaxSteps:            gw.cfg.MaxSteps,
		MaxStepsKey:         "bot.max_steps",
		RequireKey:          true,
		Sink:                sessionSink,
		StatsSource:         "bot",
		WorkspaceRoot:       profile.workspaceRoot,
		SessionDir:          botSessionDir(profile.workspaceRoot),
		ApprovalTimeout:     gw.approvalTimeout(),
		OnSessionRecovered:  gw.botSessionRecoveredHandler(key, msg, state),
		OnSessionTransition: state.onSessionTransition,
	}
	reusedRuntime := false
	if previous != nil {
		if binding, ok := previous.ctrl.(interface {
			SessionBinding() (*session.Service, *session.Runtime, bool)
		}); ok {
			if service, runtime, bound := binding.SessionBinding(); bound {
				reusedRuntime = profile.sessionPath == "" && (profile.sessionRef.SessionID == "" || profile.sessionRef.SessionID == runtime.Ref().SessionID)
				if reusedRuntime {
					buildOptions.SessionService = service
					buildOptions.SessionRuntime = runtime
					buildOptions.SessionHostID = runtime.Ref().HostID
				}
			}
		}
	}
	ctrl, err := gw.buildBotController(ctx, buildOptions)
	if err != nil {
		if !reusedLease {
			leases.Release()
		}
		return nil, err
	}
	state.ctrl = ctrl
	fail := func(buildErr error) (*builtBotSession, error) {
		if reusedRuntime {
			ctrl.ReleaseResources()
		} else {
			ctrl.Close()
		}
		if reusedLease {
			if restoreErr := bindBotSessionWriteAuthority(previous); restoreErr != nil {
				gw.logger.Error("restore bot session write authority failed", "err", secrets.RedactError(restoreErr))
			}
		} else {
			leases.Release()
		}
		return nil, buildErr
	}
	if identity, ok := any(ctrl).(control.IdentityLifecycle); ok && identity.UsesExclusiveSession() {
		ref, bindErr := bindBotSessionIdentity(ctx, identity, profile, msg)
		if bindErr != nil {
			if (profile.sessionRefOptional || profile.sessionPathOptional) && !reusedRuntime {
				gw.logger.Warn("mapped bot session unavailable; starting fresh", "err", bindErr)
				ref, bindErr = identity.BindFreshSession(ctx, "")
				state.mappingDegraded = bindErr == nil
			}
			if bindErr != nil {
				return fail(bindErr)
			}
		}
		state.sessionRef = ref
		state.sessionPath = ""
		ctrl.EnableInteractiveApproval()
		ctrl.SetToolApprovalMode(profile.toolApprovalMode)
		return &builtBotSession{state: state, reusedRuntime: reusedRuntime}, nil
	}

	if profile.sessionPath != "" {
		degrade := func(reason string, loadErr error) bool {
			if !profile.sessionPathOptional {
				return false
			}
			gw.logger.Warn("mapped bot session unavailable; starting fresh", "reason", reason, "session_path", profile.sessionPath, "err", loadErr)
			profile.sessionPath = ""
			state.sessionPath = ""
			state.mappingDegraded = true
			return true
		}
		if err := leases.Rebind(profile.sessionPath); err != nil {
			if !degrade("lease held elsewhere", err) {
				return fail(fmt.Errorf("attached bot session is in use: %w", err))
			}
		} else if loaded, err := agent.LoadSession(profile.sessionPath); err != nil {
			if os.IsNotExist(err) && profile.sessionPathOptional {
				ctrl.SetSessionPath(profile.sessionPath)
			} else if !degrade("load failed", err) {
				return fail(fmt.Errorf("load attached bot session: %w", err))
			}
		} else {
			ctrl.Resume(loaded, profile.sessionPath)
		}
	}
	ctrl.EnableInteractiveApproval()
	ctrl.SetToolApprovalMode(profile.toolApprovalMode)
	ctrl.EnsureSessionPath()
	if reusedLease && agent.CanonicalSessionPath(ctrl.SessionPath()) != agent.CanonicalSessionPath(leases.HeldPath()) {
		return fail(errors.New("replacement session path changed while reusing the current lease"))
	}
	if err := rebindBotSessionWriteAuthority(state, ctrl.SessionPath()); err != nil {
		return fail(fmt.Errorf("bind bot session write authority: %w", err))
	}
	return &builtBotSession{state: state, reusedLease: reusedLease}, nil
}

func bindBotSessionIdentity(ctx context.Context, identity control.IdentityLifecycle, profile sessionRuntimeProfile, msg InboundMessage) (session.SessionRef, error) {
	service := identity.SessionService()
	if service == nil {
		return session.SessionRef{}, errors.New("bot v3 session service is unavailable")
	}
	if current, ok := identity.SessionRef(); ok {
		if profile.sessionRef.SessionID == "" || profile.sessionRef.SessionID == current.SessionID {
			return current, nil
		}
	}
	if profile.sessionRef.SessionID != "" {
		ref := profile.sessionRef
		if ref.HostID == "" {
			ref.HostID = service.HostID()
		}
		opened, err := identity.OpenSession(ctx, ref)
		if err == nil {
			return opened, nil
		}
		if !errors.Is(err, session.ErrSessionNotFound) || !profile.sessionRefOptional {
			return session.SessionRef{}, err
		}
		return identity.BindFreshSession(ctx, ref.SessionID)
	}
	if profile.sessionPath != "" {
		return identity.ContinueLegacySession(ctx, profile.sessionPath, "")
	}
	stableID := ""
	if strings.TrimSpace(msg.ChatID) != "" {
		stableID = "bot-" + BuildSessionKey(msg.Session())
	}
	return identity.BindFreshSession(ctx, stableID)
}

func (gw *BotGateway) discardBuiltSession(built *builtBotSession, previous *sessionState) {
	if built == nil || built.state == nil {
		return
	}
	if built.state.ctrl != nil {
		if built.reusedRuntime {
			if releaser, ok := built.state.ctrl.(interface{ ReleaseResources() }); ok {
				releaser.ReleaseResources()
			} else {
				built.state.ctrl.Close()
			}
		} else {
			built.state.ctrl.Close()
		}
	}
	if built.reusedLease {
		if previous != nil {
			previous.lifecycleMu.Lock()
			retired := previous.retired
			previous.lifecycleMu.Unlock()
			if !retired {
				if err := bindBotSessionWriteAuthority(previous); err != nil {
					gw.logger.Error("restore bot session write authority failed", "err", secrets.RedactError(err))
				}
			}
		}
		return
	}
	if built.state.leases != nil {
		built.state.leases.Release()
	}
}

func (gw *BotGateway) setSessionRuntimeOverride(ctx context.Context, key string, msg InboundMessage, override sessionRuntimeOverride, enabled bool) (bool, error) {
	if _, ok := parseBotSessionRefTarget(override.sessionPath); !ok {
		override.sessionPath = canonicalBotPath(override.sessionPath)
	}
	override.channel.WorkspaceRoot = canonicalBotPath(override.channel.WorkspaceRoot)
	profile := gw.sessionProfileForResolvedOverride(msg, override, enabled)
	var switchErr error
	switched := gw.sessions.runIfIdle(key, func() bool {
		gw.mu.Lock()
		previous := gw.controllers[key]
		if previous == nil {
			if enabled {
				gw.sessionOverrides[key] = override
			} else {
				delete(gw.sessionOverrides, key)
			}
			gw.mu.Unlock()
			return true
		}
		if previous != nil && botSessionHasActiveWork(previous) {
			gw.mu.Unlock()
			return false
		}
		if previous != nil && sessionStateMatchesRuntime(previous, profile) {
			if enabled {
				gw.sessionOverrides[key] = override
			} else {
				delete(gw.sessionOverrides, key)
			}
			updateSessionStateRuntime(previous, msg, profile)
			gw.mu.Unlock()
			safeBotSetToolApprovalMode(previous.ctrl, profile.toolApprovalMode)
			return true
		}
		gw.mu.Unlock()

		built, err := gw.buildSessionState(ctx, key, msg, profile, previous)
		if err != nil {
			switchErr = err
			gw.logger.Error("bot session runtime switch failed", "err", secrets.RedactError(err))
			return false
		}

		gw.mu.Lock()
		if gw.controllers[key] != previous {
			gw.mu.Unlock()
			gw.discardBuiltSession(built, previous)
			switchErr = errors.New("bot session changed while replacement was building")
			return false
		}
		if enabled {
			gw.sessionOverrides[key] = override
		} else {
			delete(gw.sessionOverrides, key)
		}
		gw.controllers[key] = built.state
		if built.reusedLease && previous != nil {
			previous.leases = nil
		}
		if built.reusedRuntime && previous != nil {
			previous.releaseRuntimeOnly = true
		}
		gw.mu.Unlock()
		gw.closeSessionState(previous)
		return true
	})
	return switched, switchErr
}
