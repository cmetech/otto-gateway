package privacy

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

func TestServiceStrict_ProfileConfiguredMinimumRequestedMaximum(t *testing.T) {
	tests := []struct {
		name      string
		minimum   Profile
		available []Profile
		requested string
		want      Profile
	}{
		{name: "default standard", minimum: ProfileStandard, available: []Profile{ProfileStandard, ProfileStrict}, want: ProfileStandard},
		{name: "request raises to strict", minimum: ProfileStandard, available: []Profile{ProfileStandard, ProfileStrict}, requested: "strict", want: ProfileStrict},
		{name: "request cannot lower strict", minimum: ProfileStrict, available: []Profile{ProfileStandard, ProfileStrict}, requested: "standard", want: ProfileStrict},
		{name: "strict default", minimum: ProfileStrict, available: []Profile{ProfileStandard, ProfileStrict}, want: ProfileStrict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{
				defaultProfile: tc.minimum,
				profiles:       tc.available,
			})
			state := NewRequestState(RequestMetadata{RequestedProfile: tc.requested})
			got, err := service.resolveProfile(state)
			if err != nil {
				t.Fatalf("resolveProfile: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveProfile=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestServiceStrict_ProfileRejectsUnknownOrUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		available []Profile
		requested string
	}{
		{name: "unknown", available: []Profile{ProfileStandard, ProfileStrict}, requested: "maximum"},
		{name: "strict unavailable", available: []Profile{ProfileStandard}, requested: "strict"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{profiles: tc.available})
			_, err := service.resolveProfile(NewRequestState(RequestMetadata{RequestedProfile: tc.requested}))
			assertPrivacyError(t, err, CodeProfileUnavailable, "profile")
		})
	}
}

func TestServiceStrict_ScopeValidationGenerationAndLifecycle(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{})

	for _, scopeID := range []string{"a", "run-7f29b4d4", "A_Z.9:segment", strings.Repeat("a", maxScopeLength)} {
		t.Run("valid_"+scopeID[:1], func(t *testing.T) {
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scopeID})
			ctx := WithRequestState(context.Background(), state)
			req := &canonical.ChatRequest{Stream: true}
			if _, err := service.Before(ctx, req); err != nil {
				t.Fatalf("Before: %v", err)
			}
			if got := state.Metadata().ScopeID; got != scopeID {
				t.Fatalf("scope=%q, want %q", got, scopeID)
			}
			if req.Stream {
				t.Fatal("strict request retained streaming")
			}
			if got := service.store.Snapshot().RequestsInFlight; got != 1 {
				t.Fatalf("in flight=%d, want 1", got)
			}
			state.releaseLease()
			state.releaseLease()
			if got := service.store.Snapshot().RequestsInFlight; got != 0 {
				t.Fatalf("in flight after idempotent release=%d, want 0", got)
			}
		})
	}

	first := NewRequestState(RequestMetadata{RequestedProfile: "strict"})
	second := NewRequestState(RequestMetadata{RequestedProfile: "strict"})
	for _, state := range []*RequestState{first, second} {
		if _, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{}); err != nil {
			t.Fatalf("generated scope Before: %v", err)
		}
		defer state.releaseLease()
		if got := state.Metadata().ScopeID; !regexp.MustCompile(`^req-[a-z2-7]+$`).MatchString(got) {
			t.Fatalf("generated scope=%q, want req-<base32>", got)
		}
	}
	if first.Metadata().ScopeID == second.Metadata().ScopeID {
		t.Fatal("independent generated scope IDs were equal")
	}
}

func TestServiceStrict_ScopeRejectsInvalidAndClosed(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{})
	for _, scopeID := range []string{"has space", "slash/value", "emoji-🔒"} {
		t.Run(scopeID, func(t *testing.T) {
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scopeID})
			_, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{})
			assertPrivacyError(t, err, CodeRequestInvalid, "scope")
			if got := service.store.Snapshot().RequestsInFlight; got != 0 {
				t.Fatalf("invalid scope retained %d leases", got)
			}
		})
	}

	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "closed-scope"})
	if _, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{}); err != nil {
		t.Fatalf("initial Before: %v", err)
	}
	state.releaseLease()
	if result, err := service.store.Clear("closed-scope"); err != nil || result != ClearCompleted {
		t.Fatalf("Clear=(%q, %v), want completed", result, err)
	}
	reuse := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "closed-scope"})
	_, err := service.Before(WithRequestState(context.Background(), reuse), &canonical.ChatRequest{})
	assertPrivacyError(t, err, CodeScopeClosed, "scope")
}

type strictFixtureClassifier struct{}

