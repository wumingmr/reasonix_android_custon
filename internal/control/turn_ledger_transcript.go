package control

import (
	"log/slog"
	"slices"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/turnevent"
)

func (c *Controller) updateTurnLedgerTranscript(ledger *turnevent.Ledger) *provider.ReadCompletion {
	if c.executor != nil && c.executor.Session() != nil {
		session := c.executor.Session()
		messages, _, rewrite := session.DisplayBaseline()
		ledger.SetTranscriptRewriteEpoch(rewrite)
		digest, digestErr := session.ContentDigest()
		if digestErr != nil {
			slog.Warn("controller: compute terminal transcript digest", "err", digestErr)
		} else {
			ledger.SetTranscriptSnapshot(int64(session.TranscriptVersion()), digest)
		}
		if ref, ok := session.Head(); ok {
			ledger.SetTranscriptHead(ref.HeadID, session.LeafID())
		} else {
			ledger.SetTranscriptHead("", "")
		}
		for _, message := range slices.Backward(messages) {
			if message.ReadCompletion != nil {
				return message.ReadCompletion
			}
			if agent.IsUserAuthoredTurnMessage(message) {
				break
			}
		}
	}
	return nil
}
