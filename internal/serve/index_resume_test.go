package serve

import (
	"strings"
	"testing"
)

// Canonical /sessions rows carry an empty path, so a resume that sends only
// the path cannot name the row the user clicked.
func TestServeIndexResumesSessionByIdentity(t *testing.T) {
	html := string(indexHTML)
	for _, want := range []string{
		"post('/resume',{path:s.path,sessionId:s.sessionId,hostId:s.hostId,name:s.name})",
		"if(!r.ok){const detail=(await r.text().catch(()=>'')).trim();showNotice(__('resume_failed')",
		"'resume_failed': 'Could not switch to that session'",
		"'resume_failed': '无法切换到该会话'",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("serve index missing session resume handling %q", want)
		}
	}
}
