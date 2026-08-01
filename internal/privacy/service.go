package privacy

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"otto-gateway/internal/canonical"
)

var (
	wrappedEncryptedTokenRE   = regexp.MustCompile(`\[PII:([A-Za-z0-9_]+):([A-Za-z0-9_-]+)\]`)
	bareEncryptedPayloadRE    = regexp.MustCompile(`[A-Za-z0-9_-]{38,}`)
	compatibilityDepthWarning sync.Once
	serviceSequence           atomic.Uint64
	scopeIDPattern            = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	secretTokenPattern        = regexp.MustCompile(`^\[SECRET:[A-Z0-9_]+_[A-F0-9]{12}\]$`)
	piiTokenPattern           = regexp.MustCompile(`^\[PII:[A-Za-z0-9_]+:[A-Za-z0-9_-]+\]$`)
	replacementTokenPattern   = regexp.MustCompile(`^\[[A-Z][A-Z0-9_]*_[1-9][0-9]*\]$`)
	hashTokenPattern          = regexp.MustCompile(`^\[[A-Z][A-Z0-9_]*:h-[A-Fa-f0-9]{8}\]$`)
	reservedTokenLikePrefix   = regexp.MustCompile(`(?i)^[A-Za-z][A-Za-z0-9_]*(?:_[0-9]|:h-)`)
)

var reservedPrivacyEntities = []string{
	"Email", "IPv4", "IPv6", "SSN", "CreditCard", "USPhone", "SIP_URI",
	"IMEI", "IMSI", "MSISDN", "MAC_ADDRESS", "COORDINATES", "SITE",
	"USAddress", "USState", "USZIP", "PERSON", "LOCATION",
}

const maxCompatibilityTraversalDepth = 64

const secretHMACDomain = "otto-gateway/privacy/secret-label/v1"

// Observers are bounded event callbacks owned by the privacy service.
type Observers struct {
	Request           func(profile Profile, surface, workload, result string)
	Transformation    func(profile Profile, entity string, action Action)
	Restoration       func(profile Profile, entity, result string)
	Block             func(profile Profile, stage, reason string)
	Residual          func(profile Profile, stage, entity string)
	Receipt           func(profile Profile, result string)
	Duration          func(profile Profile, stage string, elapsed time.Duration)
	ScopeEvent        func(event string)
	CapacityRejection func(resource string)
	MappingOperation  func(operation, result string)
	InternalError     func(stage, reason string)
}

// Config is the immutable privacy-service configuration.
type Config struct {
	DefaultProfile     Profile
	RequestProfiles    []Profile
	AliasKey           []byte
	SecretAction       Action
	TechnicalAction    Action
	ScopeTTL           time.Duration
	MaxScopes          int
	MaxEntriesPerScope int
	MaxTotalEntries    int
	PIIEnabled         bool
	PIIMode            Action
	PIIHashKey         []byte
	PIIEncryptKey      []byte
	PIIEntityActions   map[string]Action
	Recognizers        []string
	NEREnabled         bool
	Classifier         Classifier
	SecretClassifier   *SecretClassifier
	TriageEnabled      bool
	Clock              Clock
	Observers          Observers
}

// SafeSnapshot is the value-free runtime posture exposed to operators.
type SafeSnapshot struct {
	DefaultProfile                          Profile
	RequestProfiles                         []Profile
	StrictAvailable                         bool
	SecretAction, TechnicalAction, PIIMode  Action
	PIIEnabled, NEREnabled                  bool
	AliasKeyPresent, TriageEnabled          bool
	Recognizers                             []string
	EntityActions                           map[string]Action
	ScopeTTL                                time.Duration
	MaxScopes, MaxEntriesPerScope           int
	MaxTotalEntries                         int
	ScopesActive, RequestsInFlight, Entries int
	OldestScopeAge                          time.Duration
	RequestsProtected, RequestsBlocked      uint64
	LastErrorCode                           string
}

// RedactionCount is one typed compatibility summary row.
type RedactionCount struct {
	Entity string `json:"entity"`
	Count  int    `json:"count"`
}

// Summary is a request-local, race-safe standard transformation tally.
type Summary struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewSummary() *Summary {
	return &Summary{counts: make(map[string]int)}
}

func (s *Summary) Add(entity string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.counts == nil {
		s.counts = make(map[string]int)
	}
	s.counts[entity]++
	s.mu.Unlock()
}

func (s *Summary) Counts() map[string]int {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.counts))
	for entity, count := range s.counts {
		out[entity] = count
	}
	return out
}

func (s *Summary) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	payload, err := json.Marshal(s.Counts())
	if err != nil {
		return nil, errorsSafe("privacy summary encoding failed")
	}
	return payload, nil
}

type summaryContextKey struct{}

func WithSummary(ctx context.Context, summary *Summary) context.Context {
	return context.WithValue(ctx, summaryContextKey{}, summary)
}

func SummaryFromContext(ctx context.Context) (*Summary, bool) {
	if ctx == nil {
		return nil, false
	}
	summary, ok := ctx.Value(summaryContextKey{}).(*Summary)
	return summary, ok
}

type safeStringError string

func (e safeStringError) Error() string { return string(e) }

func errorsSafe(message string) error { return safeStringError(message) }

// Service owns standard privacy transformation authority.
type Service struct {
	config      Config
	lifecycleID uint64
	store       *ScopeStore
	mapper      technicalMapping

	requestsProtected atomic.Uint64
	requestsBlocked   atomic.Uint64
	lastErrorMu       sync.Mutex
	lastErrorCode     string

	closed     atomic.Bool
	closeOnce  sync.Once
	reaperStop chan struct{}
	reaperDone chan struct{}
}

type technicalMapping interface {
	Map(*ScopeLease, string, string, Provenance) (string, error)
}

// NewService copies all mutable configuration into an immutable service.
func NewService(config Config) (*Service, error) {
	if config.DefaultProfile == "" {
		config.DefaultProfile = ProfileStandard
	}
	if len(config.RequestProfiles) == 0 {
		config.RequestProfiles = []Profile{ProfileStandard}
	}
	config.RequestProfiles = append([]Profile(nil), config.RequestProfiles...)
	config.AliasKey = append([]byte(nil), config.AliasKey...)
	config.PIIHashKey = append([]byte(nil), config.PIIHashKey...)
	config.PIIEncryptKey = append([]byte(nil), config.PIIEncryptKey...)
	config.Recognizers = append([]string(nil), config.Recognizers...)
	config.PIIEntityActions = cloneActions(config.PIIEntityActions)
	service := &Service{config: config, lifecycleID: serviceSequence.Add(1)}
	if containsProfile(config.RequestProfiles, ProfileStrict) {
		clock := config.Clock
		if clock == nil {
			clock = wallClock{}
		}
		store, err := NewScopeStore(StoreConfig{
			TTL:                config.ScopeTTL,
			MaxScopes:          config.MaxScopes,
			MaxEntriesPerScope: config.MaxEntriesPerScope,
			MaxTotalEntries:    config.MaxTotalEntries,
		}, clock)
		if err != nil {
			return nil, err
		}
		mapper, err := NewTechnicalMapper(config.AliasKey)
		if err != nil {
			return nil, err
		}
		service.store = store
		service.mapper = mapper
		service.startReaper(clock, config.ScopeTTL)
	}
	return service, nil
}