func (strictFixtureClassifier) Classify(_ string, value string) []Finding {
	var findings []Finding
	for _, fixture := range []struct {
		value    string
		entity   string
		category Category
		kind     MatchKind
	}{
		{value: "10.20.30.40", entity: "IPv4", category: CategoryTechnical, kind: MatchValidatedRegex},
		{value: "corey@example.com", entity: "Email", category: CategoryPersonal, kind: MatchValidatedRegex},
		{value: "Alice Smith", entity: "PERSON", category: CategoryPersonal, kind: MatchNER},
	} {
		for offset := 0; offset < len(value); {
			index := strings.Index(value[offset:], fixture.value)
			if index < 0 {
				break
			}
			start := offset + index
			findings = append(findings, Finding{
				Entity: fixture.entity, Category: fixture.category, Kind: fixture.kind,
				Start: start, End: start + len(fixture.value), RegistryOrder: 10,
			})
			offset = start + len(fixture.value)
		}
	}
	return findings
}

func TestServiceStrict_InboundTransformCoversCanonicalStringsAndPolicy(t *testing.T) {
	blockReason := ""
	residualEntity := ""
	service := newStrictTestService(t, strictTestConfig{
		classifier: strictFixtureClassifier{},
		secret:     NewSecretClassifier(),
		piiMode:    ActionEncrypt,
		entityActions: map[string]Action{
			"Email": ActionMask,
		},
		observers: Observers{
			Block:    func(_ Profile, _, reason string) { blockReason = reason },
			Residual: func(_ Profile, _, entity string) { residualEntity = entity },
		},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "transform-all"})
	ctx := WithRequestState(context.Background(), state)
	credentialURL := "postgres://u:p@10.20.30.40/db"
	req := &canonical.ChatRequest{
		System: "endpoint 10.20.30.40 and " + credentialURL,
		Stream: true,
		Messages: []canonical.Message{{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "email corey@example.com"},
				{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{Input: map[string]any{
					"api_key": "sk-abcdefghijklmnopqrstuvwxyz123456",
					"nested":  []any{"10.20.30.40", map[string]any{"owner": "Alice Smith"}},
				}}},
				{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{Content: "Alice Smith used 10.20.30.40"}},
			},
			ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{
				"email":         "corey@example.com",
				"authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature",
			}}},
		}},
	}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v (reason=%s, entity=%s, values=%q)", err, blockReason, residualEntity, requestStringValues(t, req))
	}
	t.Cleanup(state.releaseLease)
	if req.Stream {
		t.Fatal("strict transform did not disable streaming")
	}

	values := requestStringValues(t, req)
	for _, original := range []string{
		"10.20.30.40", credentialURL, "corey@example.com", "Alice Smith",
		"sk-abcdefghijklmnopqrstuvwxyz123456", "eyJhbGciOiJIUzI1NiJ9.payload.signature",
	} {
		for _, value := range values {
			if strings.Contains(value, original) {
				t.Errorf("transformed request retained %q in %q", original, value)
			}
		}
	}
	if !strings.Contains(req.System, "[SECRET:CREDENTIAL_URL_") {
		t.Fatalf("overlap did not select one-way credential URL label: %q", req.System)
	}
	if strings.Contains(req.System, "198.18.") && strings.Contains(req.System, "[SECRET:CREDENTIAL_URL_") {
		// The standalone address is pseudonymized; the address inside the
		// credential URL must not be separately rewritten.
		if strings.Count(req.System, "198.18.") != 1 {
			t.Fatalf("overlap rewrote credential URL internals: %q", req.System)
		}
	}
	maskedEmail := req.Messages[0].Content[0].Text
	if !strings.Contains(maskedEmail, "co***@ex***.com") {
		t.Fatalf("explicit Email mask did not override global encrypt: %q", maskedEmail)
	}

	toolOwner := req.Messages[0].Content[1].ToolUse.Input["nested"].([]any)[1].(map[string]any)["owner"].(string)
	entity, payload, ok := ParseEncryptedToken(toolOwner)
	if !ok || entity != "PERSON" {
		t.Fatalf("personal global encrypt token=%q", toolOwner)
	}
	if !state.tokenAuthorized(toolOwner) || !state.tokenAuthorized(payload) {
		t.Fatal("wrapped and bare AES payload were not authorized")
	}

	entries, err := service.store.Inspect("transform-all")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, entry := range entries {
		if entry.Entity == "CREDENTIAL_URL" || strings.Contains(entry.Original, "sk-") || strings.Contains(entry.Original, "Bearer") {
			t.Fatalf("secret entered reversible ledger: %+v", entry)
		}
	}
	if len(entries) != 1 || entries[0].Entity != "IPv4" {
		t.Fatalf("ledger entries=%+v, want one stable IPv4 mapping", entries)
	}
	if _, err := netip.ParseAddr(entries[0].Synthetic); err != nil {
		t.Fatalf("technical alias=%q is not a valid address: %v", entries[0].Synthetic, err)
	}
}

