package engine

import (
	"strings"

	"otto-gateway/internal/canonical"
)

// ToolProtocolReason is a bounded classification for an explicit model that
// did not satisfy the caller-tool protocol.
type ToolProtocolReason string

const (
	// ReasonActivationFailed indicates that the selected model could not be activated.
	ReasonActivationFailed ToolProtocolReason = "activation_failed"
	// ReasonRequiredMissing indicates that a required tool call was absent.
	ReasonRequiredMissing ToolProtocolReason = "required_missing"
	// ReasonNamedMismatch indicates that the call did not target the required named tool.
	ReasonNamedMismatch ToolProtocolReason = "named_mismatch"
	// ReasonMalformedWrapper indicates that a textual tool-call wrapper was malformed.
	ReasonMalformedWrapper ToolProtocolReason = "malformed_wrapper"
	// ReasonCapabilityRefusal indicates that the model refused the caller-tool protocol.
	ReasonCapabilityRefusal ToolProtocolReason = "capability_refusal"
	// ReasonBuiltInToolDenied indicates that the model attempted a denied built-in tool.
	ReasonBuiltInToolDenied ToolProtocolReason = "built_in_tool_denied"
	// ReasonEmbeddedDispatcherWrapper indicates a valid deferred wrapper surrounded by forbidden prose.
	ReasonEmbeddedDispatcherWrapper ToolProtocolReason = "embedded_dispatcher_wrapper"
)

// ToolProtocolOutcome is a bounded recovery outcome.
type ToolProtocolOutcome string

const (
	// OutcomeFirstAttempt indicates that the first model response satisfied the protocol.
	OutcomeFirstAttempt ToolProtocolOutcome = "first_attempt"
	// OutcomeCorrected indicates that the single corrective prompt satisfied the protocol.
	OutcomeCorrected ToolProtocolOutcome = "corrected"
	// OutcomeFailed indicates that protocol recovery did not succeed.
	OutcomeFailed ToolProtocolOutcome = "failed"
	// OutcomeBufferBypass indicates that bounded preflight failed open without retry.
	OutcomeBufferBypass ToolProtocolOutcome = "buffer_bypass"
)

// ToolProtocolEvent contains only bounded recovery metadata suitable for
// metrics. It intentionally carries no prompt, response, argument, or session
// data.
type ToolProtocolEvent struct {
	Model              string
	RequestID          string
	Reason             ToolProtocolReason
	Outcome            ToolProtocolOutcome
	CorrectiveAttempts int
	RecommendAuto      bool
}

type toolProtocolRequirement string

const (
	toolProtocolOptional toolProtocolRequirement = "optional"
	toolProtocolRequired toolProtocolRequirement = "required"
	toolProtocolNamed    toolProtocolRequirement = "named"
)

type toolProtocolPolicy struct {
	requirement toolProtocolRequirement
	namedTool   string
	tools       []canonical.ToolSpec
	contractV1  bool
}

// attemptObservation is deliberately limited to the data produced by the
// bounded preflight capture. It never retains a request transcript or tool
// schema beyond the policy's offered-name validation.
type attemptObservation struct {
	Text         string
	ToolCalls    []canonical.ToolCall
	Final        *canonical.FinalResult
	NativeCall   bool
	BufferBypass bool
}

// toolProtocolPolicyFor returns a policy only for a selected-model,
// caller-tool decision turn. Tool-result and assistant turns are final-answer
// turns and must never be retried as tool-decision failures.
func toolProtocolPolicyFor(req *canonical.ChatRequest) (toolProtocolPolicy, bool) {
	policy := toolProtocolPolicy{requirement: toolProtocolOptional}
	if req == nil {
		return policy, false
	}
	policy.contractV1 = req.ToolContractVersion == "v1"
	policy.tools = req.Tools
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "required", "any":
			policy.requirement = toolProtocolRequired
		case "tool", "function":
			if toolOffered(req.ToolChoice.Name, req.Tools) {
				policy.requirement = toolProtocolNamed
				policy.namedTool = req.ToolChoice.Name
			}
		}
	}

	if req.Model == "" || req.Model == "auto" || len(req.Tools) == 0 {
		return policy, false
	}
	if req.ToolChoice != nil && req.ToolChoice.Type == "none" {
		return policy, false
	}
	if len(req.Messages) == 0 {
		return policy, false
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != canonical.RoleUser {
		return policy, false
	}
	for _, content := range last.Content {
		if content.Kind == canonical.ContentKindToolResult {
			return policy, false
		}
	}
	return policy, true
}