func (s *Service) startReaper(clock Clock, ttl time.Duration) {
	s.reaperStop = make(chan struct{})
	s.reaperDone = make(chan struct{})
	interval := ttl / 2
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := clock.NewTicker(interval)
	go func() {
		defer close(s.reaperDone)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C():
				s.store.ReapExpired()
			case <-s.reaperStop:
				return
			}
		}
	}()
}

func cloneActions(actions map[string]Action) map[string]Action {
	if actions == nil {
		return nil
	}
	out := make(map[string]Action, len(actions))
	for entity, action := range actions {
		out[entity] = action
	}
	return out
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) NewTicker(interval time.Duration) Ticker {
	return wallTicker{Ticker: time.NewTicker(interval)}
}

type wallTicker struct{ *time.Ticker }

func (t wallTicker) C() <-chan time.Time { return t.Ticker.C }

// Before applies the resolved privacy profile before worker dispatch.
func (s *Service) Before(ctx context.Context, req *canonical.ChatRequest) (resp *canonical.ChatResponse, err error) {
	defer func() {
		if recover() != nil {
			resp = nil
			err = s.recoverInboundPanic(ctx)
		}
	}()
	if s == nil || !s.config.PIIEnabled || req == nil {
		return nil, nil
	}
	state, _ := StateFromContext(ctx)
	profile, err := s.resolveProfile(state)
	if err != nil {
		return nil, err
	}
	if profile == ProfileStrict {
		return nil, s.beforeStrict(ctx, state, req)
	}

	if state != nil {
		state.setProfile(ProfileStandard)
	}
	if s.encryptActive() && req.Stream {
		req.Stream = false
	}

	summary, _ := SummaryFromContext(ctx)
	if summary == nil {
		summary = NewSummary()
	}
	counters := make(map[string]int)
	next := make(map[string]int)
	transform := func(key, value string) string {
		return s.transformStandardValue(state, summary, counters, next, key, value)
	}
	transformStandardRequest(req, transform)
	if err := s.setStandardReceipt(state, "input"); err != nil {
		return nil, err
	}
	s.requestsProtected.Add(1)
	if observe := s.config.Observers.Request; observe != nil {
		meta := RequestMetadata{}
		if state != nil {
			meta = state.Metadata()
		}
		observe(ProfileStandard, meta.Surface, meta.Workload, "pass")
	}
	if state != nil {
		state.markStandardInboundPass(s.lifecycleID)
	}
	return nil, nil
}

func (s *Service) recoverInboundPanic(ctx context.Context) error {
	state, _ := StateFromContext(ctx)
	if state != nil {
		state.releaseLease()
		profile := state.effectiveProfile()
		if profile == "" && s != nil {
			profile = s.config.DefaultProfile
		}
		transformed, restored, blocked := state.counts()
		_ = state.setReceipt(Receipt{
			Version: 1, Profile: profile, Scope: state.Metadata().ScopeID,
			Coverage: "input", Result: "error",
			Transformed: transformed, Restored: restored, Blocked: blocked,
		})
	}
	if s != nil {
		s.lastErrorMu.Lock()
		s.lastErrorCode = CodeInternalError
		s.lastErrorMu.Unlock()
	}
	return &Error{Code: CodeInternalError, Stage: "input"}
}

func (s *Service) resolveProfile(state *RequestState) (Profile, error) {
	requested := ""
	if state != nil {
		requested = state.Metadata().RequestedProfile
	}
	if requested != "" {
		profile := Profile(requested)
		if !validProfile(profile) || !containsProfile(s.config.RequestProfiles, profile) {
			return "", &Error{Code: CodeProfileUnavailable, Stage: "profile"}
		}
		if s.config.DefaultProfile == ProfileStrict || profile == ProfileStrict {
			return ProfileStrict, nil
		}
	}
	return s.config.DefaultProfile, nil
}

func (s *Service) beforeStrict(ctx context.Context, state *RequestState, req *canonical.ChatRequest) (err error) {
	if s.closed.Load() {
		return &Error{Code: CodeScopeClosed, Stage: "scope"}
	}
	if state == nil || s.store == nil {
		return &Error{Code: CodeRequestInvalid, Stage: "scope"}
	}
	if state.scopeInvalid() {
		return &Error{Code: CodeRequestInvalid, Stage: "scope"}
	}
	meta := state.Metadata()
	scopeID := meta.ScopeID
	if scopeID == "" {
		scopeID, err = generateScopeID()
		if err != nil {
			return &Error{Code: CodeInternalError, Stage: "scope"}
		}
		state.setScopeID(scopeID)
	}
	lease, acquireErr := s.store.Acquire(scopeID, ProfileStrict)
	if acquireErr != nil {
		switch {
		case errors.Is(acquireErr, errScopeClosed):
			return &Error{Code: CodeScopeClosed, Stage: "scope"}
		case errors.Is(acquireErr, errCapacityExceeded):
			return &Error{Code: CodeCapacityExceeded, Stage: "scope"}
		default:
			return &Error{Code: CodeInternalError, Stage: "scope"}
		}
	}
	if !state.setLease(lease) {
		lease.Release()
		return &Error{Code: CodeInternalError, Stage: "scope"}
	}
	defer func() {
		if err != nil {
			state.releaseLease()
		}
	}()
	state.setProfile(ProfileStrict)
	req.Stream = false
	return s.transformInbound(ctx, state, req)
}

func generateScopeID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "req-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)), nil
}