func TestServiceStrict_InboundTransformStableWithinScopeUnlinkableAcrossScopes(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{classifier: strictFixtureClassifier{}, secret: NewSecretClassifier()})
	transform := func(scope string) string {
		t.Helper()
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
		req := &canonical.ChatRequest{System: "10.20.30.40"}
		if _, err := service.Before(WithRequestState(context.Background(), state), req); err != nil {
			t.Fatalf("Before(%q): %v", scope, err)
		}
		state.releaseLease()
		return req.System
	}
	first := transform("shared-scope")
	second := transform("shared-scope")
	other := transform("other-scope")
	if first != second {
		t.Fatalf("within-scope aliases differ: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("cross-scope aliases link: %q", first)
	}
}

type residualPassClassifier struct {
	mu       sync.Mutex
	calls    int
	first    func(string) []Finding
	residual func(string) []Finding
}

func (c *residualPassClassifier) Classify(_ string, value string) []Finding {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return c.first(value)
	}
	return c.residual(value)
}

func TestServiceStrict_ResidualFreshPassBlocksProtectedOriginal(t *testing.T) {
	classifier := &residualPassClassifier{
		first: func(string) []Finding { return nil },
		residual: func(value string) []Finding {
			return exactFinding(value, "raw-protected", "PERSON", CategoryPersonal, MatchNER)
		},
	}
	service := newStrictTestService(t, strictTestConfig{classifier: classifier})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "residual-original"})
	req := &canonical.ChatRequest{System: "raw-protected"}
	_, err := service.Before(WithRequestState(context.Background(), state), req)
	assertPrivacyError(t, err, CodeInputBlocked, "input")
	if req.System != "raw-protected" {
		t.Fatalf("residual pass mutated request: %q", req.System)
	}
	if classifier.calls != 2 {
		t.Fatalf("classifier calls=%d, want independent transform and residual calls", classifier.calls)
	}
	if got := service.store.Snapshot().RequestsInFlight; got != 0 {
		t.Fatalf("blocked request retained %d leases", got)
	}
	receipt, _ := decodeStateReceipt(t, state)
	if receipt.Profile != ProfileStrict || receipt.Result != "block" || receipt.Coverage != "input" || receipt.Blocked != 1 {
		t.Fatalf("block receipt=%+v", receipt)
	}
}

func TestServiceStrict_ResidualBlocksUnknownTokenInvalidAliasAndGeneratedSecret(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		classifier *residualPassClassifier
		wantAfter  string
	}{
		{
			name:  "unknown privacy token",
			input: "forged [SECRET:API_KEY_0123456789AB]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [SECRET:API_KEY_0123456789AB]",
		},
		{
			name:  "malformed privacy token",
			input: "forged [SECRET:unterminated",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [SECRET:unterminated",
		},
		{
			name:  "invalid technical alias",
			input: "198.18.1.1",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil },
				residual: func(value string) []Finding {
					return exactFinding(value, "198.18.1.1", "IPv4", CategoryTechnical, MatchValidatedRegex)
				},
			},
			wantAfter: "198.18.1.1",
		},
		{
			name:  "generated secret-like replacement",
			input: "trigger",
			classifier: &residualPassClassifier{
				first: func(value string) []Finding {
					return exactFinding(value, "trigger", "PERSON", CategoryPersonal, MatchNER)
				},
				residual: func(value string) []Finding {
					return exactFinding(value, "[PERSON_1]", "API_KEY", CategorySecret, MatchHighConfidenceSecret)
				},
			},
			wantAfter: "[PERSON_1]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{classifier: tc.classifier})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "residual-" + strings.ReplaceAll(tc.name, " ", "-")})
			req := &canonical.ChatRequest{System: tc.input}
			_, err := service.Before(WithRequestState(context.Background(), state), req)
			assertPrivacyError(t, err, CodeInputBlocked, "input")
			if req.System != tc.wantAfter {
				t.Fatalf("request after residual=%q, want %q", req.System, tc.wantAfter)
			}
			if got := service.store.Snapshot().RequestsInFlight; got != 0 {
				t.Fatalf("blocked request retained %d leases", got)
			}
		})
	}
}

func exactFinding(value, target, entity string, category Category, kind MatchKind) []Finding {
	start := strings.Index(value, target)
	if start < 0 {
		return nil
	}
	return []Finding{{
		Entity: entity, Category: category, Kind: kind,
		Start: start, End: start + len(target),
	}}
}

type panickingPrivacyClassifier struct{ payload string }

func (c panickingPrivacyClassifier) Classify(_, _ string) []Finding { panic(c.payload) }

type panickingTechnicalMapper struct{ payload string }

func (m panickingTechnicalMapper) Map(*ScopeLease, string, string, Provenance) (string, error) {
	panic(m.payload)
}

