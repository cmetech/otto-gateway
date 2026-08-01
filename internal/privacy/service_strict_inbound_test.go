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
	"unicode/utf8"

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

const inboundReceiptProtectedCanary = "raw-protected-inbound-receipt-canary"

type receiptObservation struct {
	profile Profile
	result  string
}

type receiptObserverRecorder struct {
	mu     sync.Mutex
	events []receiptObservation
}

func (r *receiptObserverRecorder) observe(profile Profile, result string) {
	r.mu.Lock()
	r.events = append(r.events, receiptObservation{profile: profile, result: result})
	r.mu.Unlock()
}

func (r *receiptObserverRecorder) snapshot() []receiptObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receiptObservation(nil), r.events...)
}

type failingTechnicalMapper struct{ err error }

func (m failingTechnicalMapper) Map(*ScopeLease, string, string, Provenance) (string, error) {
	return "", m.err
}

func TestServiceBefore_StampedInboundFailuresSetOneErrorReceipt(t *testing.T) {
	tests := []struct {
		name                 string
		code                 string
		stage                string
		wantProfile          Profile
		wantScope            string
		forbiddenScopeCanary string
		setup                func(*testing.T, *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest)
	}{
		{
			name:        "requested profile unavailable",
			code:        CodeProfileUnavailable,
			stage:       "profile",
			wantProfile: ProfileStandard,
			wantScope:   "profile-error",
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{
					profiles:  []Profile{ProfileStandard},
					observers: Observers{Receipt: recorder.observe},
				})
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "profile-error"})
				return service, state, &canonical.ChatRequest{System: inboundReceiptProtectedCanary}
			},
		},
		{
			name:                 "maximal multibyte invalid scope",
			code:                 CodeRequestInvalid,
			stage:                "scope",
			wantProfile:          ProfileStrict,
			forbiddenScopeCanary: "🔒",
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{observers: Observers{Receipt: recorder.observe}})
				scopeID := strings.Repeat("🔒", maxScopeLength)
				if utf8.RuneCountInString(scopeID) != maxScopeLength || len(scopeID) != maxScopeLength*4 {
					t.Fatalf("invalid scope fixture has %d runes and %d bytes", utf8.RuneCountInString(scopeID), len(scopeID))
				}
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scopeID})
				return service, state, &canonical.ChatRequest{System: inboundReceiptProtectedCanary}
			},
		},
		{
			name:                 "invalid scope delimiter",
			code:                 CodeRequestInvalid,
			stage:                "scope",
			wantProfile:          ProfileStrict,
			forbiddenScopeCanary: "invalid-delimiter-canary",
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{observers: Observers{Receipt: recorder.observe}})
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "invalid-delimiter-canary/scope"})
				return service, state, &canonical.ChatRequest{System: inboundReceiptProtectedCanary}
			},
		},
		{
			name:                 "invalid scope control",
			code:                 CodeRequestInvalid,
			stage:                "scope",
			wantProfile:          ProfileStrict,
			forbiddenScopeCanary: "invalid-control-canary",
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{observers: Observers{Receipt: recorder.observe}})
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "invalid-control-canary\n"})
				return service, state, &canonical.ChatRequest{System: inboundReceiptProtectedCanary}
			},
		},
		{
			name:        "closed scope",
			code:        CodeScopeClosed,
			stage:       "scope",
			wantProfile: ProfileStrict,
			wantScope:   "closed-receipt-scope",
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{observers: Observers{Receipt: recorder.observe}})
				seed := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "closed-receipt-scope"})
				if _, err := service.Before(WithRequestState(context.Background(), seed), &canonical.ChatRequest{}); err != nil {
					t.Fatalf("seed Before: %v", err)
				}
				seed.releaseLease()
				if result, err := service.store.Clear("closed-receipt-scope"); err != nil || result != ClearCompleted {
					t.Fatalf("Clear=(%q,%v), want completed", result, err)
				}
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "closed-receipt-scope"})
				return service, state, &canonical.ChatRequest{System: inboundReceiptProtectedCanary}
			},
		},
		{
			name:        "scope capacity exhaustion",
			code:        CodeCapacityExceeded,
			stage:       "scope",
			wantProfile: ProfileStrict,
			wantScope:   "capacity-receipt-scope",
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{
					maxScopes: 1,
					observers: Observers{Receipt: recorder.observe},
				})
				lease, err := service.store.Acquire("occupied-receipt-scope", ProfileStrict)
				if err != nil {
					t.Fatalf("occupy scope: %v", err)
				}
				t.Cleanup(lease.Release)
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "capacity-receipt-scope"})
				return service, state, &canonical.ChatRequest{System: inboundReceiptProtectedCanary}
			},
		},
		{
			name:        "valid maximum ASCII scope on non-panic mapper internal failure",
			code:        CodeInternalError,
			stage:       "mapping",
			wantProfile: ProfileStrict,
			wantScope:   strings.Repeat("a", maxScopeLength),
			setup: func(t *testing.T, recorder *receiptObserverRecorder) (*Service, *RequestState, *canonical.ChatRequest) {
				service := newStrictTestService(t, strictTestConfig{
					classifier: strictFixtureClassifier{},
					observers:  Observers{Receipt: recorder.observe},
				})
				service.mapper = failingTechnicalMapper{err: errors.New(inboundReceiptProtectedCanary)}
				state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: strings.Repeat("a", maxScopeLength)})
				return service, state, &canonical.ChatRequest{System: "10.20.30.40"}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &receiptObserverRecorder{}
			service, state, req := tc.setup(t, recorder)
			_, err := service.Before(WithRequestState(context.Background(), state), req)
			assertPrivacyError(t, err, tc.code, tc.stage)
			if strings.Contains(err.Error(), inboundReceiptProtectedCanary) {
				t.Fatalf("typed error leaked protected canary: %v", err)
			}

			receipt, payload := decodeStateReceipt(t, state)
			if receipt != (Receipt{
				Version: 1, Profile: tc.wantProfile, Scope: tc.wantScope,
				Coverage: "input", Result: "error",
			}) {
				t.Fatalf("receipt=%+v", receipt)
			}
			if len(state.receiptValue()) > maxEncodedReceiptBytes {
				t.Fatalf("encoded receipt bytes=%d, want <=%d", len(state.receiptValue()), maxEncodedReceiptBytes)
			}
			for _, forbidden := range []string{inboundReceiptProtectedCanary, "IPv4", "[SECRET:", "token"} {
				if strings.Contains(payload, forbidden) {
					t.Fatalf("receipt leaked %q: %s", forbidden, payload)
				}
			}
			if tc.forbiddenScopeCanary != "" && strings.Contains(payload, tc.forbiddenScopeCanary) {
				t.Fatalf("receipt leaked invalid scope canary %q: %s", tc.forbiddenScopeCanary, payload)
			}
			if got := recorder.snapshot(); len(got) != 1 || got[0] != (receiptObservation{profile: tc.wantProfile, result: "error"}) {
				t.Fatalf("receipt observer events=%+v, want one error", got)
			}
			if service.store != nil {
				if got := service.store.Snapshot().RequestsInFlight; got != 0 && tc.name != "scope capacity exhaustion" {
					t.Fatalf("failure retained %d leases", got)
				}
			}
		})
	}
}

