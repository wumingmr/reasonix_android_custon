package config

import (
	"fmt"
	"strings"
)

// Keep unset keys absent so project saves do not shadow user retention values.
func renderCheckpointsConfig(b *strings.Builder, c CheckpointsConfig) {
	if c.RetainTurns == 0 && c.BlobQuotaBytes == 0 {
		return
	}
	b.WriteString("[checkpoints]\n")
	if c.RetainTurns != 0 {
		fmt.Fprintf(b, "retain_turns = %d\n", c.RetainTurns)
	}
	if c.BlobQuotaBytes != 0 {
		fmt.Fprintf(b, "blob_quota_bytes = %d\n", c.BlobQuotaBytes)
	}
	b.WriteString("\n")
}
