package main

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestConcurrentActivateTopicSerializesSingleSurfacePruning(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })

	topics := []string{
		"topic-a",
		"topic-b",
		"topic-c",
		"topic-d",
		"topic-e",
		"topic-f",
		"topic-g",
		"topic-h",
	}
	start := make(chan struct{})
	errs := make(chan error, len(topics))
	var wg sync.WaitGroup
	for _, topicID := range topics {
		wg.Add(1)
		go func(topicID string) {
			defer wg.Done()
			<-start
			_, err := app.ActivateTopic("global", "", topicID, "")
			errs <- err
		}(topicID)
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, errSessionNavigationSuperseded) {
			t.Fatalf("ActivateTopic returned error under concurrent navigation: %v", err)
		}
	}
	if succeeded == 0 {
		t.Fatal("every request was superseded; the latest navigation must succeed")
	}
	tabs := app.ListTabs()
	if len(tabs) != 1 {
		t.Fatalf("ListTabs returned %d tabs after single-surface navigation, want 1: %+v", len(tabs), tabs)
	}
	if !tabs[0].Active {
		t.Fatalf("remaining tab is not active: %+v", tabs[0])
	}
}
