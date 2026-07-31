package privacy

import (
	"errors"
	"net/http"
)

const (
	CodeRequestInvalid     = "privacy_request_invalid"
	CodeProfileUnavailable = "privacy_profile_unavailable"
	CodeScopeClosed        = "privacy_scope_closed"
	CodeInputBlocked       = "privacy_input_blocked"
	CodeOutputBlocked      = "privacy_output_blocked"
	CodeCapacityExceeded   = "privacy_capacity_exceeded"
	CodeInternalError      = "privacy_internal_error"
)

// Error is a bounded privacy failure. Cause is available only through Unwrap.
type Error struct {
	Code  string
	Stage string
	Cause error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Stage == "" {
		return e.Code
	}
	return e.Code + ": " + e.Stage
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ErrorInfo maps only approved privacy errors to public status/code pairs.
func ErrorInfo(err error) (status int, code string, ok bool) {
	var privacyErr *Error
	if !errors.As(err, &privacyErr) || privacyErr == nil {
		return 0, "", false
	}
	switch privacyErr.Code {
	case CodeRequestInvalid, CodeProfileUnavailable:
		return http.StatusBadRequest, privacyErr.Code, true
	case CodeScopeClosed:
		return http.StatusConflict, privacyErr.Code, true
	case CodeInputBlocked:
		return http.StatusUnprocessableEntity, privacyErr.Code, true
	case CodeOutputBlocked:
		return http.StatusBadGateway, privacyErr.Code, true
	case CodeCapacityExceeded, CodeInternalError:
		return http.StatusServiceUnavailable, privacyErr.Code, true
	default:
		return 0, "", false
	}
}