func TestServiceStrict_PassReceiptsRetainAcceptedAndGeneratedScopes(t *testing.T) {
	tests := []struct {
		name    string
		scopeID string
	}{
		{name: "accepted maximum ASCII", scopeID: strings.Repeat("a", maxScopeLength)},
		{name: "generated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: tc.scopeID})
			ctx := WithRequestState(context.Background(), state)
			req := &canonical.ChatRequest{}
			if _, err := service.Before(ctx, req); err != nil {
				t.Fatalf("Before: %v", err)
			}
			acceptedScope := state.Metadata().ScopeID
			if tc.scopeID != "" && acceptedScope != tc.scopeID {
				t.Fatalf("accepted scope=%q, want %q", acceptedScope, tc.scopeID)
			}
			if tc.scopeID == "" && !regexp.MustCompile(`^req-[a-z2-7]+$`).MatchString(acceptedScope) {
				t.Fatalf("generated scope=%q, want req-<base32>", acceptedScope)
			}
			if err := service.After(ctx, req, &canonical.ChatResponse{}); err != nil {
				t.Fatalf("After: %v", err)
			}
			receipt, _ := decodeStateReceipt(t, state)
			if receipt != (Receipt{
				Version: 1, Profile: ProfileStrict, Scope: acceptedScope,
				Coverage: "full", Result: "pass",
			}) {
				t.Fatalf("receipt=%+v", receipt)
			}
			if len(state.receiptValue()) > maxEncodedReceiptBytes {
				t.Fatalf("encoded receipt bytes=%d, want <=%d", len(state.receiptValue()), maxEncodedReceiptBytes)
			}
		})
	}
}

