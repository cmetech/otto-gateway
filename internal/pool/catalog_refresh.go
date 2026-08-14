package pool

import (
	"context"
	"errors"
	"time"
)

var (
	ErrCatalogRefreshInProgress  = errors.New("model catalog refresh already in progress")
	ErrCatalogRefreshBusy        = errors.New("model catalog refresh requires an idle pool slot")
	ErrCatalogRefreshCooldown    = errors.New("model catalog manual refresh cooldown")
	ErrCatalogRefreshUnavailable = errors.New("model catalog refresh unavailable")
)

const (
	catalogManualCooldown = 30 * time.Second
	catalogBusyRetryAfter = time.Second
)

// CatalogRefreshError classifies a bounded operator-action failure and carries
// a safe retry delay for consumers that expose Retry-After.
type CatalogRefreshError struct {
	Kind       error
	RetryAfter time.Duration
}

func (e *CatalogRefreshError) Error() string { return e.Kind.Error() }
func (e *CatalogRefreshError) Unwrap() error { return e.Kind }

type catalogRefreshSource string

const (
	catalogRefreshLazy      catalogRefreshSource = "lazy"
	catalogRefreshScheduled catalogRefreshSource = "scheduled"
	catalogRefreshManual    catalogRefreshSource = "manual"
)

type catalogRefreshAdmission struct {
	source    catalogRefreshSource
	attemptAt time.Time
}

// CatalogSnapshot returns a defensive view of the published model catalog and
// refresh lifecycle.
func (p *Pool) CatalogSnapshot() ModelCatalogSnapshot {
	return p.catalog.snapshot()
}

// RefreshModelCatalog performs one operator-requested refresh using an
// immediately idle slot. It never queues behind client traffic.
func (p *Pool) RefreshModelCatalog(ctx context.Context) (CatalogRefreshResult, error) {
	return p.refreshModelCatalog(ctx, catalogRefreshManual)
}

func (p *Pool) refreshModelCatalog(ctx context.Context, source catalogRefreshSource) (CatalogRefreshResult, error) {
	admission, err := p.admitCatalogRefresh(source)
	if err != nil {
		return CatalogRefreshResult{}, err
	}
	return p.runCatalogRefresh(ctx, admission)
}

// admitCatalogRefresh preserves the shutdown ordering contract: cooldown is
// checked before the shared CAS, and probeWG.Add shares the same p.mu critical
// section as the closed-state decision.
func (p *Pool) admitCatalogRefresh(source catalogRefreshSource) (catalogRefreshAdmission, error) {
	now := p.catalogNow()
	if source == catalogRefreshManual {
		p.catalogManualMu.Lock()
		lastManual := p.catalogLastManual
		p.catalogManualMu.Unlock()
		if !lastManual.IsZero() {
			elapsed := now.Sub(lastManual)
			if elapsed < catalogManualCooldown {
				retryAfter := catalogManualCooldown - elapsed
				if retryAfter > catalogManualCooldown {
					retryAfter = catalogManualCooldown
				}
				return catalogRefreshAdmission{}, &CatalogRefreshError{Kind: ErrCatalogRefreshCooldown, RetryAfter: retryAfter}
			}
		}
	}
	if !p.catalogProbing.CompareAndSwap(false, true) {
		return catalogRefreshAdmission{}, &CatalogRefreshError{Kind: ErrCatalogRefreshInProgress, RetryAfter: catalogProbeTimeout}
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.catalogProbing.Store(false)
		return catalogRefreshAdmission{}, &CatalogRefreshError{Kind: ErrCatalogRefreshUnavailable, RetryAfter: catalogBusyRetryAfter}
	}
	p.probeWG.Add(1)
	if source == catalogRefreshManual {
		p.catalogManualMu.Lock()
		p.catalogLastManual = now
		p.catalogManualMu.Unlock()
	}
	p.mu.Unlock()
	p.catalog.setInProgress(true)
	return catalogRefreshAdmission{source: source, attemptAt: now}, nil
}

