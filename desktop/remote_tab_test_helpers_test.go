package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func waitForTabState(t *testing.T, a *App, tabID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.remoteTabMu.Lock()
		tab := a.remoteTabs[tabID]
		state := ""
		if tab != nil {
			state = tab.state
		}
		a.remoteTabMu.Unlock()
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote tab %s state = %q, want %q", tabID, state, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForRemoteTabError(t *testing.T, a *App, tabID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.remoteTabMu.Lock()
		tab := a.remoteTabs[tabID]
		message := ""
		if tab != nil {
			message = tab.err
		}
		a.remoteTabMu.Unlock()
		if strings.Contains(message, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote tab %s error = %q, want text %q", tabID, message, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// resumedSessionID reports the identity the last /resume carried.
func (fs *fakeServe) resumedSessionID() string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.resumeSessionID
}

// takeFault consumes one scheduled fault from a fakeServe counter. Caller
// holds fs.mu.
func (fs *fakeServe) takeFault(counter *int) bool {
	if *counter <= 0 {
		return false
	}
	*counter--
	return true
}

// dropHTTPConnection ends a request the way a dying tunnel does: the client
// sees a transport error, never a status, and cannot tell whether Serve
// committed the request.
func dropHTTPConnection(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		panic("fake serve cannot hijack the connection")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}
