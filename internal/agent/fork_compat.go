package agent

// ebmState is retained only to read and reproduce historical experiment fork
// bundles. The EBM and reasoning-governor enforcement paths are retired.
type ebmState struct {
	fired        bool
	captureArmed bool
	captured     bool
	captureRound int
}
