package privacy

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

const (
	privacyProfileHeader     = "X-GW-Privacy-Profile"
	privacyScopeHeader       = "X-GW-Privacy-Scope"
	privacyReceiptHeader     = "X-GW-Privacy-Receipt"
	maxProfileLength         = 16
	maxScopeLength           = 128
	maxSurfaceLength         = 32
	maxWorkloadLength        = 64
	maxAuthorizedTokens      = 4096
	maxAuthorizedTokenLength = 512
)

// RequestMetadata is bounded request information safe for telemetry.
type RequestMetadata struct {
	RequestedProfile string
	ScopeID          string
	Surface          string
	Workload         string
}

// RequestState carries request-local privacy state and never outlives the
// request context.
type RequestState struct {
	mu sync.Mutex

	metadata               RequestMetadata
	invalidScope           bool
	profile                Profile
	transformed            int
	restored               int
	blocked                int
	standardInboundService uint64
	lease                  *ScopeLease
	authorizedTokens       map[string]struct{}
	residualSafeTokens     map[string]struct{}
	receipt                string
}

// NewRequestState returns bounded request-local state.
func NewRequestState(meta RequestMetadata) *RequestState {
	invalidScope := meta.ScopeID != "" && !scopeIDPattern.MatchString(meta.ScopeID)
	meta.RequestedProfile = truncateRunes(meta.RequestedProfile, maxProfileLength)
	meta.ScopeID = truncateRunes(meta.ScopeID, maxScopeLength)
	meta.Surface = truncateRunes(meta.Surface, maxSurfaceLength)
	meta.Workload = truncateRunes(meta.Workload, maxWorkloadLength)
	return &RequestState{metadata: meta, invalidScope: invalidScope}
}

// Metadata returns a value copy of the bounded metadata.
func (s *RequestState) Metadata() RequestMetadata {
	if s == nil {
		return RequestMetadata{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata
}

func (s *RequestState) addTransformed(delta int) {
	s.mu.Lock()
	s.transformed = addBounded(s.transformed, delta)
	s.mu.Unlock()
}

func (s *RequestState) addRestored(delta int) {
	s.mu.Lock()
	s.restored = addBounded(s.restored, delta)
	s.mu.Unlock()
}

func (s *RequestState) addBlocked(delta int) {
	s.mu.Lock()
	s.blocked = addBounded(s.blocked, delta)
	s.mu.Unlock()
}

func (s *RequestState) counts() (transformed, restored, blocked int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transformed, s.restored, s.blocked
}

func (s *RequestState) setProfile(profile Profile) {
	s.mu.Lock()
	s.profile = profile
	s.mu.Unlock()
}

func (s *RequestState) effectiveProfile() Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

func (s *RequestState) setScopeID(scopeID string) {
	s.mu.Lock()
	s.metadata.ScopeID = scopeID
	s.mu.Unlock()
}

func (s *RequestState) scopeInvalid() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalidScope
}

func (s *RequestState) setLease(lease *ScopeLease) bool {
	if s == nil || lease == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lease != nil {
		return false
	}
	s.lease = lease
	return true
}

func (s *RequestState) scopeLease() *ScopeLease {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lease
}

func (s *RequestState) releaseLease() {
	if s == nil {
		return
	}
	s.mu.Lock()
	lease := s.lease
	s.lease = nil
	s.mu.Unlock()
	if lease != nil {
		lease.Release()
	}
}

func (s *RequestState) markStandardInboundPass(serviceID uint64) {
	s.mu.Lock()
	s.standardInboundService = serviceID
	s.mu.Unlock()
}

func (s *RequestState) standardInboundPassed(serviceID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return serviceID != 0 && s.standardInboundService == serviceID
}

func (s *RequestState) authorizeToken(token string) bool {
	if len(token) > maxAuthorizedTokenLength {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authorizedTokens == nil {
		s.authorizedTokens = make(map[string]struct{})
	}
	if _, ok := s.authorizedTokens[token]; ok {
		return true
	}
	if len(s.authorizedTokens) >= maxAuthorizedTokens {
		return false
	}
	s.authorizedTokens[token] = struct{}{}
	return true
}

func (s *RequestState) tokenAuthorized(token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.authorizedTokens[token]
	return ok
}

func (s *RequestState) authorizeResidualSafeToken(token string) bool {
	if !s.authorizeToken(token) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.residualSafeTokens == nil {
		s.residualSafeTokens = make(map[string]struct{})
	}
	s.residualSafeTokens[token] = struct{}{}
	return true
}

func (s *RequestState) residualFindingSafe(value string, start, end int) bool {
	if s == nil || start < 0 || end > len(value) || start >= end {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token := range s.residualSafeTokens {
		for offset := 0; offset <= len(value)-len(token); {
			index := strings.Index(value[offset:], token)
			if index < 0 {
				break
			}
			tokenStart := offset + index
			tokenEnd := tokenStart + len(token)
			if start >= tokenStart && end <= tokenEnd {
				return true
			}
			offset = tokenEnd
		}
	}
	return false
}

func (s *RequestState) authorizedTokenAt(value string, offset int) bool {
	if s == nil || offset < 0 || offset >= len(value) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token := range s.authorizedTokens {
		if strings.HasPrefix(value[offset:], token) {
			return true
		}
	}
	return false
}

func (s *RequestState) setReceipt(receipt Receipt) error {
	encoded, err := encodeReceipt(receipt)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.receipt = encoded
	s.mu.Unlock()
	return nil
}

func (s *RequestState) receiptValue() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receipt
}

type requestStateContextKey struct{}

// WithRequestState attaches state to a request context.
func WithRequestState(ctx context.Context, state *RequestState) context.Context {
	return context.WithValue(ctx, requestStateContextKey{}, state)
}

// StateFromContext retrieves non-nil request state.
func StateFromContext(ctx context.Context) (*RequestState, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(requestStateContextKey{}).(*RequestState)
	return state, ok && state != nil
}

// StampHTTPContext extracts caller privacy metadata and stamps the trusted
// adapter-supplied surface.
func StampHTTPContext(ctx context.Context, headers http.Header, surface string) (context.Context, *RequestState) {
	workload := headers.Get("X-GW-Skill")
	if workload == "" {
		workload = headers.Get("X-Flow-Name")
	}
	state := NewRequestState(RequestMetadata{
		RequestedProfile: headers.Get(privacyProfileHeader),
		ScopeID:          headers.Get(privacyScopeHeader),
		Surface:          surface,
		Workload:         workload,
	})
	return WithRequestState(ctx, state), state
}

// SetReceiptHeader writes the already-bounded encoded receipt, if present.
func SetReceiptHeader(w http.ResponseWriter, ctx context.Context) bool {
	state, ok := StateFromContext(ctx)
	if !ok {
		return false
	}
	receipt := state.receiptValue()
	if receipt == "" {
		return false
	}
	w.Header().Set(privacyReceiptHeader, receipt)
	return true
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func addBounded(current, delta int) int {
	if delta <= 0 {
		return current
	}
	maximum := int(^uint(0) >> 1)
	if current > maximum-delta {
		return maximum
	}
	return current + delta
}
