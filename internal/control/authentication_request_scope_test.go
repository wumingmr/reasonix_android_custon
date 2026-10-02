package control

import (
	"context"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
	"testing"
)

func TestMissingCredentialBlocksSiblingModel(t *testing.T) {
	prov := &sessionTitleProviderStub{out: "title"}
	c := newOwnedTestController(t, Options{
		ModelRef: "relay/chat", Sink: event.Discard,
		Authentication:   AuthenticationState{Status: AuthenticationMissingCredential, ProviderName: "relay", ModelRef: "relay/chat"},
		ProviderResolver: &provider.StaticResolver{Descriptors: []provider.Descriptor{{Ref: "relay/title"}}, Providers: map[string]provider.Provider{"relay/title": prov}},
	})
	_, _ = c.GenerateSessionTitleForModel(context.Background(), "relay/title", "draft")
	if len(prov.requests) != 0 {
		t.Fatalf("same connection is missing a credential but title issued %d request(s)", len(prov.requests))
	}
}

func TestSubagentRejectionMustNotBlockPrimary(t *testing.T) {
	c := newOwnedTestController(t, Options{
		ModelRef: "main/chat",
		Skills:   []skill.Skill{{Name: "helper", Body: "help", RunAs: skill.RunSubagent, Scope: skill.ScopeGlobal}},
		SkillRunner: func(context.Context, skill.Skill, string, skill.SubagentRunOptions) (string, error) {
			return "", &provider.AuthError{Provider: "other", Status: 403, KeyEnv: "OTHER_KEY", HasKey: true}
		},
	})
	_, err := c.RunSubagentProfile(context.Background(), "helper", "task", false)
	if err == nil {
		t.Fatal("expected subagent rejection")
	}
	if !c.AuthenticationState().Ready() {
		t.Fatalf("other model's rejection blocked main/chat: %+v", c.AuthenticationState())
	}
}
