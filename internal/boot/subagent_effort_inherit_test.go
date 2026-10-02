package boot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/event"
)

func undeclaredEffortConfig() *config.Config {
	cfg := config.Default()
	cfg.DefaultModel = "custom/some-model"
	cfg.Agent.PlannerModel = ""
	cfg.Providers = []config.ProviderEntry{{Name: "custom", Kind: "openai", BaseURL: "https://example.com/v1", Model: "some-model"}}
	cfg.Agent.SubagentEffort = "max"
	return cfg
}

func TestInheritedGlobalSubagentEffortYieldsToExecutionModel(t *testing.T) {
	cfg := undeclaredEffortConfig()
	if err := preflightRoleReasoning(cfg, Options{}, nil, false); err != nil {
		t.Fatalf("inherited agent.subagent_effort blocked assembly: %v", err)
	}
	entry, ok := cfg.ResolveModel(cfg.DefaultModel)
	if !ok {
		t.Fatal("execution model does not resolve")
	}
	if effort, dropped := inheritedSubagentEffort(cfg, entry); effort != "" || dropped != "max" {
		t.Fatalf("inheritedSubagentEffort = (%q, %q), want (\"\", \"max\")", effort, dropped)
	}
	if cfg.Agent.SubagentEffort != "max" {
		t.Fatal("preflight rewrote the configured agent.subagent_effort")
	}
}

func TestExplicitSubagentEffortsStayStrict(t *testing.T) {
	cfg := undeclaredEffortConfig()
	cfg.Agent.SubagentEfforts = map[string]string{"task": "max"}
	var role *RoleReasoningError
	if err := preflightRoleReasoning(cfg, Options{}, nil, false); !errors.As(err, &role) || role.Source != "agent.subagent_efforts.task" {
		t.Fatalf("per-profile effort must stay strict, got %v", err)
	}

	cfg = undeclaredEffortConfig()
	cfg.Agent.SubagentModel = "custom/some-model"
	if err := preflightRoleReasoning(cfg, Options{}, nil, false); !errors.As(err, &role) || role.Source != "agent.subagent_effort" {
		t.Fatalf("effort paired with agent.subagent_model must stay strict, got %v", err)
	}
}

func TestInheritedGlobalSubagentEffortKeptWhenModelDeclaresIt(t *testing.T) {
	cfg := undeclaredEffortConfig()
	cfg.Providers[0].SupportedEfforts = []string{"high", "max"}
	entry, _ := cfg.ResolveModel(cfg.DefaultModel)
	if effort, dropped := inheritedSubagentEffort(cfg, entry); effort != "max" || dropped != "" {
		t.Fatalf("inheritedSubagentEffort = (%q, %q), want (\"max\", \"\")", effort, dropped)
	}
}

type noticeCollector struct {
	mu    sync.Mutex
	texts []string
}

func (c *noticeCollector) Emit(e event.Event) {
	if e.Kind != event.Notice {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, e.Text+" "+e.Detail)
}

func TestBuildNamesTheInheritedSubagentEffortItDropped(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	sink := &noticeCollector{}
	ctrl, err := Build(context.Background(), Options{WorkspaceRoot: root, ConfigSnapshot: undeclaredEffortConfig(), Sink: sink})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, text := range sink.texts {
		if strings.Contains(text, "agent.subagent_effort") && strings.Contains(text, `"max"`) {
			return
		}
	}
	t.Fatalf("no notice names the dropped agent.subagent_effort: %q", sink.texts)
}