func (s *Service) transformInbound(_ context.Context, state *RequestState, req *canonical.ChatRequest) error {
	if state == nil || req == nil {
		return &Error{Code: CodeInternalError, Stage: "transform"}
	}
	lease := state.scopeLease()
	if lease == nil {
		return &Error{Code: CodeInternalError, Stage: "transform"}
	}

	counters := make(map[string]int)
	next := make(map[string]int)
	stringOrdinal := 0
	transform := func(key, value string) (string, error) {
		ordinal := stringOrdinal
		stringOrdinal++
		if value == "" {
			return value, nil
		}
		findings := make([]Finding, 0, 4)
		if s.config.SecretClassifier != nil {
			findings = append(findings, s.config.SecretClassifier.Classify(key, value)...)
		}
		if s.config.Classifier != nil {
			findings = append(findings, s.config.Classifier.Classify(key, value)...)
		}
		accepted := Arbitrate(findings)
		if len(accepted) == 0 {
			return value, nil
		}

		type rewrite struct {
			replacement string
			kind        authorizedOccurrenceKind
		}
		rewrites := make([]rewrite, len(accepted))
		transformed := value
		for index := len(accepted) - 1; index >= 0; index-- {
			finding := accepted[index]
			if finding.Start < 0 || finding.End > len(value) || finding.Start >= finding.End {
				return "", &Error{Code: CodeInternalError, Stage: "classify"}
			}
			original := value[finding.Start:finding.End]
			replacement, action, kind, err := s.strictReplacement(state, lease, finding, original, counters, next)
			if err != nil {
				return "", err
			}
			rewrites[index] = rewrite{replacement: replacement, kind: kind}
			transformed = transformed[:finding.Start] + replacement + transformed[finding.End:]
			state.addTransformed(1)
			if observe := s.config.Observers.Transformation; observe != nil {
				observe(ProfileStrict, finding.Entity, action)
			}
		}
		shift := 0
		for index, finding := range accepted {
			rewrite := rewrites[index]
			finalStart := finding.Start + shift
			finalEnd := finalStart + len(rewrite.replacement)
			if rewrite.kind != 0 && !state.authorizeOccurrence(ordinal, finalStart, finalEnd, rewrite.kind) {
				return "", &Error{Code: CodeCapacityExceeded, Stage: "token"}
			}
			shift += len(rewrite.replacement) - (finding.End - finding.Start)
		}
		return transformed, nil
	}
	if err := TransformRequestStrings(req, transform); err != nil {
		var privacyErr *Error
		if errors.As(err, &privacyErr) {
			return privacyErr
		}
		return &Error{Code: CodeInternalError, Stage: "transform", Cause: err}
	}
	if err := s.verifyInboundResidual(state, req); err != nil {
		return err
	}
	s.requestsProtected.Add(1)
	if observe := s.config.Observers.Request; observe != nil {
		meta := state.Metadata()
		observe(ProfileStrict, meta.Surface, meta.Workload, "pass")
	}
	return nil
}

func (s *Service) strictReplacement(
	state *RequestState,
	lease *ScopeLease,
	finding Finding,
	original string,
	counters map[string]int,
	next map[string]int,
) (string, Action, authorizedOccurrenceKind, error) {
	action := s.strictAction(finding)
	switch action {
	case ActionPseudonymize:
		mapped, err := s.mapper.Map(lease, finding.Entity, original, ProvenanceInput)
		if err != nil {
			if errors.Is(err, errCapacityExceeded) {
				return "", action, 0, &Error{Code: CodeCapacityExceeded, Stage: "mapping"}
			}
			return "", action, 0, &Error{Code: CodeInternalError, Stage: "mapping"}
		}
		return mapped, action, 0, nil
	case ActionDrop:
		return "", action, 0, nil
	case ActionEncrypt:
		token, err := EncryptValue(s.config.PIIEncryptKey, finding.Entity, original)
		if err != nil {
			return "", action, 0, &Error{Code: CodeInternalError, Stage: "encrypt"}
		}
		_, payload, ok := ParseEncryptedToken(token)
		if !ok || !state.authorizeToken(token) || !state.authorizeToken(payload) {
			return "", action, 0, &Error{Code: CodeCapacityExceeded, Stage: "token"}
		}
		return token, action, authorizedOccurrenceOpaque, nil
	case ActionReplace:
		if finding.Category == CategorySecret {
			scope := secretHMACDomain + "\x00" + state.Metadata().ScopeID
			label := OneWaySecretLabel(s.config.AliasKey, scope, finding.Entity, original)
			if !state.authorizeToken(label) {
				return "", action, 0, &Error{Code: CodeCapacityExceeded, Stage: "token"}
			}
			return label, action, authorizedOccurrenceOpaque, nil
		}
		identity := finding.Entity + "|" + CanonicalForm(original)
		counter, ok := counters[identity]
		if !ok {
			next[finding.Entity]++
			counter = next[finding.Entity]
			counters[identity] = counter
		}
		replacement := ApplyAction(action, finding.Entity, original, counter, s.config.PIIHashKey, s.config.PIIEncryptKey)
		if !state.authorizeToken(replacement) {
			return "", action, 0, &Error{Code: CodeCapacityExceeded, Stage: "token"}
		}
		return replacement, action, authorizedOccurrenceGeneral, nil
	case ActionMask, ActionHash:
		replacement := ApplyAction(action, finding.Entity, original, 0, s.config.PIIHashKey, s.config.PIIEncryptKey)
		kind := authorizedOccurrenceKind(0)
		if action == ActionHash {
			if !state.authorizeToken(replacement) {
				return "", action, 0, &Error{Code: CodeCapacityExceeded, Stage: "token"}
			}
			kind = authorizedOccurrenceGeneral
		}
		return replacement, action, kind, nil
	default:
		return "", action, 0, &Error{Code: CodeInternalError, Stage: "action"}
	}
}