// classifyToolProtocolAttempt reports only high-confidence correctable
// failures. A zero reason means the response should pass through unchanged.
func classifyToolProtocolAttempt(policy toolProtocolPolicy, observation attemptObservation, aliases map[string]string) ToolProtocolReason {
	if observation.BufferBypass {
		return ""
	}

	hasOfferedCall := false
	matchesPolicy := func(name string) bool {
		if !toolOffered(name, policy.tools) {
			return false
		}
		hasOfferedCall = true
		return policy.requirement != toolProtocolNamed || name == policy.namedTool
	}

	// Native chunks and text wrappers are independent candidates. A model can
	// emit an unusable native built-in before a valid caller wrapper; the
	// unusable candidate must not suppress wrapper validation.
	for _, call := range observation.ToolCalls {
		name := call.Name
		if observation.NativeCall {
			var surfaced bool
			name, surfaced = ResolveNativeToolName(name, policy.tools, aliases)
			if !surfaced {
				continue
			}
		}
		if matchesPolicy(name) {
			return ""
		}
	}
	wrapperCalls := ExtractToolCallWrappers(observation.Text, policy.tools)
	for _, call := range wrapperCalls {
		if matchesPolicy(call.Name) {
			return ""
		}
	}
	if dispatcher := findToolCallDispatcher(policy.tools); policy.contractV1 && dispatcher != nil &&
		(policy.requirement == toolProtocolRequired || policy.requirement == toolProtocolNamed && policy.namedTool == dispatcher.Name) &&
		ObserveToolCallWrappers(observation.Text, policy.tools) == WrapperDispatcherEmbedded {
		return ReasonEmbeddedDispatcherWrapper
	}

	if observation.Final != nil && observation.Final.ToolDenials > 0 {
		return ReasonBuiltInToolDenied
	}
	if policy.requirement == toolProtocolNamed && hasOfferedCall {
		return ReasonNamedMismatch
	}
	if len(wrapperCalls) == 0 && hasWholeResponseToolCallMarker(observation.Text) {
		return ReasonMalformedWrapper
	}
	if isHighConfidenceToolCapabilityRefusal(observation.Text) {
		return ReasonCapabilityRefusal
	}
	if policy.requirement == toolProtocolRequired || policy.requirement == toolProtocolNamed {
		return ReasonRequiredMissing
	}
	return ""
}

func hasWholeResponseToolCallMarker(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, `{"tool_call"`)
}

func isHighConfidenceToolCapabilityRefusal(text string) bool {
	n := strings.ToLower(strings.TrimSpace(text))
	structuredUnavailable := strings.HasPrefix(n, "toolunavailable:") &&
		strings.Contains(n, "not available in this environment")
	mentionsSuppliedTools := strings.Contains(n, "supplied connector tools") ||
		strings.Contains(n, "requested connector tools") ||
		strings.Contains(n, "tools you supplied")
	claimsUnavailable := strings.Contains(n, "not actually available") ||
		strings.Contains(n, "not available to me") ||
		strings.Contains(n, "can't execute") || strings.Contains(n, "cannot execute")
	return structuredUnavailable || mentionsSuppliedTools && claimsUnavailable
}

// correctiveBlocks produces a static, auditable corrective prompt. The only
// interpolated value is a name already validated against req.Tools while the
// policy was constructed.
func correctiveBlocks(policy toolProtocolPolicy) []canonical.Block {
	message := "Use an offered external tool by emitting a valid tool call when a tool is needed. A normal final answer is acceptable if no tool is needed."
	if policy.contractV1 && (policy.requirement == toolProtocolRequired || policy.requirement == toolProtocolNamed) {
		message = "Your previous response attempted a tool call but violated the caller-tool protocol. Emit exactly one structured call to an offered tool. For a deferred tool, emit only the declared outer dispatcher wrapper as exact whole-response JSON: no narration, Markdown fence, waiting text, or other bytes. Do not name or execute an unoffered tool directly. A final prose answer is not acceptable on this attempt."
		return []canonical.Block{{
			Kind: canonical.BlockKindText,
			Text: &canonical.TextBlock{Content: message},
		}}
	}
	switch policy.requirement {
	case toolProtocolRequired:
		message = "Call one of the offered external tools by emitting a valid tool call. A normal final answer is not acceptable."
	case toolProtocolNamed:
		if toolOffered(policy.namedTool, policy.tools) {
			message = "Call the offered external tool `" + policy.namedTool + "` by emitting a valid tool call. A normal final answer is not acceptable."
		}
	}
	return []canonical.Block{{
		Kind: canonical.BlockKindText,
		Text: &canonical.TextBlock{Content: message},
	}}
}
