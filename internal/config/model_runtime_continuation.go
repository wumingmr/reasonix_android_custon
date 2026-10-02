package config

import (
	"fmt"
	"net/url"
	"reflect"
	"slices"
)

// ValidateModelRuntimeContinuation checks every route reachable from an old
// frozen resolver, not only its foreground model. Endpoint edits are allowed
// by an explicit one-turn choice; revoked access/credentials never are.
func ValidateModelRuntimeContinuation(old, current *Config) error {
	if old == nil || current == nil {
		return fmt.Errorf("configuration cannot be verified")
	}
	if !reflect.DeepEqual(old.Permissions, current.Permissions) || !reflect.DeepEqual(old.Sandbox, current.Sandbox) || !reflect.DeepEqual(old.Secrets, current.Secrets) || !reflect.DeepEqual(old.Plugins, current.Plugins) {
		return fmt.Errorf("permissions or runtime policy changed")
	}
	if !reflect.DeepEqual(old.Tools, current.Tools) || !reflect.DeepEqual(old.Network, current.Network) || !reflect.DeepEqual(old.Skills, current.Skills) {
		return fmt.Errorf("tool or network policy changed")
	}
	for _, pair := range [][2]int{{old.Agent.MaxSubagentDepth, current.Agent.MaxSubagentDepth}, {old.Agent.MaxSubagentConcurrency, current.Agent.MaxSubagentConcurrency}, {old.Agent.MaxParallelWriters, current.Agent.MaxParallelWriters}} {
		// Zero-value defaults require rebuilding rather than guessing a bound.
		if pair[0] != pair[1] && (pair[0] <= 0 || pair[1] <= 0 || pair[1] < pair[0]) {
			return fmt.Errorf("model execution limits changed")
		}
	}
	for _, pair := range [][2]float64{{old.Agent.TaskCostBudget, current.Agent.TaskCostBudget}, {old.Agent.TaskTimeBudgetMinutes, current.Agent.TaskTimeBudgetMinutes}, {float64(old.Agent.GoalTokenBudget), float64(current.Agent.GoalTokenBudget)}} {
		if pair[1] > 0 && (pair[0] <= 0 || pair[1] < pair[0]) {
			return fmt.Errorf("model execution budgets were tightened")
		}
	}
	for _, route := range old.Providers {
		if old.Desktop.ProviderAccess != nil && !slices.Contains(old.Desktop.ProviderAccess, route.Name) {
			continue
		}
		next, ok := current.Provider(route.Name)
		if !ok || (current.Desktop.ProviderAccess != nil && !slices.Contains(current.Desktop.ProviderAccess, route.Name)) {
			return fmt.Errorf("provider access was removed")
		}
		if route.APIKey() != next.APIKey() {
			return fmt.Errorf("provider credentials changed")
		}
		if err := validateContinuationRoute(route, *next); err != nil {
			return err
		}
		for _, model := range route.ModelList() {
			if !slices.Contains(next.ModelList(), model) {
				return fmt.Errorf("a model in the current runtime is no longer available")
			}
		}
	}
	return nil
}

func validateContinuationRoute(before, after ProviderEntry) error {
	if before.Kind != after.Kind || before.AuthHeader != after.AuthHeader || !reflect.DeepEqual(before.Headers, after.Headers) || !reflect.DeepEqual(before.ExtraBody, after.ExtraBody) {
		return fmt.Errorf("provider authentication parameters changed")
	}
	for _, pair := range [][2]string{{before.BaseURL, after.BaseURL}, {before.ChatURL, after.ChatURL}, {before.RequestURL, after.RequestURL}} {
		if !continuationEndpointAllowed(pair[0], pair[1]) {
			return fmt.Errorf("provider authentication parameters changed")
		}
	}
	// Overrides may include per-model cost caps and capability restrictions.
	// Require a rebuild when their interpretation changed rather than assuming
	// that only the foreground model's defaults constrain an old resolver.
	if before.MaxOutputTokens != after.MaxOutputTokens || before.ContextWindow != after.ContextWindow || !reflect.DeepEqual(before.ModelOverrides, after.ModelOverrides) || !reflect.DeepEqual(before.SupportedEfforts, after.SupportedEfforts) {
		return fmt.Errorf("model limits or capability restrictions changed")
	}
	return nil
}

// Hosts may change with explicit confirmation. URLs carrying credentials or
// query parameters cannot be classified as an address-only edit safely.
func continuationEndpointAllowed(before, after string) bool {
	if before == after {
		return true
	}
	a, errA := url.Parse(before)
	b, errB := url.Parse(after)
	return errA == nil && errB == nil && a.User == nil && b.User == nil && a.RawQuery == b.RawQuery && a.Path == b.Path
}
