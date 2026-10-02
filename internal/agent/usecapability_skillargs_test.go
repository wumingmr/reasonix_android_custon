package agent

import (
	"context"
	"encoding/json"
	"testing"

	"reasonix/internal/capability"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
)

func TestUseCapabilitySkillCallAcceptsStringArgumentsPayload(t *testing.T) {
	var ran string
	runner := func(_ context.Context, sk skill.Skill, task string, _ skill.SubagentRunOptions) (string, error) {
		ran = sk.Name + ":" + task
		return "ok", nil
	}
	store := skill.New(skill.Options{HomeDir: t.TempDir()})
	reg := tool.NewRegistry()
	reg.Add(skill.NewRunSkillTool(store, runner))
	tl := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"call","capability_id":"skill:explore","arguments":"{\"task\":\"map PicAnnotateCenter\"}"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}
	if ran != "explore:map PicAnnotateCenter" {
		t.Fatalf("runner = %q, want explore task", ran)
	}
}

func TestUseCapabilitySkillCallAcceptsDedicatedTaskPayload(t *testing.T) {
	var ran string
	var gotOpts skill.SubagentRunOptions
	runner := func(_ context.Context, sk skill.Skill, task string, opts skill.SubagentRunOptions) (string, error) {
		ran = sk.Name + ":" + task
		gotOpts = opts
		return "ok", nil
	}
	store := skill.New(skill.Options{HomeDir: t.TempDir()})
	reg := tool.NewRegistry()
	reg.Add(skill.NewRunSkillTool(store, runner))
	tl := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)

	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"call","capability_id":"skill:explore","arguments":{"task":"map the loop","continue_from":"sa_prev"}}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ran != "explore:map the loop" {
		t.Fatalf("runner = %q, want explore task", ran)
	}
	if gotOpts.ContinueFrom != "sa_prev" {
		t.Fatalf("continue_from = %q, want sa_prev", gotOpts.ContinueFrom)
	}
}