func TestServiceBefore_ErrorReceiptFinalizerPreservesBlockAndPanic(t *testing.T) {
	tests := []struct {
		name       string
		options    strictTestConfig
		req        *canonical.ChatRequest
		code       string
		wantResult string
	}{
		{
			name: "block", req: &canonical.ChatRequest{System: "[SECRET:API_KEY_0123456789AB]"},
			code: CodeInputBlocked, wantResult: "block",
		},
		{
			name: "panic", options: strictTestConfig{
				classifier: panickingPrivacyClassifier{payload: inboundReceiptProtectedCanary},
			},
			req: &canonical.ChatRequest{System: "panic"}, code: CodeInternalError, wantResult: "error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &receiptObserverRecorder{}
			tc.options.observers.Receipt = recorder.observe
			service := newStrictTestService(t, tc.options)
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "preserve-" + tc.name})
			_, err := service.Before(WithRequestState(context.Background(), state), tc.req)
			assertPrivacyError(t, err, tc.code, "input")
			receipt, payload := decodeStateReceipt(t, state)
			if receipt.Profile != ProfileStrict || receipt.Coverage != "input" || receipt.Result != tc.wantResult {
				t.Fatalf("receipt=%+v", receipt)
			}
			if strings.Contains(payload, inboundReceiptProtectedCanary) {
				t.Fatalf("receipt leaked protected canary: %s", payload)
			}
			if got := recorder.snapshot(); len(got) != 1 || got[0].result != tc.wantResult {
				t.Fatalf("receipt observer events=%+v, want one %q", got, tc.wantResult)
			}
		})
	}
}

func TestServiceBefore_ErrorReceiptObserverPanicPreservesOriginalTypedError(t *testing.T) {
	calls := 0
	service := newStrictTestService(t, strictTestConfig{
		profiles: []Profile{ProfileStandard},
		observers: Observers{Receipt: func(Profile, string) {
			calls++
			panic(inboundReceiptProtectedCanary)
		}},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "observer-error"})
	_, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{})
	assertPrivacyError(t, err, CodeProfileUnavailable, "profile")
	if calls != 1 {
		t.Fatalf("receipt observer calls=%d, want 1", calls)
	}
	receipt, payload := decodeStateReceipt(t, state)
	if receipt.Profile != ProfileStandard || receipt.Coverage != "input" || receipt.Result != "error" {
		t.Fatalf("receipt=%+v", receipt)
	}
	if strings.Contains(err.Error(), inboundReceiptProtectedCanary) || strings.Contains(payload, inboundReceiptProtectedCanary) {
		t.Fatalf("observer panic payload leaked: err=%v receipt=%s", err, payload)
	}
}