func (s *Service) verifyInboundResidual(state *RequestState, req *canonical.ChatRequest) error {
	lease := state.scopeLease()
	if lease == nil {
		return &Error{Code: CodeInternalError, Stage: "verify"}
	}
	ordinal := 0
	return VisitRequestStrings(req, func(key, value string) error {
		currentOrdinal := ordinal
		ordinal++
		for _, token := range s.parseReservedTokens(value) {
			if !token.valid || !state.occurrenceAt(currentOrdinal, token.start, token.end, token.kind) {
				return s.blockInbound(state, "token")
			}
		}

		configuredFindings := make([]Finding, 0, 4)
		if s.config.Classifier != nil {
			configuredFindings = s.config.Classifier.Classify(key, value)
		}
		for _, finding := range configuredFindings {
			if !validFindingRange(finding, value) {
				return &Error{Code: CodeInternalError, Stage: "verify"}
			}
			if _, ok := state.occurrenceContaining(currentOrdinal, finding.Start, finding.End); ok {
				s.observeResidual(finding.Entity)
				return s.blockInbound(state, "residual")
			}
		}

		secretFindings := make([]Finding, 0, 4)
		if s.config.SecretClassifier != nil {
			secretFindings = s.config.SecretClassifier.Classify(key, value)
		}
		findings := make([]Finding, 0, len(secretFindings)+len(configuredFindings))
		findings = append(findings, secretFindings...)
		findings = append(findings, configuredFindings...)
		for _, finding := range Arbitrate(findings) {
			if !validFindingRange(finding, value) {
				return &Error{Code: CodeInternalError, Stage: "verify"}
			}
			matched := value[finding.Start:finding.End]
			if occurrence, ok := state.occurrenceContaining(currentOrdinal, finding.Start, finding.End); ok {
				if occurrence.kind == authorizedOccurrenceOpaque &&
					findingIn(secretFindings, finding) &&
					s.expectedOpaqueArtifact(key, value, occurrence, finding) {
					continue
				}
				s.observeResidual(finding.Entity)
				return s.blockInbound(state, "residual")
			}
			if finding.Category == CategoryTechnical {
				entry, ok := lease.ResolveSynthetic(finding.Entity, matched)
				if ok && entry.Synthetic == matched && entry.Provenance == ProvenanceInput {
					continue
				}
			}
			s.observeResidual(finding.Entity)
			return s.blockInbound(state, "residual")
		}
		return nil
	})
}

type reservedToken struct {
	start, end int
	kind       authorizedOccurrenceKind
	valid      bool
}

func (s *Service) parseReservedTokens(value string) []reservedToken {
	var tokens []reservedToken
	for offset := 0; offset < len(value); {
		relativeStart := strings.IndexByte(value[offset:], '[')
		if relativeStart < 0 {
			break
		}
		start := offset + relativeStart
		tail := value[start+1:]
		relativeClose := strings.IndexByte(tail, ']')
		end := len(value)
		if relativeClose >= 0 {
			end = start + relativeClose + 2
		}
		raw := value[start:end]
		reserved := hasReservedNamespacePrefix(tail, "SECRET") || hasReservedNamespacePrefix(tail, "PII") ||
			reservedTokenLikePrefix.MatchString(tail) || s.hasReservedEntityPrefix(tail)
		if !reserved {
			offset = start + 1
			continue
		}

		kind := authorizedOccurrenceGeneral
		valid := relativeClose >= 0 && (replacementTokenPattern.MatchString(raw) || hashTokenPattern.MatchString(raw))
		if secretTokenPattern.MatchString(raw) || piiTokenPattern.MatchString(raw) {
			kind = authorizedOccurrenceOpaque
			valid = relativeClose >= 0
		}
		if valid && end < len(value) && (value[end] == '_' || value[end] == ':') {
			valid = false
		}
		tokens = append(tokens, reservedToken{start: start, end: end, kind: kind, valid: valid})
		if relativeClose < 0 {
			break
		}
		offset = end
	}
	return tokens
}

func (s *Service) hasReservedEntityPrefix(tail string) bool {
	matches := func(entity string) bool {
		if len(tail) < len(entity) || !strings.EqualFold(tail[:len(entity)], entity) {
			return false
		}
		if len(tail) == len(entity) {
			return true
		}
		return !isTokenIdentifierByte(tail[len(entity)])
	}
	for _, entity := range reservedPrivacyEntities {
		if matches(entity) {
			return true
		}
	}
	for _, entity := range s.config.Recognizers {
		if matches(entity) {
			return true
		}
	}
	for entity := range s.config.PIIEntityActions {
		if matches(entity) {
			return true
		}
	}
	return false
}

func hasReservedNamespacePrefix(value, namespace string) bool {
	if len(value) < len(namespace) || !strings.EqualFold(value[:len(namespace)], namespace) {
		return false
	}
	if len(value) == len(namespace) {
		return true
	}
	return !isTokenIdentifierByte(value[len(namespace)])
}

func isTokenIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_'
}

func validFindingRange(finding Finding, value string) bool {
	return finding.Start >= 0 && finding.End <= len(value) && finding.Start < finding.End
}

func findingIn(findings []Finding, want Finding) bool {
	for _, finding := range findings {
		if finding == want {
			return true
		}
	}
	return false
}

func (s *Service) expectedOpaqueArtifact(
	key, value string,
	occurrence authorizedOccurrence,
	finding Finding,
) bool {
	if s.config.SecretClassifier == nil || occurrence.start < 0 || occurrence.end > len(value) {
		return false
	}
	token := value[occurrence.start:occurrence.end]
	relative := finding
	relative.Start -= occurrence.start
	relative.End -= occurrence.start
	for _, artifact := range s.config.SecretClassifier.Classify(key, token) {
		if sameArtifactIdentity(artifact, relative) {
			return true
		}
	}
	return false
}

func sameArtifactIdentity(left, right Finding) bool {
	return left.Entity == right.Entity && left.Category == right.Category && left.Kind == right.Kind &&
		left.Start == right.Start && left.End == right.End
}

func (s *Service) observeResidual(entity string) {
	if observe := s.config.Observers.Residual; observe != nil {
		observe(ProfileStrict, "input", entity)
	}
}

func (s *Service) blockInbound(state *RequestState, reason string) error {
	state.addBlocked(1)
	s.requestsBlocked.Add(1)
	if observe := s.config.Observers.Block; observe != nil {
		observe(ProfileStrict, "input", reason)
	}
	transformed, restored, blocked := state.counts()
	if err := state.setReceipt(Receipt{
		Version: 1, Profile: ProfileStrict, Scope: state.Metadata().ScopeID,
		Coverage: "input", Result: "block", Transformed: transformed, Restored: restored, Blocked: blocked,
	}); err != nil {
		return &Error{Code: CodeInternalError, Stage: "receipt"}
	}
	if observe := s.config.Observers.Receipt; observe != nil {
		observe(ProfileStrict, "block")
	}
	return &Error{Code: CodeInputBlocked, Stage: "input"}
}

func (s *Service) strictAction(finding Finding) Action {
	if finding.Category == CategorySecret {
		return s.config.SecretAction
	}
	if action, ok := s.config.PIIEntityActions[finding.Entity]; ok {
		return action
	}
	if finding.Category == CategoryTechnical {
		return s.config.TechnicalAction
	}
	return s.config.PIIMode
}

