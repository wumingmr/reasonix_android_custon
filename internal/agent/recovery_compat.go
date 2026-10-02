package agent

// RecoveryGate is retained as a source-compatible option type. The execution
// runtime never stores or invokes it, so an old caller cannot reinstall Auto
// Guard by supplying a legacy gate implementation.
type RecoveryGate any

// RecoveryAction keeps retired UI and RPC payloads decodable. Controller
// rejects every value with recovery_retired.
type RecoveryAction string

const (
	RecoveryActionContinue     RecoveryAction = "continue"
	RecoveryActionContinueTask RecoveryAction = "continue_task"
	RecoveryActionRevise       RecoveryAction = "revise"
)