func TestServiceBefore_BlockReceiptObserverPanicPreservesInputBlocked(t *testing.T) {
	calls := 0
	service := newStrictTestService(t, strictTestConfig{
		observers: Observers{Receipt: func(Profile, string) {
			calls++
			panic(inboundReceiptProtectedCanary)
		}},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "block-observer-error"})
	_, err := service.Before(
		WithRequestState(context.Background(), state),
		&canonical.ChatRequest{System: "[SECRET:API_KEY_0123456789AB]"},
	)
	assertPrivacyError(t, err, CodeInputBlocked, "input")
	if calls != 1 {
		t.Fatalf("receipt observer calls=%d, want 1", calls)
	}
	receipt, payload := decodeStateReceipt(t, state)
	if receipt.Profile != ProfileStrict || receipt.Coverage != "input" || receipt.Result != "block" {
		t.Fatalf("receipt=%+v", receipt)
	}
	if strings.Contains(err.Error(), inboundReceiptProtectedCanary) || strings.Contains(payload, inboundReceiptProtectedCanary) {
		t.Fatalf("observer panic payload leaked: err=%v receipt=%s", err, payload)
	}
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

func TestServiceStrict_InboundCredentialAssignmentsRemainOneWay(t *testing.T) {
	tests := []struct {
		name, scope, original, input, labelPrefix string
	}{
		{
			name: "JSON password", scope: "credential-json-password", original: "task17-json-password",
			input: `{"password":"task17-json-password"}`, labelPrefix: `{"password":"[SECRET:PASSWORD_`,
		},
		{
			name: "YAML client secret", scope: "credential-yaml-secret", original: "task17-yaml-client-secret",
			input: "client_secret: task17-yaml-client-secret", labelPrefix: "client_secret: [SECRET:CLIENT_SECRET_",
		},
		{
			name: "dotenv refresh token", scope: "credential-dotenv-token", original: "task17-dotenv-refresh-token",
			input: "REFRESH_TOKEN=task17-dotenv-refresh-token", labelPrefix: "REFRESH_TOKEN=[SECRET:REFRESH_TOKEN_",
		},
		{
			name: "CLI access token", scope: "credential-cli-token", original: "task17-cli-access-token",
			input: "--access-token=task17-cli-access-token", labelPrefix: "--access-token=[SECRET:ACCESS_TOKEN_",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			residualEntity := ""
			service := newStrictTestService(t, strictTestConfig{
				secret: NewSecretClassifier(),
				observers: Observers{Residual: func(_ Profile, _, entity string) {
					residualEntity = entity
				}},
			})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: tc.scope})
			req := &canonical.ChatRequest{System: tc.input}

			if _, err := service.Before(WithRequestState(context.Background(), state), req); err != nil {
				t.Fatalf("Before: %v; transformed=%q residual_entity=%q", err, req.System, residualEntity)
			}
			t.Cleanup(state.releaseLease)
			if strings.Contains(req.System, tc.original) || !strings.HasPrefix(req.System, tc.labelPrefix) {
				t.Fatalf("transformed assignment=%q, want one-way prefix %q without original", req.System, tc.labelPrefix)
			}
			entries, err := service.store.Inspect(tc.scope)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("credential entered reversible ledger: %+v", entries)
			}
		})
	}
}