func (s *Service) transformStandardValue(
	state *RequestState,
	summary *Summary,
	counters, next map[string]int,
	key, value string,
) string {
	if value == "" || s.config.Classifier == nil {
		return value
	}
	findings := s.config.Classifier.Classify(key, value)
	if len(findings) == 0 {
		return value
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Start < findings[j].Start })
	var out strings.Builder
	out.Grow(len(value))
	cursor := 0
	for _, finding := range findings {
		if finding.Start < cursor || finding.Start < 0 || finding.End > len(value) || finding.Start >= finding.End {
			continue
		}
		original := value[finding.Start:finding.End]
		identity := finding.Entity + "|" + CanonicalForm(original)
		counter, seen := counters[identity]
		if !seen {
			next[finding.Entity]++
			counter = next[finding.Entity]
			counters[identity] = counter
		}
		action := s.actionFor(finding.Entity)
		out.WriteString(value[cursor:finding.Start])
		out.WriteString(ApplyAction(action, finding.Entity, original, counter, s.config.PIIHashKey, s.config.PIIEncryptKey))
		cursor = finding.End
		summary.Add(finding.Entity)
		if state != nil {
			state.addTransformed(1)
		}
		if observe := s.config.Observers.Transformation; observe != nil {
			observe(ProfileStandard, finding.Entity, action)
		}
	}
	out.WriteString(value[cursor:])
	return out.String()
}

// After validates strict output or restores standard AES tokens from
// aggregated responses.
func (s *Service) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) (err error) {
	if s == nil || !s.config.PIIEnabled {
		return nil
	}
	state, stamped := StateFromContext(ctx)
	if stamped && state.effectiveProfile() == ProfileStrict {
		if !state.beginAfter() {
			return nil
		}
		defer state.releaseLease()
		defer func() {
			if recover() != nil {
				err = &Error{Code: CodeInternalError, Stage: "output"}
			}
			if err != nil {
				result := "error"
				var privacyErr *Error
				if errors.As(err, &privacyErr) && privacyErr.Code == CodeOutputBlocked {
					result = "block"
				}
				if receiptErr := s.setStrictReceipt(state, result); receiptErr != nil {
					err = receiptErr
				}
			}
		}()
		if resp == nil {
			if receiptErr := s.setStrictReceipt(state, "error"); receiptErr != nil {
				return receiptErr
			}
			return nil
		}
		working, err := cloneChatResponse(resp)
		if err != nil {
			return &Error{Code: CodeInternalError, Stage: "output"}
		}
		if err := s.transformOutbound(ctx, state, working); err != nil {
			return err
		}
		if err := s.setStrictReceipt(state, "pass"); err != nil {
			return err
		}
		*resp = *working
		return nil
	}
	if resp == nil {
		return nil
	}
	if stamped && !state.standardInboundPassed(s.lifecycleID) {
		return nil
	}
	if s.encryptActive() {
		entities := s.decryptEntities()
		restore := func(_ string, value string) string {
			return s.restoreStandardValue(state, entities, value)
		}
		transformStandardResponse(resp, restore)
	}
	coverage := "full"
	if req != nil && req.Stream {
		coverage = "input"
	}
	return s.setStandardReceipt(state, coverage)
}

func (s *Service) transformOutbound(_ context.Context, state *RequestState, resp *canonical.ChatResponse) error {
	authorization, err := s.prepareStrictOutbound(state, resp)
	if err != nil {
		return err
	}
	if err := s.verifyStrictOutboundResidual(state, resp, authorization); err != nil {
		return err
	}
	if err := s.restoreStrictOutbound(state, resp); err != nil {
		return err
	}
	return s.verifyStrictOutboundIntegrity(state, resp, authorization)
}

func (s *Service) setStrictReceipt(state *RequestState, result string) error {
	transformed, restored, blocked := state.counts()
	receipt := Receipt{
		Version:     1,
		Profile:     ProfileStrict,
		Scope:       state.Metadata().ScopeID,
		Coverage:    "full",
		Result:      result,
		Transformed: transformed,
		Restored:    restored,
		Blocked:     blocked,
	}
	if err := state.setReceipt(receipt); err != nil {
		return &Error{Code: CodeInternalError, Stage: "output"}
	}
	if observe := s.config.Observers.Receipt; observe != nil {
		observerOK := func() (ok bool) {
			defer func() { _ = recover() }()
			observe(ProfileStrict, result)
			return true
		}()
		if !observerOK {
			return &Error{Code: CodeInternalError, Stage: "output"}
		}
	}
	return nil
}

type outboundAuthorization struct {
	occurrences     []authorizedOccurrence
	generatedTokens map[string]struct{}
}

func (a *outboundAuthorization) authorize(ordinal, start, end int, token string) {
	a.occurrences = append(a.occurrences, authorizedOccurrence{
		ordinal: ordinal,
		start:   start,
		end:     end,
		kind:    authorizedOccurrenceGeneral,
	})
	if a.generatedTokens == nil {
		a.generatedTokens = make(map[string]struct{})
	}
	a.generatedTokens[token] = struct{}{}
}

func (a *outboundAuthorization) occurrenceAt(ordinal, start, end int) bool {
	for _, occurrence := range a.occurrences {
		if occurrence.ordinal == ordinal && occurrence.start == start && occurrence.end == end {
			return true
		}
	}
	return false
}

func (a *outboundAuthorization) occurrenceContaining(ordinal, start, end int) bool {
	for _, occurrence := range a.occurrences {
		if occurrence.ordinal == ordinal && start >= occurrence.start && end <= occurrence.end {
			return true
		}
	}
	return false
}

