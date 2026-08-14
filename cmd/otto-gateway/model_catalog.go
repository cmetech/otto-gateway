package main

import (
	"context"
	"errors"
	"time"

	"otto-gateway/internal/admin"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/registry"
)

type modelCatalogRuntime interface {
	CatalogSnapshot() pool.ModelCatalogSnapshot
	RefreshModelCatalog(context.Context) (pool.CatalogRefreshResult, error)
}

type adminModelCatalogAdapter struct {
	source modelCatalogRuntime
	reg    *registry.Registry
	now    func() time.Time
}

func (a adminModelCatalogAdapter) Snapshot() admin.ModelCatalogView {
	if a.source == nil || a.reg == nil {
		return admin.ModelCatalogView{State: "degraded", Models: []admin.ModelCatalogModel{}}
	}

	snapshot := a.source.CatalogSnapshot()
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	enriched := a.reg.Enrich(snapshot.Models, now)
	models := make([]admin.ModelCatalogModel, 0, len(enriched.Entries))
	for _, model := range enriched.Entries {
		models = append(models, admin.ModelCatalogModel{
			ID:            model.ID,
			Name:          model.Name,
			SelectionMode: model.SelectionMode,
			Capabilities: map[string]string{
				"completion": modelCatalogCapability(model.Capabilities["completion"]),
				"tools":      modelCatalogCapability(model.Capabilities["tools"]),
				"vision":     modelCatalogCapability(model.Capabilities["vision"]),
				"reasoning":  modelCatalogCapability(model.Capabilities["reasoning"]),
			},
		})
	}

	return admin.ModelCatalogView{
		State:      modelCatalogState(snapshot),
		Count:      len(models),
		Generation: snapshot.Generation,
		Models:     models,
		Refresh: admin.ModelCatalogRefreshView{
			Enabled:         snapshot.RefreshInterval > 0,
			IntervalSeconds: int64(snapshot.RefreshInterval / time.Second),
			InProgress:      snapshot.InProgress,
			LastAttemptAt:   modelCatalogTimestamp(snapshot.LastAttemptAt),
			LastSuccessAt:   modelCatalogTimestamp(snapshot.LastSuccessAt),
			LastUpdatedAt:   modelCatalogTimestamp(snapshot.LastUpdatedAt),
			NextAttemptAt:   modelCatalogTimestamp(snapshot.NextAttemptAt),
			LastOutcome:     modelCatalogOutcome(snapshot.LastOutcome),
			PendingRemovals: snapshot.PendingRemovals,
		},
	}
}

func (a adminModelCatalogAdapter) Refresh(ctx context.Context) admin.ModelCatalogActionResult {
	if a.source == nil {
		return admin.ModelCatalogActionResult{
			Code:    "catalog_refresh_unavailable",
			Message: "Model catalog refresh is unavailable.",
		}
	}

	result, err := a.source.RefreshModelCatalog(ctx)
	if err == nil {
		return admin.ModelCatalogActionResult{
			Outcome: modelCatalogOutcome(result.Outcome),
			Message: "Model catalog refresh completed.",
		}
	}

	retryAfterSeconds := 0
	var refreshErr *pool.CatalogRefreshError
	if errors.As(err, &refreshErr) {
		retryAfterSeconds = ceilModelCatalogSeconds(refreshErr.RetryAfter)
	}

	action := admin.ModelCatalogActionResult{
		Code:              "catalog_refresh_failed",
		Message:           "Model catalog refresh failed. The current catalog remains in use.",
		RetryAfterSeconds: retryAfterSeconds,
	}
	switch {
	case errors.Is(err, pool.ErrCatalogRefreshInProgress):
		action.Code = "catalog_refresh_in_progress"
		action.Message = "A model catalog refresh is already in progress."
	case errors.Is(err, pool.ErrCatalogRefreshCooldown):
		action.Code = "catalog_refresh_cooldown"
		action.Message = "Model catalog refresh is temporarily rate limited."
	case errors.Is(err, pool.ErrCatalogRefreshBusy):
		action.Code = "catalog_refresh_busy"
		action.Message = "No idle gateway worker is available for a model catalog refresh. The current catalog remains in use."
	case errors.Is(err, pool.ErrCatalogRefreshUnavailable):
		action.Code = "catalog_refresh_unavailable"
		action.Message = "Model catalog refresh is unavailable."
	}
	return action
}

func modelCatalogCapability(state canonical.CapabilityState) string {
	switch state {
	case canonical.CapSupported, canonical.CapUnsupported:
		return string(state)
	default:
		return string(canonical.CapUnknown)
	}
}

func modelCatalogState(snapshot pool.ModelCatalogSnapshot) string {
	switch {
	case snapshot.InProgress:
		return "refreshing"
	case snapshot.PendingRemovals > 0:
		return "pending_shrink"
	case len(snapshot.Models) == 0:
		return "degraded"
	case snapshot.RefreshInterval <= 0:
		return "disabled"
	default:
		return "ready"
	}
}

func modelCatalogOutcome(outcome pool.CatalogOutcome) string {
	switch outcome {
	case pool.CatalogStartup,
		pool.CatalogUnchanged,
		pool.CatalogExpanded,
		pool.CatalogMetadataUpdated,
		pool.CatalogPendingShrink,
		pool.CatalogShrinkConfirmed,
		pool.CatalogSkippedBusy,
		pool.CatalogFailed,
		pool.CatalogCancelled:
		return string(outcome)
	default:
		return string(pool.CatalogFailed)
	}
}

func modelCatalogTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func ceilModelCatalogSeconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Second - 1) / time.Second)
}