func TestServiceStrict_ResidualRejectsForgedCredentialAssignmentMarker(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{secret: NewSecretClassifier()})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "credential-forged-assignment"})
	req := &canonical.ChatRequest{System: `{"password":"[SECRET:PASSWORD_0123456789AB]"}`}

	_, err := service.Before(WithRequestState(context.Background(), state), req)
	assertPrivacyError(t, err, CodeInputBlocked, "input")
	if req.System != `{"password":"[SECRET:PASSWORD_0123456789AB]"}` {
		t.Fatalf("forged marker was rewritten before residual verification: %q", req.System)
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
		name        string
		input       string
		classifier  *residualPassClassifier
		recognizers []string
		wantAfter   string
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
			name:  "malformed secret token",
			input: "forged [SECRET:unterminated",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [SECRET:unterminated",
		},
		{
			name:  "malformed replace token suffix",
			input: "forged [PERSON_1_extra]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PERSON_1_extra]",
		},
		{
			name:  "unterminated replace token",
			input: "forged [PERSON_1",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PERSON_1",
		},
		{
			name:  "reserved entity without counter",
			input: "forged [PERSON]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PERSON]",
		},
		{
			name:  "reserved namespace without delimiter",
			input: "forged [SECRET]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [SECRET]",
		},
		{
			name:  "malformed hash token length",
			input: "forged [IPv4:h-123456789]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [IPv4:h-123456789]",
		},
		{
			name:  "secret namespace bang delimiter",
			input: "forged [SECRET!forged]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [SECRET!forged]",
		},
		{
			name:  "pii namespace dash delimiter",
			input: "forged [PII-forged]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PII-forged]",
		},
		{
			name:  "personal entity slash delimiter",
			input: "forged [PERSON/1]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PERSON/1]",
		},
		{
			name:  "personal entity whitespace delimiter",
			input: "forged [PERSON 1]",
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PERSON 1]",
		},
		{
			name:        "configured technical entity question delimiter",
			input:       "forged [PRIVATE_IP?forged]",
			recognizers: []string{"PRIVATE_IP"},
			classifier: &residualPassClassifier{
				first: func(string) []Finding { return nil }, residual: func(string) []Finding { return nil },
			},
			wantAfter: "forged [PRIVATE_IP?forged]",
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
			service := newStrictTestService(t, strictTestConfig{classifier: tc.classifier, recognizers: tc.recognizers})
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

func TestServiceStrict_ResidualOccurrenceAllowsMultipleGeneratedOpaqueArtifacts(t *testing.T) {
	const scope = "occurrence-multiple-opaque"
	first := "sk-" + strings.Repeat("A", 40)
	second := "sk-" + strings.Repeat("B", 32)
	service := newStrictTestService(t, strictTestConfig{secret: NewSecretClassifier()})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
	req := &canonical.ChatRequest{System: "first " + first + " middle " + second + " last"}

	if _, err := service.Before(WithRequestState(context.Background(), state), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	defer state.releaseLease()

	firstLabel := OneWaySecretLabel(
		[]byte("strict-test-alias-key"),
		secretHMACDomain+"\x00"+scope,
		"OPENAI_API_KEY",
		first,
	)
	secondLabel := OneWaySecretLabel(
		[]byte("strict-test-alias-key"),
		secretHMACDomain+"\x00"+scope,
		"OPENAI_API_KEY",
		second,
	)
	want := "first " + firstLabel + " middle " + secondLabel + " last"
	if req.System != want {
		t.Fatalf("transformed=%q, want %q", req.System, want)
	}
}

func TestServiceStrict_ResidualAllowsUnrelatedBracketedProse(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{recognizers: []string{"PRIVATE_IP"}})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "unrelated-bracketed-prose"})
	req := &canonical.ChatRequest{System: "[PERSONAL note] [SECRETARY!ordinary] [PIIless-forged?] [PRIVATE_IP_RANGE? ordinary]"}

	if _, err := service.Before(WithRequestState(context.Background(), state), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	defer state.releaseLease()
}

type exactSecretClassifier struct {
	original string
	entity   string
	inject   *Finding
}

func (c exactSecretClassifier) Classify(_ string, value string) []Finding {
	if strings.Contains(value, c.original) {
		return exactFinding(value, c.original, c.entity, CategorySecret, MatchHighConfidenceSecret)
	}
	if c.inject != nil && strings.HasPrefix(value, "[SECRET:") {
		finding := *c.inject
		return []Finding{finding}
	}
	return nil
}

func TestServiceStrict_ResidualOccurrenceAuthorizationRejectsDuplicateCallerToken(t *testing.T) {
	const (
		scope    = "occurrence-duplicate"
		original = "raw-secret-for-occurrence"
		entity   = "API_KEY"
	)
	label := OneWaySecretLabel(
		[]byte("strict-test-alias-key"),
		secretHMACDomain+"\x00"+scope,
		entity,
		original,
	)
	service := newStrictTestService(t, strictTestConfig{
		classifier: exactSecretClassifier{original: original, entity: entity},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
	req := &canonical.ChatRequest{
		System: original,
		Messages: []canonical.Message{{Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: label,
		}}}},
	}
	_, err := service.Before(WithRequestState(context.Background(), state), req)
	assertPrivacyError(t, err, CodeInputBlocked, "input")
	if req.System != label || req.Messages[0].Content[0].Text != label {
		t.Fatalf("unexpected transformed values: system=%q message=%q", req.System, req.Messages[0].Content[0].Text)
	}
	if got := service.store.Snapshot().RequestsInFlight; got != 0 {
		t.Fatalf("duplicate-token block retained %d leases", got)
	}
}

