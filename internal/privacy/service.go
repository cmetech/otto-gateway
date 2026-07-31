package privacy

import (
	"context"
	"encoding/json"
	"log/slog"
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
)

const maxCompatibilityTraversalDepth = 64

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
	config Config

	requestsProtected atomic.Uint64
	requestsBlocked   atomic.Uint64
	lastErrorMu       sync.Mutex
	lastErrorCode     string
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
	return &Service{config: config}, nil
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

// Before applies byte-compatible standard PII transformations.
func (s *Service) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if s == nil || !s.config.PIIEnabled || req == nil {
		return nil, nil
	}
	state, _ := StateFromContext(ctx)
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
	return nil, nil
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

// After restores standard AES tokens from aggregated responses.
func (s *Service) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	if s == nil || !s.config.PIIEnabled || resp == nil {
		return nil
	}
	state, _ := StateFromContext(ctx)
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
	return SafeSnapshot{
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
}

func containsProfile(profiles []Profile, want Profile) bool {
	for _, profile := range profiles {
		if profile == want {
			return true
		}
	}
	return false
}

// Close releases future service-owned lifecycle resources.
func (s *Service) Close() {}
