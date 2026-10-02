package serve

import (
	"context"
	"fmt"
	"reasonix/internal/config"
)

func (s *Server) switchEffortExpected(ctx context.Context, level, expectedPath string) error {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if err := s.expectedSessionPathErrorLocked(expectedPath); err != nil {
		return err
	}
	cur := s.ctl()
	if controllerHasActiveRuntimeWork(cur) {
		return fmt.Errorf("cannot change effort while active work or background jobs are running")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if s.managedModels != nil {
		if err := s.managedModels.Apply(cfg, cur.WorkspaceRoot()); err != nil {
			return err
		}
	}
	ref := currentModelRef(cur)
	entry, ok := cfg.ResolveModel(ref)
	if !ok {
		return fmt.Errorf("cannot resolve current provider %q", ref)
	}
	if !config.EffortCapabilityForEntry(entry).Supported && level != "auto" {
		return fmt.Errorf("effort is not configurable for %s", entry.Name)
	}
	effort, err := config.NormalizeEffort(entry, level)
	if err != nil {
		return err
	}
	if s.managedModels != nil {
		// Managed providers are transient tunnel identities. Keep an explicit
		// session effort override in memory instead of persisting virtual keys.
		previous := s.buildOptions.EffortOverride
		s.buildOptions.EffortOverride = &effort
		if err := s.switchModelLocked(ctx, ref); err != nil {
			s.buildOptions.EffortOverride = previous
			return err
		}
		return nil
	}
	editPath := config.UserConfigPath()
	if editPath == "" {
		return fmt.Errorf("no config file found")
	}
	// Lock only the load-modify-save cycle; switchModel below rebuilds the
	// controller and must not hold the config edit lock.
	if err := func() error {
		unlock := config.LockUserConfigEdits()
		defer unlock()
		edit := config.LoadForEdit(editPath)
		if err := applyEffortEdit(edit, entry, effort); err != nil {
			return err
		}
		if err := edit.SaveTo(editPath); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		return nil
	}(); err != nil {
		return err
	}
	previous := s.buildOptions.EffortOverride
	s.buildOptions.EffortOverride = &effort
	if err := s.switchModelLocked(ctx, entry.Name+"/"+entry.Model); err != nil {
		s.buildOptions.EffortOverride = previous
		return err
	}
	return nil
}
