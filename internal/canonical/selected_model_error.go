package canonical

import "errors"

const (
	// CodeSelectedModelActivationFailed identifies an explicit model that could not be activated.
	CodeSelectedModelActivationFailed = "selected_model_activation_failed"
	// CodeSelectedModelToolProtocolFailed identifies an explicit model that failed caller-tool recovery.
	CodeSelectedModelToolProtocolFailed = "selected_model_tool_protocol_failed"
)

var selectedModelMessages = map[string]string{
	CodeSelectedModelActivationFailed:   "The selected model could not be activated. Retry the request with model `auto`.",
	CodeSelectedModelToolProtocolFailed: "The selected model did not produce a valid external tool call after one corrective attempt. Retry the request with model `auto`.",
}

// SelectedModelError is a client-safe selected-model failure. Cause remains
// available to server-side error handling through Unwrap, but is never
// included in the public Error text.
type SelectedModelError struct {
	Code  string
	Cause error
}

// Error returns a closed public message and never exposes Cause.
func (e *SelectedModelError) Error() string {
	if e != nil {
		if message, ok := selectedModelMessages[e.Code]; ok {
			return message
		}
	}
	return "The selected model request failed."
}

// Unwrap retains the original cause for server-side classification and logs.
func (e *SelectedModelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// SelectedModelErrorInfo extracts a recognized stable code and its safe public
// message. Unknown codes fail closed so callers retain their ordinary error
// handling rather than presenting an invented selected-model protocol error.
func SelectedModelErrorInfo(err error) (code, message string, ok bool) {
	var selected *SelectedModelError
	if !errors.As(err, &selected) || selected == nil {
		return "", "", false
	}
	message, ok = selectedModelMessages[selected.Code]
	if !ok {
		return "", "", false
	}
	return selected.Code, message, true
}