func (p *Pool) runCatalogRefresh(ctx context.Context, admission catalogRefreshAdmission) (result CatalogRefreshResult, err error) {
	defer p.probeWG.Done()
	defer p.catalogProbing.Store(false)
	defer p.catalog.setInProgress(false)
	started := time.Now()

	var slot *Slot
	select {
	case slot = <-p.slots:
	default:
		if admission.source == catalogRefreshScheduled {
			result = p.catalog.recordRefreshOutcome(CatalogSkippedBusy, admission.attemptAt)
			p.logCatalogRefresh(admission.source, result, time.Since(started))
			return result, nil
		}
		if admission.source == catalogRefreshManual {
			return CatalogRefreshResult{}, &CatalogRefreshError{Kind: ErrCatalogRefreshBusy, RetryAfter: catalogBusyRetryAfter}
		}
		return CatalogRefreshResult{}, nil
	}
	p.markSlotCheckedOut(slot)
	defer func() { p.releaseOrRecycle(slot) }()

	if !p.slotAlive(slot) {
		result = p.catalog.recordRefreshOutcome(CatalogFailed, admission.attemptAt)
		p.logCatalogRefresh(admission.source, result, time.Since(started))
		return result, &CatalogRefreshError{Kind: ErrCatalogRefreshUnavailable, RetryAfter: catalogBusyRetryAfter}
	}

	probeCtx, cancel := context.WithTimeout(ctx, catalogProbeTimeout)
	stopPoolCancel := context.AfterFunc(p.catalogCtx, cancel)
	if p.catalogCtx.Err() != nil {
		cancel()
	}
	defer stopPoolCancel()
	defer cancel()

	models, probeErr := p.probeCatalogOnce(probeCtx, slot)
	if probeErr != nil {
		outcome := CatalogFailed
		returnedErr := probeErr
		if ctxErr := ctx.Err(); ctxErr != nil {
			outcome = CatalogCancelled
			returnedErr = ctxErr
		} else if lifecycleErr := p.catalogCtx.Err(); lifecycleErr != nil {
			outcome = CatalogCancelled
			returnedErr = lifecycleErr
		}
		result = p.catalog.recordRefreshOutcome(outcome, admission.attemptAt)
		p.logCatalogRefresh(admission.source, result, time.Since(started))
		return result, returnedErr
	}

	result, err = p.catalog.reconcile(models, admission.attemptAt)
	p.logCatalogRefresh(admission.source, result, time.Since(started))
	return result, err
}

func (p *Pool) startCatalogScheduler() {
	if p.cfg.ModelCatalogRefreshInterval <= 0 {
		return
	}
	p.catalogSchedulerLifecycleMu.Lock()
	defer p.catalogSchedulerLifecycleMu.Unlock()
	if p.catalogSchedulerClosing {
		return
	}
	p.catalogSchedulerOnce.Do(func() {
		p.catalogSchedulerWG.Add(1)
		go p.catalogRefreshLoop()
	})
}

func (p *Pool) catalogRefreshLoop() {
	defer p.catalogSchedulerWG.Done()
	ticks := p.catalogRefreshTicks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(p.cfg.ModelCatalogRefreshInterval)
		ticks = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-ticks:
			_, _ = p.refreshModelCatalog(context.Background(), catalogRefreshScheduled)
		case <-p.closing:
			return
		}
	}
}

func (p *Pool) logCatalogRefresh(source catalogRefreshSource, result CatalogRefreshResult, duration time.Duration) {
	if p.cfg.Logger == nil {
		return
	}
	p.cfg.Logger.Info(
		"pool: model catalog refresh",
		"source", string(source),
		"outcome", string(result.Outcome),
		"previous_count", result.PreviousCount,
		"candidate_count", result.CandidateCount,
		"published_count", result.PublishedCount,
		"pending_removals", result.PendingRemovals,
		"duration", duration,
	)
}

// recordRefreshOutcome stores a bounded terminal result without performing
// ACP, channel, recycle, or logging work while the catalog mutex is held.
func (s *catalogStore) recordRefreshOutcome(outcome CatalogOutcome, at time.Time) CatalogRefreshResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAttemptAt = at
	s.nextAttemptAt = nextCatalogAttempt(at, s.interval)
	s.lastOutcome = outcome
	return s.resultLocked(outcome, len(s.models), 0)
}
