package pii

import (
	"context"

	"otto-gateway/internal/privacy"
)

type (
	RedactionCount = privacy.RedactionCount
	Summary        = privacy.Summary
)

func NewSummary() *Summary {
	return privacy.NewSummary()
}

func WithSummary(ctx context.Context, summary *Summary) context.Context {
	return privacy.WithSummary(ctx, summary)
}

func SummaryFromContext(ctx context.Context) (*Summary, bool) {
	return privacy.SummaryFromContext(ctx)
}
