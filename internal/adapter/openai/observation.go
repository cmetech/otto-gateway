package openai

import (
	"context"
	"errors"
	"strings"

	"otto-gateway/internal/canonical"
)

// RequestObservation is the bounded, adapter-owned final request outcome passed
// to the consumer. The gateway command bridges it to Prometheus without making
// this adapter depend on internal/metrics.
type RequestObservation struct {
	Outcome     string
	Stream      string
	SessionMode string
}

func newRequestObservation() *RequestObservation {
	return &RequestObservation{
		Outcome:     "internal_error",
		Stream:      "unknown",
		SessionMode: "unknown",
	}
}

func (a *Adapter) observeRequest(observation *RequestObservation) {
	if a.cfg.ObserveRequest != nil {
		a.cfg.ObserveRequest(*observation)
	}
}

func classifyRequestError(err error) string {
	switch {
	case errors.Is(err, canonical.ErrPoolExhausted):
		return "pool_exhausted"
	case errors.Is(err, canonical.ErrStreamIdleTimeout):
		return "stream_idle_timeout"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "client_cancelled"
	default:
		return "upstream_error"
	}
}

func classifyStreamingError(err error) string {
	if err == nil {
		return "success"
	}
	message := err.Error()
	if strings.Contains(message, "response writer is not flusher") ||
		strings.Contains(message, "marshal") ||
		strings.Contains(message, "write") {
		return "internal_error"
	}
	return classifyRequestError(err)
}