func (s *Service) prepareStrictOutbound(state *RequestState, resp *canonical.ChatResponse) (*outboundAuthorization, error) {
	lease := state.scopeLease()
	if lease == nil {
		return nil, &Error{Code: CodeInternalError, Stage: "output"}
	}
	counters := make(map[string]int)
	next := make(map[string]int)
	authorization := &outboundAuthorization{}
	ordinal := 0
	transform := func(key, value string) (string, error) {
		currentOrdinal := ordinal
		ordinal++
		for _, token := range s.parseReservedTokens(value) {
			if !token.valid || !state.tokenAuthorized(value[token.start:token.end]) {
				return "", s.blockOutbound(state, "token")
			}
		}

		findings := make([]Finding, 0, 4)
		if s.config.SecretClassifier != nil {
			findings = append(findings, s.config.SecretClassifier.Classify(key, value)...)
		}
		if s.config.Classifier != nil {
			findings = append(findings, s.config.Classifier.Classify(key, value)...)
		}
		accepted := Arbitrate(findings)
		type rewrite struct {
			replacement string
			authorize   bool
			changed     bool
		}
		rewrites := make([]rewrite, len(accepted))
		transformed := value
		for index := len(accepted) - 1; index >= 0; index-- {
			finding := accepted[index]
			if !validFindingRange(finding, value) {
				return "", &Error{Code: CodeInternalError, Stage: "output"}
			}
			original := value[finding.Start:finding.End]
			if tokenFindingAuthorized(state, value, finding) {
				continue
			}
			var replacement string
			var action Action
			switch finding.Category {
			case CategorySecret:
				return "", s.blockOutbound(state, "secret")
			case CategoryTechnical:
				if entry, ok := lease.ResolveSynthetic(finding.Entity, original); ok {
					if entry.Synthetic == original {
						rewrites[index] = rewrite{replacement: original}
						continue
					}
				}
				if entry, ok := lease.ResolveOriginal(finding.Entity, original); ok && entry.Provenance == ProvenanceInput {
					return "", s.blockOutbound(state, "original")
				}
				if reservedTechnicalAlias(finding.Entity, original) {
					return "", s.blockOutbound(state, "alias")
				}
				mapped, err := s.mapper.Map(lease, finding.Entity, original, ProvenanceGenerated)
				if err != nil {
					if errors.Is(err, errCapacityExceeded) {
						return "", &Error{Code: CodeCapacityExceeded, Stage: "mapping"}
					}
					return "", &Error{Code: CodeInternalError, Stage: "mapping"}
				}
				replacement = mapped
				action = ActionPseudonymize
			case CategoryPersonal:
				identity := finding.Entity + "|" + CanonicalForm(original)
				counter, ok := counters[identity]
				if !ok {
					next[finding.Entity]++
					counter = next[finding.Entity]
					counters[identity] = counter
				}
				replacement = ApplyAction(ActionReplace, finding.Entity, original, counter, nil, nil)
				action = ActionReplace
				rewrites[index].authorize = true
			default:
				return "", &Error{Code: CodeInternalError, Stage: "output"}
			}
			transformed = transformed[:finding.Start] + replacement + transformed[finding.End:]
			rewrites[index].replacement = replacement
			rewrites[index].changed = true
			state.addTransformed(1)
			if observe := s.config.Observers.Transformation; observe != nil {
				observe(ProfileStrict, finding.Entity, action)
			}
		}
		shift := 0
		for index, finding := range accepted {
			rewrite := rewrites[index]
			if !rewrite.changed {
				continue
			}
			finalStart := finding.Start + shift
			finalEnd := finalStart + len(rewrite.replacement)
			if rewrite.authorize {
				authorization.authorize(currentOrdinal, finalStart, finalEnd, rewrite.replacement)
			}
			shift += len(rewrite.replacement) - (finding.End - finding.Start)
		}
		return transformed, nil
	}
	if err := TransformResponseStrings(resp, transform); err != nil {
		var privacyErr *Error
		if errors.As(err, &privacyErr) {
			return nil, privacyErr
		}
		return nil, &Error{Code: CodeInternalError, Stage: "output"}
	}
	return authorization, nil
}

func (s *Service) verifyStrictOutboundResidual(
	state *RequestState,
	resp *canonical.ChatResponse,
	authorization *outboundAuthorization,
) error {
	lease := state.scopeLease()
	if lease == nil {
		return &Error{Code: CodeInternalError, Stage: "output"}
	}
	ordinal := 0
	return VisitResponseStrings(resp, func(key, value string) error {
		currentOrdinal := ordinal
		ordinal++
		for _, token := range s.parseReservedTokens(value) {
			raw := value[token.start:token.end]
			if !token.valid || (!state.tokenAuthorized(raw) && !authorization.occurrenceAt(currentOrdinal, token.start, token.end)) {
				return s.blockOutbound(state, "token")
			}
		}
		for _, payload := range bareEncryptedPayloadRE.FindAllString(value, -1) {
			if !state.tokenAuthorized(payload) {
				return s.blockOutbound(state, "token")
			}
		}

		secretFindings := []Finding(nil)
		if s.config.SecretClassifier != nil {
			secretFindings = s.config.SecretClassifier.Classify(key, value)
		}
		configuredFindings := []Finding(nil)
		if s.config.Classifier != nil {
			configuredFindings = s.config.Classifier.Classify(key, value)
		}
		findings := append(append(make([]Finding, 0, len(secretFindings)+len(configuredFindings)), secretFindings...), configuredFindings...)
		for _, finding := range Arbitrate(findings) {
			if !validFindingRange(finding, value) {
				return &Error{Code: CodeInternalError, Stage: "output"}
			}
			if authorization.occurrenceContaining(currentOrdinal, finding.Start, finding.End) || tokenFindingAuthorized(state, value, finding) {
				continue
			}
			matched := value[finding.Start:finding.End]
			if finding.Category == CategoryTechnical {
				if entry, ok := lease.ResolveSynthetic(finding.Entity, matched); ok && entry.Synthetic == matched {
					continue
				}
			}
			if observe := s.config.Observers.Residual; observe != nil {
				observe(ProfileStrict, "output", finding.Entity)
			}
			return s.blockOutbound(state, "residual")
		}
		return nil
	})
}

func (s *Service) restoreStrictOutbound(state *RequestState, resp *canonical.ChatResponse) error {
	lease := state.scopeLease()
	if lease == nil {
		return &Error{Code: CodeInternalError, Stage: "output"}
	}
	restore := func(key, value string) (string, error) {
		findings := []Finding(nil)
		if s.config.Classifier != nil {
			findings = Arbitrate(s.config.Classifier.Classify(key, value))
		}
		restored := value
		for index := len(findings) - 1; index >= 0; index-- {
			finding := findings[index]
			if finding.Category != CategoryTechnical || !validFindingRange(finding, value) {
				continue
			}
			matched := value[finding.Start:finding.End]
			entry, ok := lease.ResolveSynthetic(finding.Entity, matched)
			if !ok || entry.Provenance != ProvenanceInput || entry.Synthetic != matched {
				continue
			}
			restored = restored[:finding.Start] + entry.Original + restored[finding.End:]
			state.addRestored(1)
			if observe := s.config.Observers.Restoration; observe != nil {
				observe(ProfileStrict, finding.Entity, "pass")
			}
		}
		restored = wrappedEncryptedTokenRE.ReplaceAllStringFunc(restored, func(token string) string {
			if !state.tokenAuthorized(token) {
				return token
			}
			match := wrappedEncryptedTokenRE.FindStringSubmatch(token)
			if len(match) != 3 || !state.tokenAuthorized(match[2]) {
				return token
			}
			plaintext, err := DecryptToken(s.config.PIIEncryptKey, match[1], match[2])
			if err != nil {
				return token
			}
			state.addRestored(1)
			if observe := s.config.Observers.Restoration; observe != nil {
				observe(ProfileStrict, match[1], "pass")
			}
			return plaintext
		})
		return bareEncryptedPayloadRE.ReplaceAllStringFunc(restored, func(payload string) string {
			if !state.tokenAuthorized(payload) {
				return payload
			}
			for _, entity := range s.decryptEntities() {
				plaintext, err := DecryptToken(s.config.PIIEncryptKey, entity, payload)
				if err != nil {
					continue
				}
				state.addRestored(1)
				if observe := s.config.Observers.Restoration; observe != nil {
					observe(ProfileStrict, entity, "pass")
				}
				return plaintext
			}
			return payload
		}), nil
	}
	if err := TransformResponseStrings(resp, restore); err != nil {
		return &Error{Code: CodeInternalError, Stage: "output"}
	}
	return nil
}

