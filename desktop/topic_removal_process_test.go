package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestTopicRemovalRejectsAnotherProcessRename(t *testing.T) {
	if id := os.Getenv("REASONIX_TOPIC_REMOVAL_RENAME"); id != "" {
		// TestMain deliberately isolates each process. Rebind only this child's
		// test profile to the parent's disposable fixture after that isolation.
		var environment map[string]string
		if err := json.Unmarshal([]byte(os.Getenv("REASONIX_TOPIC_REMOVAL_ENV")), &environment); err != nil {
			t.Fatal(err)
		}
		for key, value := range environment {
			t.Setenv(key, value)
		}
		a := NewApp()
		a.ctx = t.Context()
		installNoopRuntimeEvents(a)
		defer a.closeSessionServices()
		if err := a.RenameTopic(id, "Saved by another process"); err != nil {
			t.Fatal(err)
		}
		return
	}
	a, topic, _ := topicRemovalFixture(t, "global", "")
	request := inspectedTopicRemoval(t, a, topic.ID)
	child := exec.Command(os.Args[0], "-test.run=^TestTopicRemovalRejectsAnotherProcessRename$")
	environment := map[string]string{}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME", "AppData"} {
		environment[key] = os.Getenv(key)
	}
	body, err := json.Marshal(environment)
	if err != nil {
		t.Fatal(err)
	}
	child.Env = append(os.Environ(), "REASONIX_TOPIC_REMOVAL_RENAME="+topic.ID, "REASONIX_TOPIC_REMOVAL_ENV="+string(body))
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("rename process: %v %s", err, output)
	}
	result, err := a.RemoveTopic(request)
	if err != nil || result.Committed || result.ErrorCode != "state_conflict" {
		t.Fatalf("stale cross-process confirmation: %+v %v", result, err)
	}
	if loadTopicTitle("", topic.ID) != "Saved by another process" {
		t.Fatal("another process's saved metadata was removed")
	}
}
