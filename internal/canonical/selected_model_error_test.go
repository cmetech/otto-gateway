package canonical

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSelectedModelErrorInfo_RecognizesOnlySafeSelectedModelErrors(t *testing.T) {
	cause := errors.New("secret-upstream-detail")
	cases := []struct {
		name        string
		err         error
		wantCode    string
		wantMessage string
	}{
		{
			name: "activation direct",
			err: &SelectedModelError{
				Code:  CodeSelectedModelActivationFailed,
				Cause: cause,
			},
			wantCode:    "selected_model_activation_failed",
			wantMessage: "The selected model could not be activated. Retry the request with model `auto`.",
		},
		{
			name: "tool protocol wrapped",
			err: fmt.Errorf("request failed: %w", &SelectedModelError{
				Code:  CodeSelectedModelToolProtocolFailed,
				Cause: cause,
			}),
			wantCode:    "selected_model_tool_protocol_failed",
			wantMessage: "The selected model did not produce a valid external tool call after one corrective attempt. Retry the request with model `auto`.",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			code, message, ok := SelectedModelErrorInfo(tt.err)
			if !ok {
				t.Fatal("SelectedModelErrorInfo() ok = false, want true")
			}
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if message != tt.wantMessage {
				t.Errorf("message = %q, want %q", message, tt.wantMessage)
			}
			if strings.Contains(message, "secret-upstream-detail") {
				t.Errorf("safe message leaked cause: %q", message)
			}
			if strings.Contains(tt.err.Error(), "secret-upstream-detail") {
				t.Errorf("Error() leaked cause: %q", tt.err.Error())
			}
		})
	}
}

func TestSelectedModelError_UnwrapsOnlyForServerSideClassification(t *testing.T) {
	cause := errors.New("secret-upstream-detail")
	err := &SelectedModelError{
		Code:  CodeSelectedModelActivationFailed,
		Cause: cause,
	}
	if got := errors.Unwrap(err); got != cause {
		t.Errorf("errors.Unwrap() = %v, want original cause", got)
	}
	if strings.Contains(err.Error(), "secret-upstream-detail") {
		t.Errorf("Error() leaked cause: %q", err.Error())
	}
}

func TestSelectedModelErrorInfo_RejectsUnrelatedAndUnknownErrors(t *testing.T) {
	cases := []error{
		errors.New("secret-upstream-detail"),
		fmt.Errorf("wrapped: %w", errors.New("secret-upstream-detail")),
		&SelectedModelError{Code: "unknown", Cause: errors.New("secret-upstream-detail")},
	}
	for _, err := range cases {
		if code, message, ok := SelectedModelErrorInfo(err); ok || code != "" || message != "" {
			t.Errorf("SelectedModelErrorInfo(%v) = (%q, %q, %v), want empty unrelated result", err, code, message, ok)
		}
	}
}
