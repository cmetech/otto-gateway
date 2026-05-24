// Package canonical defines the typed chunk and block types that flow through
// the OTTO Gateway. This package imports nothing under internal/.
package canonical

// StopReason classifies why a prompt turn ended. Values mirror the ACP spec
// stopReason enum (end_turn | max_tokens | max_turn_requests | refusal |
// cancelled) plus StopUnknown for forward compatibility with values kiro-cli
// may emit that the spec does not (yet) enumerate.
type StopReason int

const (
	// StopUnknown is the zero value. It signals either an unparsed wire
	// string or a teardown that did not produce a stopReason (e.g. abrupt
	// client close). Forward-compat default per Phase 1.1 D-02.
	StopUnknown StopReason = iota
	// StopEndTurn means the agent finished its turn naturally.
	StopEndTurn
	// StopMaxTokens means the turn ended because the agent hit a token limit.
	StopMaxTokens
	// StopMaxTurnRequests means the turn ended because the agent hit a
	// max-turn-requests limit.
	StopMaxTurnRequests
	// StopRefusal means the agent refused the prompt.
	StopRefusal
	// StopCancelled means the turn was cancelled by the client.
	StopCancelled
)
