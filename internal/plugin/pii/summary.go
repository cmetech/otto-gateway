package pii

import (
	"context"

	"otto-gateway/internal/privacy"
)

type (
	// RedactionCount reports the number of transformations for one entity.
	RedactionCount = privacy.RedactionCount
	// Summary accumulates bounded per-request privacy transformation counts.
	Summary = privacy.Summary
)

// NewSummary creates an empty privacy transformation summary.
func NewSummary() *Summary {
	return privacy.NewSummary()
}

// WithSummary attaches a privacy transformation summary to ctx.
func WithSummary(ctx context.Context, summary *Summary) context.Context {
	return privacy.WithSummary(ctx, summary)
}

// SummaryFromContext returns the privacy transformation summary attached to ctx.
func SummaryFromContext(ctx context.Context) (*Summary, bool) {
	return privacy.SummaryFromContext(ctx)
}
