package privacy

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Profile selects the privacy policy applied to a scope.
type Profile string

const (
	ProfileStandard Profile = "standard"
	ProfileStrict   Profile = "strict"
)

// Ticker is the clock-owned ticker contract used by privacy lifecycle code.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Clock supplies wall-clock time without coupling lifecycle tests to real time.
type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}

// Provenance records whether a reversible value came from caller input or was
// generated while processing a response.
type Provenance string

const (
	ProvenanceInput     Provenance = "input"
	ProvenanceGenerated Provenance = "generated"
)

// StoreConfig bounds the in-memory scope and mapping ledgers.
type StoreConfig struct {
	TTL                time.Duration
	MaxScopes          int
	MaxEntriesPerScope int
	MaxTotalEntries    int
}

// MappingEntry is one reversible, process-memory-only mapping.
type MappingEntry struct {
	Entity     string
	Original   string
	Synthetic  string
	Provenance Provenance
	CreatedAt  time.Time
}

// ClearResult describes whether a clear finished immediately or is draining
// leases that were already acquired.
type ClearResult string

const (
	ClearCompleted ClearResult = "completed"
	ClearClosing   ClearResult = "closing"
)

// ScopeInfo is safe lifecycle metadata for one retained scope.
type ScopeInfo struct {
	ID         string
	Profile    Profile
	State      string
	Entries    int
	InFlight   int
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// ClearSummary reports the result of clearing every retained scope.
type ClearSummary struct {
	Completed int
	Closing   int
}

// StoreSnapshot is safe aggregate store metadata.
type StoreSnapshot struct {
	ScopesActive       int
	RequestsInFlight   int
	Entries            int
	MaxScopes          int
	MaxEntriesPerScope int
	MaxTotalEntries    int
	OldestScopeAge     time.Duration
}

var (
	errInvalidStoreConfig = errors.New("invalid privacy scope store configuration")
	errInvalidProfile     = errors.New("invalid privacy profile")
	errInvalidProvenance  = errors.New("invalid mapping provenance")
	errScopeClosed        = errors.New("privacy scope is closed")
	errScopeNotFound      = errors.New("privacy scope not found")
	errCapacityExceeded   = errors.New("privacy mapping capacity exceeded")
	errLeaseReleased      = errors.New("privacy scope lease is released")
	errNilCandidate       = errors.New("mapping candidate callback is nil")
	errCandidateExhausted = errors.New("mapping candidate attempts exhausted")
)

type lifecycleState uint8

const (
	lifecycleActive lifecycleState = iota + 1
	lifecycleClosing
)

func (s lifecycleState) String() string {
	if s == lifecycleClosing {
		return "closing"
	}
	return "active"
}

type mappingKey struct {
	entity string
	value  string
}

type scopeState struct {
	mu        sync.Mutex
	id        string
	profile   Profile
	forward   map[mappingKey]MappingEntry
	reverse   map[mappingKey]MappingEntry
	relations map[string]string
	inFlight  int
	state     lifecycleState
	createdAt time.Time
	lastUsed  time.Time
	expiresAt time.Time
}

type tombstone struct {
	id        string
	expiresAt time.Time
	sequence  uint64
}

// ScopeStore owns bounded process-memory-only ledgers grouped by scope.
type ScopeStore struct {
	mu            sync.Mutex
	config        StoreConfig
	clock         Clock
	scopes        map[string]*scopeState
	totalEntries  atomic.Int64
	tombstones    map[string]tombstone
	tombstoneRing []tombstone
	tombstoneHead int
	tombstoneLen  int
	nextSequence  uint64
}

// ScopeLease holds one in-flight reference to a scope.
type ScopeLease struct {
	mu       sync.RWMutex
	store    *ScopeStore
	scope    *scopeState
	released bool
}

// NewScopeStore constructs an empty bounded store.
func NewScopeStore(config StoreConfig, clock Clock) (*ScopeStore, error) {
	if clock == nil || config.TTL <= 0 || config.MaxScopes <= 0 ||
		config.MaxEntriesPerScope <= 0 || config.MaxTotalEntries <= 0 {
		return nil, errInvalidStoreConfig
	}

	tombstoneCapacity := 4 * config.MaxScopes
	if tombstoneCapacity < 128 {
		tombstoneCapacity = 128
	}

	return &ScopeStore{
		config:        config,
		clock:         clock,
		scopes:        make(map[string]*scopeState),
		tombstones:    make(map[string]tombstone, tombstoneCapacity),
		tombstoneRing: make([]tombstone, tombstoneCapacity),
	}, nil
}

// Acquire obtains an in-flight reference, creating the scope if necessary.
func (s *ScopeStore) Acquire(scopeID string, profile Profile) (*ScopeLease, error) {
	if !validProfile(profile) {
		return nil, errInvalidProfile
	}

	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneTombstonesLocked(now)
	if scope, ok := s.scopes[scopeID]; ok {
		scope.mu.Lock()
		defer scope.mu.Unlock()

		if scope.inFlight == 0 && !scope.expiresAt.After(now) {
			scope.state = lifecycleClosing
			s.wipeAndRemoveLocked(scope, now)
			return nil, errScopeClosed
		}
		if scope.state != lifecycleActive {
			return nil, errScopeClosed
		}
		if scope.profile != profile {
			return nil, fmt.Errorf("scope profile mismatch: %w", errInvalidProfile)
		}
		scope.inFlight++
		s.touchLocked(scope, now)
		return &ScopeLease{store: s, scope: scope}, nil
	}
	if _, closed := s.tombstones[scopeID]; closed {
		return nil, errScopeClosed
	}

	if len(s.scopes) >= s.config.MaxScopes {
		s.reapAvailableExpiredLocked(now)
		if len(s.scopes) >= s.config.MaxScopes {
			return nil, errCapacityExceeded
		}
	}

	scope := &scopeState{
		id:        scopeID,
		profile:   profile,
		forward:   make(map[mappingKey]MappingEntry),
		reverse:   make(map[mappingKey]MappingEntry),
		relations: make(map[string]string),
		inFlight:  1,
		state:     lifecycleActive,
		createdAt: now,
		lastUsed:  now,
		expiresAt: now.Add(s.config.TTL),
	}
	s.scopes[scopeID] = scope
	return &ScopeLease{store: s, scope: scope}, nil
}

// GetOrCreate returns the stable mapping for a tuple, retrying candidate
// collisions within the same entity.
func (l *ScopeLease) GetOrCreate(
	entity string,
	original string,
	provenance Provenance,
	candidate func(attempt uint32) (string, error),
) (MappingEntry, bool, error) {
	if provenance != ProvenanceInput && provenance != ProvenanceGenerated {
		return MappingEntry{}, false, errInvalidProvenance
	}
	if candidate == nil {
		return MappingEntry{}, false, errNilCandidate
	}
	if !l.lockOperation() {
		return MappingEntry{}, false, errLeaseReleased
	}
	defer l.mu.RUnlock()

	scope := l.scope
	scope.mu.Lock()

	key := mappingKey{entity: entity, value: original}
	if entry, ok := scope.forward[key]; ok {
		l.store.touchLocked(scope, l.store.clock.Now())
		scope.mu.Unlock()
		return entry, false, nil
	}
	if len(scope.forward)+len(scope.relations) >= l.store.config.MaxEntriesPerScope {
		scope.mu.Unlock()
		return MappingEntry{}, false, errCapacityExceeded
	}
	if !l.store.reserveEntry() {
		scope.mu.Unlock()
		l.store.reclaimAvailableExpired()
		scope.mu.Lock()

		if entry, ok := scope.forward[key]; ok {
			l.store.touchLocked(scope, l.store.clock.Now())
			scope.mu.Unlock()
			return entry, false, nil
		}
		if len(scope.forward)+len(scope.relations) >= l.store.config.MaxEntriesPerScope ||
			!l.store.reserveEntry() {
			scope.mu.Unlock()
			return MappingEntry{}, false, errCapacityExceeded
		}
	}
	defer scope.mu.Unlock()

	reserved := true
	defer func() {
		if reserved {
			l.store.totalEntries.Add(-1)
		}
	}()

	for attempt := uint32(0); ; attempt++ {
		synthetic, err := candidate(attempt)
		if err != nil {
			return MappingEntry{}, false, err
		}
		reverseKey := mappingKey{entity: entity, value: synthetic}
		if _, collision := scope.reverse[reverseKey]; collision {
			if attempt == ^uint32(0) {
				return MappingEntry{}, false, errCandidateExhausted
			}
			continue
		}

		now := l.store.clock.Now()
		entry := MappingEntry{
			Entity:     entity,
			Original:   original,
			Synthetic:  synthetic,
			Provenance: provenance,
			CreatedAt:  now,
		}
		scope.forward[key] = entry
		scope.reverse[reverseKey] = entry
		l.store.touchLocked(scope, now)
		reserved = false
		return entry, true, nil
	}
}

// GetOrCreateRelation reserves capacity for a stable relationship mapping.
func (l *ScopeLease) GetOrCreateRelation(
	key string,
	candidate func(attempt uint32) (string, error),
) (string, bool, error) {
	if candidate == nil {
		return "", false, errNilCandidate
	}
	if !l.lockOperation() {
		return "", false, errLeaseReleased
	}
	defer l.mu.RUnlock()

	scope := l.scope
	scope.mu.Lock()

	if value, ok := scope.relations[key]; ok {
		l.store.touchLocked(scope, l.store.clock.Now())
		scope.mu.Unlock()
		return value, false, nil
	}
	if len(scope.forward)+len(scope.relations) >= l.store.config.MaxEntriesPerScope {
		scope.mu.Unlock()
		return "", false, errCapacityExceeded
	}
	if !l.store.reserveEntry() {
		scope.mu.Unlock()
		l.store.reclaimAvailableExpired()
		scope.mu.Lock()

		if value, ok := scope.relations[key]; ok {
			l.store.touchLocked(scope, l.store.clock.Now())
			scope.mu.Unlock()
			return value, false, nil
		}
		if len(scope.forward)+len(scope.relations) >= l.store.config.MaxEntriesPerScope ||
			!l.store.reserveEntry() {
			scope.mu.Unlock()
			return "", false, errCapacityExceeded
		}
	}
	defer scope.mu.Unlock()

	reserved := true
	defer func() {
		if reserved {
			l.store.totalEntries.Add(-1)
		}
	}()

	for attempt := uint32(0); ; attempt++ {
		value, err := candidate(attempt)
		if err != nil {
			return "", false, err
		}
		collision := false
		for existingKey, existingValue := range scope.relations {
			if existingKey != key && existingValue == value {
				collision = true
				break
			}
		}
		if collision {
			if attempt == ^uint32(0) {
				return "", false, errCandidateExhausted
			}
			continue
		}

		scope.relations[key] = value
		l.store.touchLocked(scope, l.store.clock.Now())
		reserved = false
		return value, true, nil
	}
}

// ResolveSynthetic looks up a reversible entry by entity and synthetic value.
func (l *ScopeLease) ResolveSynthetic(entity, synthetic string) (MappingEntry, bool) {
	if !l.lockOperation() {
		return MappingEntry{}, false
	}
	defer l.mu.RUnlock()

	scope := l.scope
	scope.mu.Lock()
	defer scope.mu.Unlock()

	entry, ok := scope.reverse[mappingKey{entity: entity, value: synthetic}]
	if ok {
		l.store.touchLocked(scope, l.store.clock.Now())
	}
	return entry, ok
}

// Release drops an in-flight reference exactly once.
func (l *ScopeLease) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	l.released = true

	scope := l.scope
	scope.mu.Lock()
	if scope.inFlight > 0 {
		scope.inFlight--
	}
	finalize := scope.state == lifecycleClosing && scope.inFlight == 0
	scope.mu.Unlock()

	if finalize {
		l.store.finalizeClosed(scope)
	}
}

