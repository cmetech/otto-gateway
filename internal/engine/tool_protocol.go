package engine

import (
	"strings"

	"otto-gateway/internal/canonical"
)

// ToolProtocolReason is a bounded classification for an explicit model that
// did not satisfy the caller-tool protocol.
type ToolProtocolReason string

const (
	ReasonActivationFailed  ToolProtocolReason = "activation_failed"
	ReasonRequiredMissing   ToolProtocolReason = "required_missing"
	ReasonNamedMismatch     ToolProtocolReason = "named_mismatch"
	ReasonMalformedWrapper  ToolProtocolReason = "malformed_wrapper"
	ReasonCapabilityRefusal ToolProtocolReason = "capability_refusal"
	ReasonBuiltInToolDenied ToolProtocolReason = "built_in_tool_denied"
)

// ToolProtocolOutcome is a bounded recovery outcome.
type ToolProtocolOutcome string

const (
	OutcomeFirstAttempt ToolProtocolOutcome = "first_attempt"
	OutcomeCorrected    ToolProtocolOutcome = "corrected"
	OutcomeFailed       ToolProtocolOutcome = "failed"
	OutcomeBufferBypass ToolProtocolOutcome = "buffer_bypass"
)

// ToolProtocolEvent contains only bounded recovery metadata suitable for
// metrics. It intentionally carries no prompt, response, argument, or session
// data.
type ToolProtocolEvent struct {
	Model              string
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
	policy.tools = req.Tools
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "required", "any":
			policy.requirement = toolProtocolRequired
		case "tool":
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

	calls := observation.ToolCalls
	if len(calls) == 0 && !observation.NativeCall {
		calls = ExtractToolCallWrappers(observation.Text, policy.tools)
	}
	hasOfferedCall := false
	for _, call := range calls {
		name := call.Name
		if observation.NativeCall {
			var surfaced bool
			name, surfaced = ResolveNativeToolName(name, policy.tools, aliases)
			if !surfaced {
				continue
			}
		}
		if !toolOffered(name, policy.tools) {
			continue
		}
		hasOfferedCall = true
		if policy.requirement != toolProtocolNamed || name == policy.namedTool {
			return ""
		}
	}

	if observation.Final != nil && observation.Final.ToolDenials > 0 {
		return ReasonBuiltInToolDenied
	}
	if policy.requirement == toolProtocolNamed && hasOfferedCall {
		return ReasonNamedMismatch
	}
	if len(observation.ToolCalls) == 0 && !observation.NativeCall && hasWholeResponseToolCallMarker(observation.Text) {
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
	mentionsSuppliedTools := strings.Contains(n, "supplied connector tools") ||
		strings.Contains(n, "requested connector tools") ||
		strings.Contains(n, "tools you supplied")
	claimsUnavailable := strings.Contains(n, "not actually available") ||
		strings.Contains(n, "not available to me") ||
		strings.Contains(n, "can't execute") || strings.Contains(n, "cannot execute")
	return mentionsSuppliedTools && claimsUnavailable
}

// correctiveBlocks produces a static, auditable corrective prompt. The only
// interpolated value is a name already validated against req.Tools while the
// policy was constructed.
func correctiveBlocks(policy toolProtocolPolicy) []canonical.Block {
	message := "Use an offered external tool by emitting a valid tool call when a tool is needed. A normal final answer is acceptable if no tool is needed."
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