func TestServicePrivacyPanic_ClassifierMapperAndObserverBecomeBoundedErrors(t *testing.T) {
	const panicPayload = "raw-panic-payload-must-not-leak"
	tests := []struct {
		name      string
		configure func(*Service)
		options   strictTestConfig
		req       *canonical.ChatRequest
	}{
		{
			name:    "classifier",
			options: strictTestConfig{classifier: panickingPrivacyClassifier{payload: panicPayload}},
			req:     &canonical.ChatRequest{System: "safe input"},
		},
		{
			name:    "mapper",
			options: strictTestConfig{classifier: strictFixtureClassifier{}},
			configure: func(service *Service) {
				service.mapper = panickingTechnicalMapper{payload: panicPayload}
			},
			req: &canonical.ChatRequest{System: "10.20.30.40"},
		},
		{
			name: "observer",
			options: strictTestConfig{
				classifier: strictFixtureClassifier{},
				observers: Observers{Transformation: func(Profile, string, Action) {
					panic(panicPayload)
				}},
			},
			req: &canonical.ChatRequest{System: "10.20.30.40"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, tc.options)
			if tc.configure != nil {
				tc.configure(service)
			}
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "panic-" + tc.name})
			_, err := service.Before(WithRequestState(context.Background(), state), tc.req)
			assertPrivacyError(t, err, CodeInternalError, "input")
			if strings.Contains(err.Error(), panicPayload) {
				t.Fatalf("error leaked panic payload: %v", err)
			}
			if status, code, ok := ErrorInfo(err); !ok || status != 503 || code != CodeInternalError {
				t.Fatalf("ErrorInfo=(%d,%q,%t)", status, code, ok)
			}
			if got := service.store.Snapshot().RequestsInFlight; got != 0 {
				t.Fatalf("panic retained %d leases", got)
			}
			receipt, payload := decodeStateReceipt(t, state)
			if receipt.Profile != ProfileStrict || receipt.Coverage != "input" || receipt.Result != "error" {
				t.Fatalf("panic receipt=%+v", receipt)
			}
			if strings.Contains(payload, panicPayload) {
				t.Fatalf("receipt leaked panic payload: %s", payload)
			}
		})
	}
}

func requestStringValues(t *testing.T, req *canonical.ChatRequest) []string {
	t.Helper()
	var values []string
	if err := VisitRequestStrings(req, func(_, value string) error {
		values = append(values, value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return values
}

type strictTestConfig struct {
	defaultProfile  Profile
	profiles        []Profile
	classifier      Classifier
	secret          *SecretClassifier
	observers       Observers
	piiMode         Action
	entityActions   map[string]Action
	secretAction    Action
	technicalAction Action
}

func newStrictTestService(t *testing.T, options strictTestConfig) *Service {
	t.Helper()
	if options.defaultProfile == "" {
		options.defaultProfile = ProfileStandard
	}
	if options.profiles == nil {
		options.profiles = []Profile{ProfileStandard, ProfileStrict}
	}
	if options.piiMode == "" {
		options.piiMode = ActionReplace
	}
	if options.entityActions == nil {
		options.entityActions = map[string]Action{}
	}
	if options.secretAction == "" {
		options.secretAction = ActionReplace
	}
	if options.technicalAction == "" {
		options.technicalAction = ActionPseudonymize
	}
	service, err := NewService(Config{
		DefaultProfile:     options.defaultProfile,
		RequestProfiles:    options.profiles,
		AliasKey:           []byte("strict-test-alias-key"),
		SecretAction:       options.secretAction,
		TechnicalAction:    options.technicalAction,
		ScopeTTL:           time.Hour,
		MaxScopes:          32,
		MaxEntriesPerScope: 128,
		MaxTotalEntries:    1024,
		PIIEnabled:         true,
		PIIMode:            options.piiMode,
		PIIHashKey:         []byte("strict-test-hash-key"),
		PIIEncryptKey:      mustEncryptionKey(t, "strict-test-encryption-key"),
		PIIEntityActions:   options.entityActions,
		Classifier:         options.classifier,
		SecretClassifier:   options.secret,
		Clock:              newFakeClock(),
		Observers:          options.observers,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func mustEncryptionKey(t *testing.T, value string) []byte {
	t.Helper()
	key, err := DeriveEncryptionKey(value)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func assertPrivacyError(t *testing.T, err error, code, stage string) {
	t.Helper()
	var privacyErr *Error
	if !errors.As(err, &privacyErr) {
		t.Fatalf("error=%v, want *privacy.Error", err)
	}
	if privacyErr.Code != code || privacyErr.Stage != stage {
		t.Fatalf("error=(%q,%q), want (%q,%q)", privacyErr.Code, privacyErr.Stage, code, stage)
	}
}