// List returns copied metadata for retained scopes.
func (s *ScopeStore) List() []ScopeInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]ScopeInfo, 0, len(s.scopes))
	for _, scope := range s.scopes {
		scope.mu.Lock()
		infos = append(infos, s.scopeInfoLocked(scope))
		scope.mu.Unlock()
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ID < infos[j].ID
	})
	return infos
}

// Inspect returns copied reversible entries for a retained scope.
func (s *ScopeStore) Inspect(scopeID string) ([]MappingEntry, error) {
	s.mu.Lock()
	scope, ok := s.scopes[scopeID]
	if !ok {
		s.mu.Unlock()
		return nil, errScopeNotFound
	}
	scope.mu.Lock()
	s.mu.Unlock()
	defer scope.mu.Unlock()

	entries := make([]MappingEntry, 0, len(scope.forward))
	for _, entry := range scope.forward {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Entity != entries[j].Entity {
			return entries[i].Entity < entries[j].Entity
		}
		return entries[i].Synthetic < entries[j].Synthetic
	})
	return entries, nil
}

// Clear closes one scope immediately and wipes it once leases have drained.
func (s *ScopeStore) Clear(scopeID string) (ClearResult, error) {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneTombstonesLocked(now)

	scope, ok := s.scopes[scopeID]
	if !ok {
		if _, closed := s.tombstones[scopeID]; closed {
			return ClearCompleted, nil
		}
		return "", errScopeNotFound
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()

	scope.state = lifecycleClosing
	if scope.inFlight > 0 {
		return ClearClosing, nil
	}
	s.wipeAndRemoveLocked(scope, now)
	return ClearCompleted, nil
}

// ClearAll closes all scopes and wipes those with no active lease.
func (s *ScopeStore) ClearAll() ClearSummary {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneTombstonesLocked(now)

	var summary ClearSummary
	for _, scope := range s.scopes {
		scope.mu.Lock()
		scope.state = lifecycleClosing
		if scope.inFlight > 0 {
			summary.Closing++
			scope.mu.Unlock()
			continue
		}
		s.wipeAndRemoveLocked(scope, now)
		summary.Completed++
		scope.mu.Unlock()
	}
	return summary
}

// ReapExpired wipes expired idle scopes and returns the number reclaimed.
func (s *ScopeStore) ReapExpired() int {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneTombstonesLocked(now)
	return s.reapExpiredLocked(now)
}

// Snapshot returns copied aggregate metadata.
func (s *ScopeStore) Snapshot() StoreSnapshot {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := StoreSnapshot{
		ScopesActive:       len(s.scopes),
		Entries:            int(s.totalEntries.Load()),
		MaxScopes:          s.config.MaxScopes,
		MaxEntriesPerScope: s.config.MaxEntriesPerScope,
		MaxTotalEntries:    s.config.MaxTotalEntries,
	}
	var oldest time.Time
	for _, scope := range s.scopes {
		scope.mu.Lock()
		snapshot.RequestsInFlight += scope.inFlight
		if oldest.IsZero() || scope.createdAt.Before(oldest) {
			oldest = scope.createdAt
		}
		scope.mu.Unlock()
	}
	if !oldest.IsZero() && now.After(oldest) {
		snapshot.OldestScopeAge = now.Sub(oldest)
	}
	return snapshot
}

func validProfile(profile Profile) bool {
	return profile == ProfileStandard || profile == ProfileStrict
}

func (l *ScopeLease) lockOperation() bool {
	l.mu.RLock()
	if l.released {
		l.mu.RUnlock()
		return false
	}
	return true
}

func (s *ScopeStore) reserveEntry() bool {
	limit := int64(s.config.MaxTotalEntries)
	for {
		current := s.totalEntries.Load()
		if current >= limit {
			return false
		}
		if s.totalEntries.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (s *ScopeStore) touchLocked(scope *scopeState, now time.Time) {
	scope.lastUsed = now
	scope.expiresAt = now.Add(s.config.TTL)
}

func (s *ScopeStore) scopeInfoLocked(scope *scopeState) ScopeInfo {
	return ScopeInfo{
		ID:         scope.id,
		Profile:    scope.profile,
		State:      scope.state.String(),
		Entries:    len(scope.forward) + len(scope.relations),
		InFlight:   scope.inFlight,
		CreatedAt:  scope.createdAt,
		LastUsedAt: scope.lastUsed,
		ExpiresAt:  scope.expiresAt,
	}
}

func (s *ScopeStore) reapExpiredLocked(now time.Time) int {
	reaped := 0
	for _, scope := range s.scopes {
		scope.mu.Lock()
		if scope.inFlight == 0 && (scope.state == lifecycleClosing || !scope.expiresAt.After(now)) {
			scope.state = lifecycleClosing
			s.wipeAndRemoveLocked(scope, now)
			reaped++
		}
		scope.mu.Unlock()
	}
	return reaped
}

func (s *ScopeStore) reapAvailableExpiredLocked(now time.Time) int {
	reaped := 0
	for _, scope := range s.scopes {
		if !scope.mu.TryLock() {
			continue
		}
		if scope.inFlight == 0 && (scope.state == lifecycleClosing || !scope.expiresAt.After(now)) {
			scope.state = lifecycleClosing
			s.wipeAndRemoveLocked(scope, now)
			reaped++
		}
		scope.mu.Unlock()
	}
	return reaped
}

func (s *ScopeStore) reclaimAvailableExpired() {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneTombstonesLocked(now)
	s.reapAvailableExpiredLocked(now)
}

// wipeAndRemoveLocked requires the store lock followed by the scope lock.
func (s *ScopeStore) wipeAndRemoveLocked(scope *scopeState, now time.Time) {
	if current, ok := s.scopes[scope.id]; !ok || current != scope {
		return
	}

	entries := len(scope.forward) + len(scope.relations)
	clear(scope.forward)
	clear(scope.reverse)
	clear(scope.relations)
	scope.forward = nil
	scope.reverse = nil
	scope.relations = nil
	delete(s.scopes, scope.id)
	if entries != 0 {
		s.totalEntries.Add(-int64(entries))
	}
	s.addTombstoneLocked(scope.id, now.Add(s.config.TTL))
}

func (s *ScopeStore) finalizeClosed(scope *scopeState) {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.scopes[scope.id]
	if !ok || current != scope {
		return
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.state == lifecycleClosing && scope.inFlight == 0 {
		s.wipeAndRemoveLocked(scope, now)
	}
}

func (s *ScopeStore) addTombstoneLocked(scopeID string, expiresAt time.Time) {
	s.nextSequence++
	marker := tombstone{id: scopeID, expiresAt: expiresAt, sequence: s.nextSequence}

	if s.tombstoneLen == len(s.tombstoneRing) {
		oldest := s.tombstoneRing[s.tombstoneHead]
		if current, ok := s.tombstones[oldest.id]; ok && current.sequence == oldest.sequence {
			delete(s.tombstones, oldest.id)
		}
		s.tombstoneRing[s.tombstoneHead] = marker
		s.tombstoneHead = (s.tombstoneHead + 1) % len(s.tombstoneRing)
	} else {
		index := (s.tombstoneHead + s.tombstoneLen) % len(s.tombstoneRing)
		s.tombstoneRing[index] = marker
		s.tombstoneLen++
	}
	s.tombstones[scopeID] = marker
}

func (s *ScopeStore) pruneTombstonesLocked(now time.Time) {
	for s.tombstoneLen > 0 {
		oldest := s.tombstoneRing[s.tombstoneHead]
		current, ok := s.tombstones[oldest.id]
		if ok && current.sequence == oldest.sequence && current.expiresAt.After(now) {
			return
		}
		if ok && current.sequence == oldest.sequence {
			delete(s.tombstones, oldest.id)
		}
		s.tombstoneRing[s.tombstoneHead] = tombstone{}
		s.tombstoneHead = (s.tombstoneHead + 1) % len(s.tombstoneRing)
		s.tombstoneLen--
	}
}