func (s *Service) verifyStrictOutboundIntegrity(
	state *RequestState,
	resp *canonical.ChatResponse,
	authorization *outboundAuthorization,
) error {
	lease := state.scopeLease()
	if lease == nil {
		return &Error{Code: CodeInternalError, Stage: "output"}
	}
	return VisitResponseStrings(resp, func(_ string, value string) error {
		for _, token := range s.parseReservedTokens(value) {
			raw := value[token.start:token.end]
			_, generated := authorization.generatedTokens[raw]
			if !token.valid || (!state.tokenAuthorized(raw) && !generated) {
				return s.blockOutbound(state, "integrity")
			}
		}
		for _, match := range outboundIPv4PatternInternal.FindAllString(value, -1) {
			address, err := netip.ParseAddr(match)
			if err != nil || !netip.MustParsePrefix("198.18.0.0/15").Contains(address) {
				continue
			}
			entry, ok := lease.ResolveSynthetic("IPv4", match)
			if !ok || entry.Provenance != ProvenanceGenerated {
				return s.blockOutbound(state, "integrity")
			}
		}
		return nil
	})
}

var outboundIPv4PatternInternal = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)

func tokenFindingAuthorized(state *RequestState, value string, finding Finding) bool {
	for _, token := range state.authorizedTokenValues() {
		for offset := 0; offset < len(value); {
			index := strings.Index(value[offset:], token)
			if index < 0 {
				break
			}
			start := offset + index
			if finding.Start >= start && finding.End <= start+len(token) {
				return true
			}
			offset = start + len(token)
		}
	}
	return false
}

func reservedTechnicalAlias(entity, value string) bool {
	switch entity {
	case "IPv4":
		address, err := netip.ParseAddr(strings.TrimSuffix(value, "/15"))
		prefix := netip.MustParsePrefix("198.18.0.0/15")
		return err == nil && prefix.Contains(address)
	case "IPv6":
		address, err := netip.ParseAddr(value)
		prefix := netip.MustParsePrefix("2001:db8::/32")
		return err == nil && prefix.Contains(address)
	case "SIP_URI":
		return strings.Contains(value, "@gw.invalid")
	case "SITE":
		return strings.Contains(value, "-SYN-")
	default:
		return false
	}
}

func (s *Service) blockOutbound(state *RequestState, reason string) error {
	state.addBlocked(1)
	s.requestsBlocked.Add(1)
	if observe := s.config.Observers.Block; observe != nil {
		observe(ProfileStrict, "output", reason)
	}
	return &Error{Code: CodeOutputBlocked, Stage: "output"}
}

func cloneChatResponse(resp *canonical.ChatResponse) (*canonical.ChatResponse, error) {
	if resp == nil {
		return nil, nil
	}
	clone := *resp
	clone.Message.Content = append([]canonical.ContentPart(nil), resp.Message.Content...)
	for index := range clone.Message.Content {
		part := &clone.Message.Content[index]
		if part.Image != nil {
			image := *part.Image
			part.Image = &image
		}
		if part.ToolResult != nil {
			result := *part.ToolResult
			part.ToolResult = &result
		}
		if part.ToolUse != nil {
			toolUse := *part.ToolUse
			input, err := TransformStrings(part.ToolUse.Input, func(_ string, value string) (string, error) { return value, nil })
			if err != nil {
				return nil, err
			}
			toolUse.Input = input.(map[string]any)
			part.ToolUse = &toolUse
		}
	}
	clone.Message.ToolCalls = append([]canonical.ToolCall(nil), resp.Message.ToolCalls...)
	for index := range clone.Message.ToolCalls {
		arguments, err := TransformStrings(clone.Message.ToolCalls[index].Arguments, func(_ string, value string) (string, error) { return value, nil })
		if err != nil {
			return nil, err
		}
		clone.Message.ToolCalls[index].Arguments = arguments.(map[string]any)
	}
	return &clone, nil
}

func (s *Service) setStandardReceipt(state *RequestState, coverage string) error {
	if state == nil {
		return nil
	}
	transformed, restored, blocked := state.counts()
	receipt := Receipt{
		Version:     1,
		Profile:     ProfileStandard,
		Scope:       state.Metadata().ScopeID,
		Coverage:    coverage,
		Result:      "pass",
		Transformed: transformed,
		Restored:    restored,
		Blocked:     blocked,
	}
	if err := state.setReceipt(receipt); err != nil {
		if observe := s.config.Observers.Receipt; observe != nil {
			observe(ProfileStandard, "error")
		}
		return &Error{Code: CodeInternalError, Stage: "receipt", Cause: err}
	}
	if observe := s.config.Observers.Receipt; observe != nil {
		observe(ProfileStandard, "pass")
	}
	return nil
}

func (s *Service) restoreStandardValue(state *RequestState, entities []string, value string) string {
	restored := wrappedEncryptedTokenRE.ReplaceAllStringFunc(value, func(token string) string {
		match := wrappedEncryptedTokenRE.FindStringSubmatch(token)
		if len(match) != 3 {
			return token
		}
		plaintext, err := DecryptToken(s.config.PIIEncryptKey, match[1], match[2])
		if err != nil {
			return token
		}
		if state != nil {
			state.addRestored(1)
		}
		return plaintext
	})
	if len(entities) == 0 {
		return restored
	}
	return bareEncryptedPayloadRE.ReplaceAllStringFunc(restored, func(payload string) string {
		for _, entity := range entities {
			plaintext, err := DecryptToken(s.config.PIIEncryptKey, entity, payload)
			if err != nil {
				continue
			}
			if state != nil {
				state.addRestored(1)
			}
			return plaintext
		}
		return payload
	})
}

func transformStandardRequest(req *canonical.ChatRequest, transform func(key, value string) string) {
	req.System = transform("system", req.System)
	for index := range req.Messages {
		transformStandardMessage(&req.Messages[index], transform, false)
	}
}

