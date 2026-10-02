package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/serve"
	"reasonix/internal/transcript"
)

// A Serve whose foreground is a path-backed session (the controller POST
// /resume installs for a historical .jsonl) still serves Follow v2 through the
// legacy projection, so the handshake a desktop attaches with must say so.
func TestRemoteFollowAttachesToServeWithPathBackedForeground(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sess := agent.NewSession("system")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "question"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "answer"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	bc := serve.NewBroadcaster()
	ctrl := control.New(control.Options{Executor: agent.New(nil, nil, sess, agent.Options{}, bc), SessionDir: dir, SessionPath: path, Sink: bc})
	defer ctrl.Close()
	if ctrl.UsesExclusiveSession() {
		t.Fatal("fixture must put a path-backed session in the foreground")
	}
	server := httptest.NewServer(serve.New(ctrl, bc, config.ServeConfig{AuthMode: "token", Token: "fixture-token"}).Handler())
	defer server.Close()

	client, err := newServeHTTPClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	capabilities, err := serveHandshakeCapabilities(ctx, client, server.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	tab := &remoteTab{id: "remote", state: "ready", client: client, base: server.URL, gen: 1,
		routing: remoteTabSessionRouting{currentPath: path}, capabilities: map[string]bool{}}
	for _, capability := range capabilities {
		tab.capabilities[capability] = true
	}
	app := &App{remoteTabs: map[string]*remoteTab{tab.id: tab}}

	followed, err := app.RemoteTranscriptFollowForTab(tab.id, transcript.FollowRequest{})
	if err != nil {
		t.Fatalf("follow against a path-backed foreground (capabilities %v): %v", capabilities, err)
	}
	if followed.ProtocolVersion != transcript.FollowProtocolVersion || followed.Snapshot == nil || followed.Subscription == "" || followed.StorageBackend != "legacy" {
		t.Fatalf("follow = %+v", followed)
	}
	if len(followed.Snapshot.Records) != 2 {
		t.Fatalf("follow snapshot records = %d, want 2", len(followed.Snapshot.Records))
	}
	if _, err := app.RemoteTranscriptFollowForTab(tab.id, transcript.FollowRequest{Subscription: followed.Subscription, Close: true}); err != nil {
		t.Fatalf("close follow: %v", err)
	}
}