func TestServiceStrict_ResidualOccurrenceAuthorizationRejectsTrailingTokenSyntax(t *testing.T) {
	const (
		scope    = "occurrence-trailing"
		original = "raw-secret-before-trailer"
		entity   = "API_KEY"
	)
	label := OneWaySecretLabel(
		[]byte("strict-test-alias-key"),
		secretHMACDomain+"\x00"+scope,
		entity,
		original,
	)
	service := newStrictTestService(t, strictTestConfig{
		classifier: exactSecretClassifier{original: original, entity: entity},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
	req := &canonical.ChatRequest{System: original + "_extra]"}
	_, err := service.Before(WithRequestState(context.Background(), state), req)
	assertPrivacyError(t, err, CodeInputBlocked, "input")
	if req.System != label+"_extra]" {
		t.Fatalf("transformed trailing token=%q, want %q", req.System, label+"_extra]")
	}
}

func TestServiceStrict_ResidualOccurrenceAuthorizationRejectsInjectedFindings(t *testing.T) {
	const original = "raw-secret-for-injected-finding"
	tests := []struct {
		name     string
		entity   string
		category Category
		kind     MatchKind
	}{
		{name: "personal", entity: "PERSON", category: CategoryPersonal, kind: MatchNER},
		{name: "technical", entity: "IPv4", category: CategoryTechnical, kind: MatchValidatedRegex},
		{name: "secret", entity: "API_KEY", category: CategorySecret, kind: MatchHighConfidenceSecret},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{
				classifier: exactSecretClassifier{
					original: original,
					entity:   "API_KEY",
					inject: &Finding{
						Entity: tc.entity, Category: tc.category, Kind: tc.kind,
						Start: len("[SECRET:"), End: len("[SECRET:") + 3,
					},
				},
			})
			state := NewRequestState(RequestMetadata{
				RequestedProfile: "strict",
				ScopeID:          "occurrence-injected-" + tc.name,
			})
			req := &canonical.ChatRequest{System: original}
			_, err := service.Before(WithRequestState(context.Background(), state), req)
			assertPrivacyError(t, err, CodeInputBlocked, "input")
			if !strings.HasPrefix(req.System, "[SECRET:API_KEY_") {
				t.Fatalf("secret was not transformed before injected residual: %q", req.System)
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
	recognizers     []string
	secretAction    Action
	technicalAction Action
	maxScopes       int
	maxEntries      int
	maxTotalEntries int
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
	if options.maxScopes == 0 {
		options.maxScopes = 32
	}
	if options.maxEntries == 0 {
		options.maxEntries = 128
	}
	if options.maxTotalEntries == 0 {
		options.maxTotalEntries = 1024
	}
	service, err := NewService(Config{
		DefaultProfile:     options.defaultProfile,
		RequestProfiles:    options.profiles,
		AliasKey:           []byte("strict-test-alias-key"),
		SecretAction:       options.secretAction,
		TechnicalAction:    options.technicalAction,
		ScopeTTL:           time.Hour,
		MaxScopes:          options.maxScopes,
		MaxEntriesPerScope: options.maxEntries,
		MaxTotalEntries:    options.maxTotalEntries,
		PIIEnabled:         true,
		PIIMode:            options.piiMode,
		PIIHashKey:         []byte("strict-test-hash-key"),
		PIIEncryptKey:      mustEncryptionKey(t, "strict-test-encryption-key"),
		PIIEntityActions:   options.entityActions,
		Recognizers:        options.recognizers,
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
