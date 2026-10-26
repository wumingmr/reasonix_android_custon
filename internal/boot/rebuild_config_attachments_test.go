package boot

import (
	"testing"

	"reasonix/internal/config"
)

func TestRuntimeConfigFingerprintIgnoresUploadAdmissionPolicy(t *testing.T) {
	isolateConfigHome(t)
	t.Setenv("REASONIX_PROJECTION_TEST_KEY", "fixture-key")
	cfg := projectionConfig(t)
	before := runtimeConfigFingerprint(cfg)
	if before == "" {
		t.Fatal("fixture has no reusable runtime fingerprint")
	}
	for _, limit := range []int64{8, 0, config.DefaultAttachmentStreamBytes, -1} {
		cfg.Attachments.MaxStreamBytes = &limit
		if got := runtimeConfigFingerprint(cfg); got != before {
			t.Fatalf("upload ceiling %d invalidated controller reuse", limit)
		}
	}
	cfg.Attachments.MaxStreamBytes = nil
	if got := runtimeConfigFingerprint(cfg); got != before {
		t.Fatal("restoring the default upload ceiling invalidated controller reuse")
	}
}