func transformStandardResponse(resp *canonical.ChatResponse, transform func(key, value string) string) {
	transformStandardMessage(&resp.Message, transform, true)
}

func transformStandardMessage(message *canonical.Message, transform func(key, value string) string, toolCalls bool) {
	for index := range message.Content {
		part := &message.Content[index]
		switch part.Kind {
		case canonical.ContentKindText:
			part.Text = transform("text", part.Text)
		case canonical.ContentKindToolUse:
			if part.ToolUse != nil && part.ToolUse.Input != nil {
				if transformed, ok := WalkStrings(part.ToolUse.Input, func(value string) string {
					return transform("", value)
				}).(map[string]any); ok {
					part.ToolUse.Input = transformed
				}
			}
		case canonical.ContentKindToolResult:
			if part.ToolResult != nil {
				part.ToolResult.Content = transform("content", part.ToolResult.Content)
			}
		}
	}
	if !toolCalls {
		return
	}
	for index := range message.ToolCalls {
		if transformed, ok := WalkStrings(message.ToolCalls[index].Arguments, func(value string) string {
			return transform("", value)
		}).(map[string]any); ok {
			message.ToolCalls[index].Arguments = transformed
		}
	}
}

// WalkStrings preserves the standard compatibility traversal behavior.
func WalkStrings(value any, transform func(string) string) any {
	return walkCompatibilityStrings(value, transform, 0)
}

func walkCompatibilityStrings(value any, transform func(string) string, depth int) any {
	if depth > maxCompatibilityTraversalDepth {
		compatibilityDepthWarning.Do(func() {
			slog.Default().Warn("privacy.walk.depth_truncated", "max_depth", maxCompatibilityTraversalDepth)
		})
		return value
	}
	switch typed := value.(type) {
	case string:
		return transform(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = walkCompatibilityStrings(child, transform, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = walkCompatibilityStrings(child, transform, depth+1)
		}
		return out
	default:
		return value
	}
}

func (s *Service) actionFor(entity string) Action {
	if action, ok := s.config.PIIEntityActions[entity]; ok {
		return action
	}
	return s.config.PIIMode
}

func (s *Service) encryptActive() bool {
	if s.config.PIIMode == ActionEncrypt {
		return true
	}
	for _, action := range s.config.PIIEntityActions {
		if action == ActionEncrypt {
			return true
		}
	}
	return false
}

func (s *Service) decryptEntities() []string {
	entities := make([]string, 0, len(s.config.Recognizers)+2)
	seen := make(map[string]struct{}, len(s.config.Recognizers)+2)
	add := func(entity string) {
		if _, ok := seen[entity]; ok || s.actionFor(entity) != ActionEncrypt {
			return
		}
		seen[entity] = struct{}{}
		entities = append(entities, entity)
	}
	for _, entity := range s.config.Recognizers {
		add(entity)
	}
	if s.config.NEREnabled {
		add("PERSON")
		add("LOCATION")
	}
	return entities
}

// Describe returns the legacy hook-health projection without key material.
func (s *Service) Describe() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	return map[string]any{
		"enabled":        s.config.PIIEnabled,
		"mode":           string(s.config.PIIMode),
		"entities":       append([]string(nil), s.config.Recognizers...),
		"decrypt_active": s.encryptActive(),
		"entity_actions": cloneActions(s.config.PIIEntityActions),
	}
}

// Snapshot returns a safe copied runtime projection.
func (s *Service) Snapshot() SafeSnapshot {
	if s == nil {
		return SafeSnapshot{}
	}
	s.lastErrorMu.Lock()
	lastError := s.lastErrorCode
	s.lastErrorMu.Unlock()
	snapshot := SafeSnapshot{
		DefaultProfile:     s.config.DefaultProfile,
		RequestProfiles:    append([]Profile(nil), s.config.RequestProfiles...),
		StrictAvailable:    containsProfile(s.config.RequestProfiles, ProfileStrict),
		SecretAction:       s.config.SecretAction,
		TechnicalAction:    s.config.TechnicalAction,
		PIIMode:            s.config.PIIMode,
		PIIEnabled:         s.config.PIIEnabled,
		NEREnabled:         s.config.NEREnabled,
		AliasKeyPresent:    len(s.config.AliasKey) != 0,
		TriageEnabled:      s.config.TriageEnabled,
		Recognizers:        append([]string(nil), s.config.Recognizers...),
		EntityActions:      cloneActions(s.config.PIIEntityActions),
		ScopeTTL:           s.config.ScopeTTL,
		MaxScopes:          s.config.MaxScopes,
		MaxEntriesPerScope: s.config.MaxEntriesPerScope,
		MaxTotalEntries:    s.config.MaxTotalEntries,
		RequestsProtected:  s.requestsProtected.Load(),
		RequestsBlocked:    s.requestsBlocked.Load(),
		LastErrorCode:      lastError,
	}
	if s.store != nil {
		storeSnapshot := s.store.Snapshot()
		snapshot.ScopesActive = storeSnapshot.ScopesActive
		snapshot.RequestsInFlight = storeSnapshot.RequestsInFlight
		snapshot.Entries = storeSnapshot.Entries
		snapshot.OldestScopeAge = storeSnapshot.OldestScopeAge
	}
	return snapshot
}

func containsProfile(profiles []Profile, want Profile) bool {
	for _, profile := range profiles {
		if profile == want {
			return true
		}
	}
	return false
}

// AllowSensitiveTrace reports whether the current request may use the legacy
// explicitly sensitive trace path. Strict requests never may.
func (s *Service) AllowSensitiveTrace(ctx context.Context) bool {
	state, ok := StateFromContext(ctx)
	return !ok || state.effectiveProfile() != ProfileStrict
}

// TraceSummary returns bounded aggregate metadata only.
func (s *Service) TraceSummary(ctx context.Context) map[string]any {
	state, ok := StateFromContext(ctx)
	if !ok {
		return map[string]any{"profile": string(ProfileStandard)}
	}
	transformed, restored, blocked := state.counts()
	return map[string]any{
		"profile":     string(state.effectiveProfile()),
		"scope":       state.Metadata().ScopeID,
		"transformed": transformed,
		"restored":    restored,
		"blocked":     blocked,
	}
}

// Close stops and joins the service reaper, rejects future strict scope
// acquisition, and closes retained scopes without disrupting active leases.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.reaperStop != nil {
			close(s.reaperStop)
			<-s.reaperDone
		}
		if s.store != nil {
			s.store.ClearAll()
		}
	})
}
