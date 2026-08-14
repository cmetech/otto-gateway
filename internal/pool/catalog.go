package pool

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"otto-gateway/internal/canonical"
)

var (
	errInvalidCatalog = errors.New("model catalog contains a blank model ID")
	errEmptyCatalog   = errors.New("model catalog contains no selectable models")
)

// CatalogOutcome describes the effect of a catalog observation on the
// published catalog.
type CatalogOutcome string

const (
	// CatalogStartup records the initial catalog observation.
	CatalogStartup CatalogOutcome = "startup"
	// CatalogUnchanged records an observation identical to the published catalog.
	CatalogUnchanged CatalogOutcome = "unchanged"
	// CatalogExpanded records an observation that immediately adds models.
	CatalogExpanded CatalogOutcome = "expanded"
	// CatalogMetadataUpdated records changed metadata for the same model IDs.
	CatalogMetadataUpdated CatalogOutcome = "metadata_updated"
	// CatalogPendingShrink records the first observation of missing models.
	CatalogPendingShrink CatalogOutcome = "pending_shrink"
	// CatalogShrinkConfirmed records the matching observation that confirms removal.
	CatalogShrinkConfirmed CatalogOutcome = "shrink_confirmed"
	// CatalogSkippedBusy records a scheduled probe skipped for lack of an idle worker.
	CatalogSkippedBusy CatalogOutcome = "skipped_busy"
	// CatalogFailed records an invalid or failed catalog observation.
	CatalogFailed CatalogOutcome = "failed"
	// CatalogCancelled records an observation cancelled before completion.
	CatalogCancelled CatalogOutcome = "cancelled"
)

// ModelCatalogSnapshot is a defensive copy of the catalog store's published
// state and refresh lifecycle information.
type ModelCatalogSnapshot struct {
	Models                                      []canonical.ModelInfo
	Generation                                  uint64
	RefreshInterval                             time.Duration
	InProgress                                  bool
	LastAttemptAt, LastSuccessAt, LastUpdatedAt time.Time
	NextAttemptAt                               time.Time
	LastOutcome                                 CatalogOutcome
	PendingRemovals                             int
}

// CatalogRefreshResult summarizes one catalog observation without exposing
// any upstream probe data.
type CatalogRefreshResult struct {
	Outcome                                                        CatalogOutcome
	PreviousCount, CandidateCount, PublishedCount, PendingRemovals int
	RetryAfter                                                     time.Duration
}

// catalogStore is the sole owner of the published catalog. A reduced catalog
// must be observed twice with the same ID set before it replaces published
// state, while additions are available on their first valid observation.
type catalogStore struct {
	mu sync.RWMutex

	models     []canonical.ModelInfo
	generation uint64
	interval   time.Duration
	inProgress bool

	lastAttemptAt time.Time
	lastSuccessAt time.Time
	lastUpdatedAt time.Time
	nextAttemptAt time.Time
	lastOutcome   CatalogOutcome

	pendingCandidate []canonical.ModelInfo
	pendingRemovals  int
}

func newCatalogStore(interval time.Duration) *catalogStore {
	return &catalogStore{interval: interval}
}

// initialize establishes the initial published catalog. Warmup can be
// degraded, so an invalid initial observation is recorded without publishing
// a catalog.
func (s *catalogStore) initialize(models []canonical.ModelInfo, at time.Time) {
	candidate, err := normalizeCatalog(models)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAttemptAt = at
	s.nextAttemptAt = nextCatalogAttempt(at, s.interval)
	if err != nil {
		s.lastOutcome = CatalogFailed
		return
	}
	s.models = cloneCatalog(candidate)
	s.generation++
	s.lastSuccessAt = at
	s.lastUpdatedAt = at
	s.lastOutcome = CatalogStartup
	s.clearPendingLocked()
}

// reconcile normalizes one completed probe and atomically applies the safe
// portion of its result. Its input is always copied before it is retained.
func (s *catalogStore) reconcile(models []canonical.ModelInfo, at time.Time) (CatalogRefreshResult, error) {
	return s.reconcileCompleted(models, at, at)
}

// reconcileCompleted records admission and completion independently. The
// public snapshot's attempt time describes admission, while successful probe
// and publication timestamps describe when the observation finished.
func (s *catalogStore) reconcileCompleted(models []canonical.ModelInfo, attemptAt, completedAt time.Time) (CatalogRefreshResult, error) {
	candidate, err := normalizeCatalog(models)

	s.mu.Lock()
	defer s.mu.Unlock()

	previousCount := len(s.models)
	s.lastAttemptAt = attemptAt
	s.nextAttemptAt = nextCatalogAttempt(attemptAt, s.interval)
	if err != nil {
		s.lastOutcome = CatalogFailed
		return s.resultLocked(CatalogFailed, previousCount, 0), err
	}

	s.lastSuccessAt = completedAt
	publishedIDs := catalogIDs(s.models)
	candidateIDs := catalogIDs(candidate)
	publishedMissing := countMissing(publishedIDs, candidateIDs)
	candidateAdditions := countMissing(candidateIDs, publishedIDs)

	switch {
	case publishedMissing == 0 && candidateAdditions == 0:
		s.clearPendingLocked()
		if catalogMetadataEqual(s.models, candidate) {
			s.lastOutcome = CatalogUnchanged
			return s.resultLocked(CatalogUnchanged, previousCount, len(candidate)), nil
		}
		s.publishLocked(candidate, completedAt)
		s.lastOutcome = CatalogMetadataUpdated
		return s.resultLocked(CatalogMetadataUpdated, previousCount, len(candidate)), nil

	case publishedMissing == 0:
		s.clearPendingLocked()
		s.publishLocked(candidate, completedAt)
		s.lastOutcome = CatalogExpanded
		return s.resultLocked(CatalogExpanded, previousCount, len(candidate)), nil

	case candidateAdditions == 0 && catalogIDSetEqual(s.pendingCandidate, candidate):
		// Confirmation covers only exact IDs: model metadata and source order
		// may legitimately change between two observations of the same set. The
		// current confirming observation is the one that gets published.
		s.publishLocked(candidate, completedAt)
		s.clearPendingLocked()
		s.lastOutcome = CatalogShrinkConfirmed
		return s.resultLocked(CatalogShrinkConfirmed, previousCount, len(candidate)), nil

	default:
		if candidateAdditions > 0 {
			s.publishLocked(mergeCatalogAdditions(s.models, candidate), completedAt)
		}
		s.stageShrinkLocked(candidate)
		s.lastOutcome = CatalogPendingShrink
		return s.resultLocked(CatalogPendingShrink, previousCount, len(candidate)), nil
	}
}

func (s *catalogStore) snapshot() ModelCatalogSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ModelCatalogSnapshot{
		Models:          cloneCatalog(s.models),
		Generation:      s.generation,
		RefreshInterval: s.interval,
		InProgress:      s.inProgress,
		LastAttemptAt:   s.lastAttemptAt,
		LastSuccessAt:   s.lastSuccessAt,
		LastUpdatedAt:   s.lastUpdatedAt,
		NextAttemptAt:   s.nextAttemptAt,
		LastOutcome:     s.lastOutcome,
		PendingRemovals: s.pendingRemovals,
	}
}

func (s *catalogStore) setInProgress(inProgress bool) {
	s.mu.Lock()
	s.inProgress = inProgress
	s.mu.Unlock()
}

func (s *catalogStore) publishLocked(models []canonical.ModelInfo, at time.Time) {
	s.models = cloneCatalog(models)
	s.generation++
	s.lastUpdatedAt = at
}

func (s *catalogStore) stageShrinkLocked(candidate []canonical.ModelInfo) {
	s.pendingCandidate = cloneCatalog(candidate)
	s.pendingRemovals = countMissing(catalogIDs(s.models), catalogIDs(candidate))
}

func (s *catalogStore) clearPendingLocked() {
	s.pendingCandidate = nil
	s.pendingRemovals = 0
}

func (s *catalogStore) resultLocked(outcome CatalogOutcome, previousCount, candidateCount int) CatalogRefreshResult {
	return CatalogRefreshResult{
		Outcome:         outcome,
		PreviousCount:   previousCount,
		CandidateCount:  candidateCount,
		PublishedCount:  len(s.models),
		PendingRemovals: s.pendingRemovals,
	}
}

func normalizeCatalog(models []canonical.ModelInfo) ([]canonical.ModelInfo, error) {
	normalized := make([]canonical.ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		model.Name = strings.TrimSpace(model.Name)
		if model.ID == "" {
			return nil, errInvalidCatalog
		}
		if model.ID == "auto" {
			continue
		}
		if _, duplicate := seen[model.ID]; duplicate {
			continue
		}
		seen[model.ID] = struct{}{}
		normalized = append(normalized, model)
	}
	if len(normalized) == 0 {
		return nil, errEmptyCatalog
	}
	return normalized, nil
}

func cloneCatalog(models []canonical.ModelInfo) []canonical.ModelInfo {
	if models == nil {
		return nil
	}
	cloned := make([]canonical.ModelInfo, len(models))
	copy(cloned, models)
	return cloned
}

func catalogIDs(models []canonical.ModelInfo) map[string]struct{} {
	ids := make(map[string]struct{}, len(models))
	for _, model := range models {
		ids[model.ID] = struct{}{}
	}
	return ids
}

func countMissing(left, right map[string]struct{}) int {
	missing := 0
	for id := range left {
		if _, found := right[id]; !found {
			missing++
		}
	}
	return missing
}

func catalogMetadataEqual(published, candidate []canonical.ModelInfo) bool {
	if len(published) != len(candidate) {
		return false
	}
	byID := make(map[string]canonical.ModelInfo, len(published))
	for _, model := range published {
		byID[model.ID] = model
	}
	for _, model := range candidate {
		if publishedModel, found := byID[model.ID]; !found || publishedModel != model {
			return false
		}
	}
	return true
}

func mergeCatalogAdditions(published, candidate []canonical.ModelInfo) []canonical.ModelInfo {
	candidateIDs := catalogIDs(candidate)
	merged := cloneCatalog(candidate)
	for _, model := range published {
		if _, present := candidateIDs[model.ID]; !present {
			merged = append(merged, model)
		}
	}
	return merged
}

func catalogSortedIDs(models []canonical.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	sort.Strings(ids)
	return ids
}

func catalogIDSetEqual(left, right []canonical.ModelInfo) bool {
	leftIDs := catalogSortedIDs(left)
	rightIDs := catalogSortedIDs(right)
	if len(leftIDs) != len(rightIDs) {
		return false
	}
	for i := range leftIDs {
		if leftIDs[i] != rightIDs[i] {
			return false
		}
	}
	return true
}

func nextCatalogAttempt(at time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return time.Time{}
	}
	return at.Add(interval)
}
