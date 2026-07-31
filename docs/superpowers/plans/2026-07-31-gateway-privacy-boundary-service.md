# Gateway Privacy Boundary Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current isolated PII hook internals with one profile-aware, scoped, fail-closed privacy boundary while preserving standard-mode compatibility across Ollama, OpenAI, and Anthropic.

**Architecture:** Add a leaf `internal/privacy` service that owns profiles, classifiers, transformations, scoped mappings, receipts, lifecycle, and safe telemetry. Keep `PIIRedactionHook` as the compatibility-facing Pre/Post adapter; inject narrow projections into admin, metrics, chat trace, diagnostics, and wrappers. Compression runs before privacy, and each HTTP adapter stamps the same request metadata and renders the same typed privacy failures in its native wire format.

**Tech Stack:** Go 1.26.5, `net/netip`, HMAC-SHA256, AES-256-GCM, chi v5, Prometheus client_golang, Go race detector, Bash 3.2, PowerShell 5.1+, Node test runner, Python 3 Grafana generator.

## Global Constraints

- The approved design in `docs/superpowers/specs/2026-07-31-privacy-boundary-service-design.md` is authoritative.
- Strict TDD is mandatory for every behavior change: write one focused failing test, run it and record the expected failure, implement the smallest passing behavior, run the focused test, then refactor.
- Preserve `PIIRedactionHook` as the registered `ENABLED_HOOKS` name and `/health/hooks` row.
- Preserve current `standard` response bodies, current default PII enablement, and all existing `PII_*` configuration names.
- Never persist scope mappings. Never expose originals or aliases through logs, metrics, receipts, health, dashboard, support bundles, or ordinary errors.
- `internal/privacy` may depend on `internal/canonical` only. It must not import adapters, admin, config, engine, metrics, plugin, server, or scripts.
- Classification and transformation must not hold a Gateway-wide mutex. A brief store-index lock is allowed for acquire/create/reap; mapping operations lock only one scope.
- Strict output is completely buffered and validated before any HTTP headers or body bytes are released. A client that requested streaming receives native synthetic replay only after full validation.
- The privacy boundary is the last content-mutating inbound hook: RequestID → Auth → JSON steering → Compression → Privacy → Logging → worker.
- Exact synthetic ranges are fixed here: IPv4 uses the RFC 2544 benchmarking block `198.18.0.0/15`; IPv6 uses the RFC 3849 documentation prefix `2001:db8::/32`; SIP aliases use the RFC 2606 `.invalid` top-level domain.
- Run `git diff --check` before every task commit. Do not combine task commits.

---

## File Structure

### New privacy core

- `internal/privacy/types.go` — profiles, actions, categories, findings, provenance, safe snapshots.
- `internal/privacy/context.go` — per-request metadata/state and receipt handoff.
- `internal/privacy/classifier.go` — classifier interface and deterministic span arbitration.
- `internal/privacy/secrets.go` — shared high-confidence credential classifier/redactor.
- `internal/privacy/walk.go` — bounded key-aware canonical and structured-value traversal.
- `internal/privacy/actions.go` — standard replace/mask/hash/drop/encrypt behavior.
- `internal/privacy/receipt.go` — bounded receipt construction and base64url encoding.
- `internal/privacy/errors.go` — typed bounded privacy failures.
- `internal/privacy/store.go` — scoped ledger, leases, capacity, expiry, clear, inspection.
- `internal/privacy/technical.go` — format-preserving technical aliases.
- `internal/privacy/service.go` — standard and strict inbound/outbound orchestration.
- `internal/privacy/*_test.go`, `testmain_test.go`, and `testdata/secret-cases.json` — unit, race, leakage, and fixture coverage.

### Compatibility and HTTP integration

- `internal/plugin/pii/*.go` — PII classifier adapter plus thin compatibility hook/wrappers.
- `internal/plugin/trace.go` — strict-safe metadata-only chat traces.
- `internal/adapter/{ollama,openai,anthropic}` — request metadata, receipts, strict buffering, native errors.
- `cmd/otto-gateway/main.go` — service lifecycle, chain ordering, observer wiring, admin adapters.
- `.go-arch-lint.yml` — privacy component and permitted leaf dependencies.

### Operations

- `internal/metrics/metrics.go` — privacy counters/histograms and pull gauges.
- `internal/admin/privacy.go`, snapshot/templates/static assets — triage, status, About, dashboard, docs.
- `scripts/gw`, `scripts/gw.ps1`, `scripts/.env.example`, installer/wrapper tests — managed keys and privacy CLI.
- `scripts/lib/redact.*` and support tests — fail-closed use of the shared Go credential redactor.
- `docs/operating.md`, `docs/operator-quickstart.md`, `docs/INSTALL.md`, `README.md`, `docs/grafana/*`, and `docs/releases/2026-07-31-privacy-boundary.md` — operator, workflow, reporting, and release guidance.

## Locked Core Interfaces

These signatures are the cross-task contract. A later task may add unexported helpers but must not silently redesign these exported seams.

~~~go
package privacy

type Profile string
const (
	ProfileStandard Profile = "standard"
	ProfileStrict   Profile = "strict"
)

type Action string
const (
	ActionReplace      Action = "replace"
	ActionMask         Action = "mask"
	ActionHash         Action = "hash"
	ActionDrop         Action = "drop"
	ActionEncrypt      Action = "encrypt"
	ActionPseudonymize Action = "pseudonymize"
)

type Category string
const (
	CategorySecret    Category = "secret"
	CategoryTechnical Category = "technical"
	CategoryPersonal  Category = "personal"
)

type Finding struct {
	Entity        string
	Category      Category
	Kind          MatchKind
	Start, End    int
	RegistryOrder int
}

type Classifier interface {
	Classify(key, value string) []Finding
}

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

type RequestMetadata struct {
	RequestedProfile string
	ScopeID          string
	Surface          string
	Workload         string
}

func NewRequestState(meta RequestMetadata) *RequestState
func WithRequestState(context.Context, *RequestState) context.Context
func StateFromContext(context.Context) (*RequestState, bool)
func StampHTTPContext(context.Context, http.Header, string) (context.Context, *RequestState)
func SetReceiptHeader(http.ResponseWriter, context.Context) bool

type Error struct {
	Code  string
	Stage string
	Cause error
}
func (e *Error) Error() string
func (e *Error) Unwrap() error
func ErrorInfo(error) (status int, code string, ok bool)

type Config struct {
	DefaultProfile      Profile
	RequestProfiles     []Profile
	AliasKey            []byte
	SecretAction        Action
	TechnicalAction     Action
	ScopeTTL            time.Duration
	MaxScopes           int
	MaxEntriesPerScope  int
	MaxTotalEntries     int
	PIIEnabled          bool
	PIIMode             Action
	PIIHashKey          []byte
	PIIEncryptKey       []byte
	PIIEntityActions    map[string]Action
	Recognizers         []string
	NEREnabled          bool
	Classifier          Classifier
	SecretClassifier    *SecretClassifier
	TriageEnabled       bool
	Clock                Clock
	Observers            Observers
}

type SafeSnapshot struct {
	DefaultProfile Profile
	RequestProfiles []Profile
	StrictAvailable bool
	SecretAction, TechnicalAction, PIIMode Action
	PIIEnabled, NEREnabled, AliasKeyPresent, TriageEnabled bool
	Recognizers []string
	EntityActions map[string]Action
	ScopeTTL time.Duration
	MaxScopes, MaxEntriesPerScope, MaxTotalEntries int
	ScopesActive, RequestsInFlight, Entries int
	OldestScopeAge time.Duration
	RequestsProtected, RequestsBlocked uint64
	LastErrorCode string
}

func NewService(Config) (*Service, error)
func (s *Service) Before(context.Context, *canonical.ChatRequest) (*canonical.ChatResponse, error)
func (s *Service) After(context.Context, *canonical.ChatRequest, *canonical.ChatResponse) error
func (s *Service) Describe() map[string]any
func (s *Service) Snapshot() SafeSnapshot
func (s *Service) Close()
~~~

`ErrorInfo` is an exact closed map: invalid request/profile syntax → `(400, privacy_request_invalid)`, unavailable profile → `(400, privacy_profile_unavailable)`, closed scope → `(409, privacy_scope_closed)`, unsafe input → `(422, privacy_input_blocked)`, unsafe output → `(502, privacy_output_blocked)`, exhausted scope/mapping capacity → `(503, privacy_capacity_exceeded)`, and internal privacy failure → `(503, privacy_internal_error)`.

### Task 1: Parse and validate the privacy configuration contract

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/plugin_config_test.go`
- Modify: `internal/config/testmain_test.go`
- Modify: `cmd/otto-gateway/testmain_test.go`

**Interfaces:**

~~~go
type Config struct {
	PrivacyDefaultProfile     string
	PrivacyRequestProfiles    []string
	PrivacyAliasKey           string
	PrivacySecretAction       string
	PrivacyTechnicalAction    string
	PrivacyScopeTTL           time.Duration
	PrivacyMaxScopes          int
	PrivacyMaxEntriesPerScope int
	PrivacyMaxTotalEntries    int
	PrivacyTriageEnabled      bool
	PrivacyTriageToken        string
}
~~~

Map these fields one-to-one from `PRIVACY_DEFAULT_PROFILE`, `PRIVACY_REQUEST_PROFILES`, `PRIVACY_ALIAS_KEY`, `PRIVACY_SECRET_ACTION`, `PRIVACY_TECHNICAL_ACTION`, `PRIVACY_SCOPE_TTL`, `PRIVACY_MAX_SCOPES`, `PRIVACY_MAX_ENTRIES_PER_SCOPE`, `PRIVACY_MAX_TOTAL_ENTRIES`, `PRIVACY_TRIAGE_ENABLED`, and `PRIVACY_TRIAGE_TOKEN`.

- [ ] **Step 1: RED — lock defaults, valid overrides, and NER alignment**

Add table-driven tests asserting the exact defaults: `standard`, `standard,strict`, `replace`, `pseudonymize`, `1h`, `128`, `4096`, `32768`, triage false, `PII_REDACTION_ENABLED=true`, `PII_REDACTION_MODE=encrypt`, and `PII_NER_ENABLED=true`. Set deterministic `PRIVACY_ALIAS_KEY` and `PRIVACY_TRIAGE_TOKEN` values in both TestMain functions.

~~~go
func TestLoad_PrivacyDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil { t.Fatal(err) }
	if cfg.PrivacyDefaultProfile != "standard" { t.Fatalf("default=%q", cfg.PrivacyDefaultProfile) }
	if diff := cmp.Diff([]string{"standard", "strict"}, cfg.PrivacyRequestProfiles); diff != "" { t.Fatal(diff) }
	if cfg.PrivacyScopeTTL != time.Hour || cfg.PrivacyMaxScopes != 128 || cfg.PrivacyMaxEntriesPerScope != 4096 || cfg.PrivacyMaxTotalEntries != 32768 { t.Fatalf("privacy defaults=%+v", cfg) }
	if !cfg.PIINEREnabled { t.Fatal("PII_NER_ENABLED compiled default must be true") }
}
~~~

Run: `go test ./internal/config -run 'Privacy|PIINEREnabled_Default' -count=1`
Expected: FAIL because the fields do not exist and NER defaults to false.

- [ ] **Step 2: GREEN — add fields and environment parsing**

Parse the ten `PRIVACY_*` keys next to existing PII settings. Use `time.ParseDuration` for TTL. Copy slices/maps before assigning them into `Config`.

~~~go
privacyTTL, err := time.ParseDuration(getEnvStr("PRIVACY_SCOPE_TTL", "1h"))
if err != nil || privacyTTL <= 0 {
	errs = append(errs, fmt.Errorf("PRIVACY_SCOPE_TTL: must be a positive Go duration"))
}
piiNEREnabled, err := getEnvBool("PII_NER_ENABLED", true)
~~~

Run: `go test ./internal/config -run 'Privacy|PIINEREnabled_Default' -count=1`
Expected: PASS.

- [ ] **Step 3: RED — lock fail-fast combinations**

Cover unknown/duplicate profiles, default not present in allowed profiles, secret actions outside `replace|drop`, technical actions outside `pseudonymize|drop`, missing alias key when strict is available, triage enabled without its token, zero/negative limits, per-scope limit greater than global, PII disabled while strict is available, and `pseudonymize` assigned to a non-technical entity.

~~~go
tests := []struct{ name, key, value, want string }{
	{"unknown profile", "PRIVACY_DEFAULT_PROFILE", "maximum", "PRIVACY_DEFAULT_PROFILE"},
	{"bad secret action", "PRIVACY_SECRET_ACTION", "encrypt", "PRIVACY_SECRET_ACTION"},
	{"bad ttl", "PRIVACY_SCOPE_TTL", "0s", "PRIVACY_SCOPE_TTL"},
	{"bad scope cap", "PRIVACY_MAX_SCOPES", "0", "PRIVACY_MAX_SCOPES"},
}
~~~

Run: `go test ./internal/config -run PrivacyInvalid -count=1`
Expected: FAIL because invalid combinations are accepted.

- [ ] **Step 4: GREEN — implement one validator and technical entity policy**

The technical entity set is exactly `IPv4`, `IPv6`, `SIP_URI`, `IMEI`, `IMSI`, `MSISDN`, `MAC_ADDRESS`, `COORDINATES`, and `SITE`. Extend `parsePIIEntityActions` to accept `pseudonymize` only for that set. Reject strict availability unless PII work is enabled and the alias key is non-empty.

Run: `go test ./internal/config -run 'Privacy|PIIEntityActions' -count=1`
Expected: PASS.

- [ ] **Step 5: Refactor, verify, and commit**

Run:

~~~bash
gofumpt -w internal/config/config.go internal/config/plugin_config_test.go internal/config/testmain_test.go cmd/otto-gateway/testmain_test.go
go test ./internal/config ./cmd/otto-gateway -run 'Privacy|PIINEREnabled_Default|PIIEntityActions' -count=1
go vet ./internal/config ./cmd/otto-gateway
git diff --check
git add internal/config/config.go internal/config/plugin_config_test.go internal/config/testmain_test.go cmd/otto-gateway/testmain_test.go
git commit -m "feat(config): add privacy profile and capacity contract"
~~~

### Task 2: Build the shared secret classifier and deterministic span arbiter

**Files:**
- Create: `internal/privacy/types.go`
- Create: `internal/privacy/classifier.go`
- Create: `internal/privacy/secrets.go`
- Create: `internal/privacy/walk.go`
- Create: `internal/privacy/classifier_test.go`
- Create: `internal/privacy/secrets_test.go`
- Create: `internal/privacy/walk_test.go`
- Create: `internal/privacy/testmain_test.go`
- Create: `internal/privacy/testdata/secret-cases.json`

**Interfaces:**

~~~go
type MatchKind uint8
const (
	MatchNER MatchKind = iota + 1
	MatchContextualTechnical
	MatchValidatedRegex
	MatchStructuredAssignment
	MatchHighConfidenceSecret
)

func Arbitrate([]Finding) []Finding
func NewSecretClassifier() *SecretClassifier
func (c *SecretClassifier) Classify(key, value string) []Finding
func (c *SecretClassifier) IsSecretKey(key string) bool
func (c *SecretClassifier) Redact(key, value string) string
func TransformStrings(v any, fn func(key, value string) (string, error)) (any, error)
func TransformRequestStrings(*canonical.ChatRequest, func(key, value string) (string, error)) error
func TransformResponseStrings(*canonical.ChatResponse, func(key, value string) (string, error)) error
func VisitRequestStrings(*canonical.ChatRequest, func(key, value string) error) error
func VisitResponseStrings(*canonical.ChatResponse, func(key, value string) error) error
~~~

- [ ] **Step 1: RED — create the shared credential corpus**

The JSON fixture must include positive and negative rows for authorization/proxy authorization, bearer/basic/OAuth tokens, AWS/GitHub/GitLab/OpenAI-style keys, access/refresh tokens, password/passphrase assignments, PEM/SSH/service-account private keys, credentialed database/service URLs, dotenv, JSON, YAML-like, header, and CLI forms. Include safe `keyboard`, `monkey`, `key_count`, `token_count`, `secretary`, public key, checksum, and ordinary prose cases.

~~~json
{"name":"dotenv client secret","key":"","value":"CLIENT_SECRET=s3cr3t-value","entities":["CLIENT_SECRET"]}
{"name":"generic key is safe","key":"key","value":"display-name","entities":[]}
{"name":"bearer header","key":"Authorization","value":"Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig","entities":["AUTHORIZATION"]}
~~~

Run: `go test ./internal/privacy -run SecretClassifier -count=1`
Expected: FAIL because the package does not exist.

- [ ] **Step 2: GREEN — implement high-confidence classification**

Normalize structured keys by splitting camelCase, snake_case, dash, and dot boundaries. A bare normalized word `key` is never sufficient; require a credential compound name or a credential-shaped value. Findings contain offsets only and never retain the source string.

~~~go
func (c *SecretClassifier) IsSecretKey(key string) bool {
	words := normalizeKeyWords(key)
	return containsCredentialCompound(words) || containsAuthorizationName(words)
}
~~~

Run: `go test ./internal/privacy -run SecretClassifier -count=1`
Expected: PASS for the full fixture.

- [ ] **Step 3: RED — lock overlap priority and one-pass rewriting**

Use overlapping fixtures such as a bearer token containing an email-like substring and a credentialed URL containing an IP. Assert secret > structured assignment > validated regex > contextual technical > NER, then longest span, then registry order.

~~~go
got := Arbitrate([]Finding{
	{Entity:"Email", Kind:MatchValidatedRegex, Start:7, End:20, RegistryOrder:3},
	{Entity:"AUTHORIZATION", Kind:MatchHighConfidenceSecret, Start:0, End:25, RegistryOrder:9},
})
if len(got) != 1 || got[0].Entity != "AUTHORIZATION" { t.Fatalf("got=%+v", got) }
~~~

Run: `go test ./internal/privacy -run Arbitrate -count=1`
Expected: FAIL because arbitration is absent.

- [ ] **Step 4: GREEN — implement stable arbitration and key-aware traversal**

Sort candidates by priority descending, span length descending, and registry order ascending; greedily accept non-overlapping spans, then sort accepted spans by start. `TransformStrings` recurses through `map[string]any` and `[]any`, passes map keys to the callback, never transforms map keys, and fails closed beyond depth 64 instead of returning an unvisited subtree. The canonical walkers explicitly cover system text, every message text part, tool-use inputs, tool-result content, response text, and returned tool-call arguments. Visit functions never mutate and use a separate traversal from transform functions.

Run: `go test ./internal/privacy -run 'Arbitrate|TransformStrings' -count=1`
Expected: PASS.

- [ ] **Step 5: Refactor, race-test, and commit**

~~~bash
gofumpt -w internal/privacy
go test -race ./internal/privacy -run 'SecretClassifier|Arbitrate|TransformStrings' -count=1
go vet ./internal/privacy
git diff --check
git add internal/privacy
git commit -m "feat(privacy): add shared credential classifier"
~~~

### Task 3: Implement the bounded scoped mapping store

**Files:**
- Create: `internal/privacy/store.go`
- Create: `internal/privacy/store_test.go`
- Create: `internal/privacy/store_race_test.go`

**Interfaces:**

~~~go
type Ticker interface {
	C() <-chan time.Time
	Stop()
}
type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}
type Provenance string
const (
	ProvenanceInput     Provenance = "input"
	ProvenanceGenerated Provenance = "generated"
)
type StoreConfig struct {
	TTL                time.Duration
	MaxScopes          int
	MaxEntriesPerScope int
	MaxTotalEntries    int
}
type MappingEntry struct {
	Entity, Original, Synthetic string
	Provenance                  Provenance
	CreatedAt                   time.Time
}
type ClearResult string
const (
	ClearCompleted ClearResult = "completed"
	ClearClosing   ClearResult = "closing"
)
type ScopeInfo struct {
	ID string
	Profile Profile
	State string
	Entries, InFlight int
	CreatedAt, LastUsedAt, ExpiresAt time.Time
}
type ClearSummary struct { Completed, Closing int }
type StoreSnapshot struct {
	ScopesActive, RequestsInFlight, Entries int
	MaxScopes, MaxEntriesPerScope, MaxTotalEntries int
	OldestScopeAge time.Duration
}

func NewScopeStore(StoreConfig, Clock) (*ScopeStore, error)
func (s *ScopeStore) Acquire(scopeID string, profile Profile) (*ScopeLease, error)
func (l *ScopeLease) GetOrCreate(entity, original string, provenance Provenance, candidate func(attempt uint32) (string, error)) (MappingEntry, bool, error)
func (l *ScopeLease) GetOrCreateRelation(key string, candidate func(attempt uint32) (string, error)) (string, bool, error)
func (l *ScopeLease) ResolveSynthetic(entity, synthetic string) (MappingEntry, bool)
func (l *ScopeLease) Release()
func (s *ScopeStore) List() []ScopeInfo
func (s *ScopeStore) Inspect(scopeID string) ([]MappingEntry, error)
func (s *ScopeStore) Clear(scopeID string) (ClearResult, error)
func (s *ScopeStore) ClearAll() ClearSummary
func (s *ScopeStore) ReapExpired() int
func (s *ScopeStore) Snapshot() StoreSnapshot
~~~

- [ ] **Step 1: RED — lock acquisition, stability, and capacity**

Use a fake clock. Assert same `(scope, entity, original)` returns one entry, cross-scope values are independent, successful use refreshes idle expiry, per-scope/global limits reject atomically, relation reservations consume the same bounded capacity as reversible entries, expired/closed scopes are reclaimed before rejecting new scope capacity, and no active scope is evicted.

Run: `go test ./internal/privacy -run ScopeStore -count=1`
Expected: FAIL because the store is absent.

- [ ] **Step 2: GREEN — implement short global locking and per-scope ledgers**

`ScopeStore.mu` protects only the scope index and lifecycle transitions. Each `scope.mu` protects that scope's forward/reverse/relation maps. Global entry usage is an `atomic.Int64`. Lock order is always store then scope; release unlocks the scope before asking the store to finalize a closed record.

~~~go
type scopeState struct {
	mu sync.Mutex
	forward map[string]MappingEntry
	reverse map[string]MappingEntry
	relations map[string]string
	inFlight int
	state lifecycleState
}
~~~

Run: `go test ./internal/privacy -run ScopeStore -count=1`
Expected: PASS.

- [ ] **Step 3: RED — lock clear/expiry race semantics**

Cover clear while one lease is active, new acquire after close, release-triggered wipe, bounded tombstone expiry/churn, expiry while active, and idempotent `Release`/`Clear`. Inspection must return a copy sorted by entity then synthetic value.

Run: `go test -race ./internal/privacy -run 'ScopeStore_(Clear|Expiry|Inspect)' -count=25`
Expected: FAIL on missing lifecycle semantics.

- [ ] **Step 4: GREEN — implement closing and tombstones**

Clear marks the record closing immediately. Active leases finish; the final release wipes maps, decrements global usage, and retains a no-value tombstone for at most one TTL. Tombstones use a FIFO ring capped at `max(128, 4*MaxScopes)`; deliberate churn may evict the oldest tombstone but can never grow memory without bound. Reap never removes an in-flight scope.

Run: `go test -race ./internal/privacy -run 'ScopeStore_(Clear|Expiry|Inspect)' -count=25`
Expected: PASS.

- [ ] **Step 5: RED/GREEN — prove unrelated scopes do not serialize**

Block a candidate callback in scope A, perform a complete insertion in scope B, and require B to finish before A is released. Then run 100 goroutines against one scope and 100 against distinct scopes; assert exact capacities and one same-value entry.

Run: `go test -race ./internal/privacy -run ScopeStore_Parallel -count=20`
Expected after GREEN: PASS with no races.

- [ ] **Step 6: Refactor and commit**

~~~bash
gofumpt -w internal/privacy/store.go internal/privacy/store_test.go internal/privacy/store_race_test.go
go test -race ./internal/privacy -run ScopeStore -count=1
git diff --check
git add internal/privacy/store.go internal/privacy/store_test.go internal/privacy/store_race_test.go
git commit -m "feat(privacy): add bounded scoped mapping store"
~~~

### Task 4: Implement format-preserving technical pseudonyms

**Files:**
- Create: `internal/privacy/technical.go`
- Create: `internal/privacy/technical_test.go`
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`

**Interfaces:**

~~~go
func NewTechnicalMapper(aliasKey []byte) (*TechnicalMapper, error)
func (m *TechnicalMapper) Map(lease *ScopeLease, entity, original string, provenance Provenance) (string, error)
~~~

- [ ] **Step 1: RED — lock IP, CIDR, and subnet behavior**

Extend IPv4/IPv6 recognition to include an optional CIDR suffix while retaining entity names `IPv4` and `IPv6`. Test these exact rules:

- IPv4 aliases live inside `198.18.0.0/15`.
- Standalone IPv4 values use their source `/24` as a relation group, map that group to a synthetic `/24`, and preserve the low host octet.
- Explicit IPv4 CIDRs preserve prefix and host offsets only for prefixes `/15` through `/32`; broader prefixes return an error.
- IPv6 aliases live inside `2001:db8::/32`.
- Standalone IPv6 values use source `/64` relation groups.
- Explicit IPv6 CIDRs preserve prefixes `/32` through `/128`; broader prefixes return an error.

Run: `go test ./internal/privacy ./internal/plugin/pii -run 'Technical_IP|CIDR' -count=1`
Expected: FAIL because the mapper and CIDR recognition are absent.

- [ ] **Step 2: GREEN — implement keyed group allocation and collision probing**

Derive candidates with domain-separated, length-prefixed `HMAC-SHA256(aliasKey, scope || entity || relation || attempt)` input so concatenation cannot alias distinct tuples. Use `GetOrCreateRelation` for subnet allocation and `GetOrCreate` for reversible entries. Collision probing increments `attempt`; exhaustion returns a typed capacity error and never weakens the requested relationship.

Run: `go test ./internal/privacy ./internal/plugin/pii -run 'Technical_IP|CIDR' -count=1`
Expected: PASS.

- [ ] **Step 3: RED — lock every remaining technical format**

Assert:

- MAC first octet has local bit 1 and multicast bit 0.
- IMEI is 15 digits and its final digit passes Luhn.
- IMSI remains 15 digits.
- MSISDN keeps `+`, digit count, and non-zero first digit.
- SIP keeps `sip`/`sips`, uses `u-<base32>@gw.invalid`, and maps an optional port into `49152..65535`.
- SITE preserves a recognized type prefix and emits `<TYPE>-SYN-<10 base32 chars>`.
- COORDINATES use one scope-derived 3D rotation of the unit sphere, remain valid latitude/longitude, preserve decimal precision and hemisphere notation, and preserve pairwise great-circle distance within `1e-6` radians.

Run: `go test ./internal/privacy -run Technical_Formats -count=1`
Expected: FAIL.

- [ ] **Step 4: GREEN — implement the exact formatters**

Use HMAC bytes, not `math/rand`. For coordinates, derive a normalized rotation axis and angle, apply Rodrigues' rotation matrix, and convert back to latitude/longitude. Do not use a flat additive offset because pole reflection would distort topology.

Run: `go test ./internal/privacy -run Technical_Formats -count=1`
Expected: PASS.

- [ ] **Step 5: RED/GREEN — lock stability, unlinkability, and generated provenance**

Test same-scope repeat stability, cross-scope inequality, collision retry, reverse lookup, input vs generated provenance, and that only input entries are restoration-eligible.

Run: `go test -race ./internal/privacy -run Technical -count=10`
Expected after GREEN: PASS.

- [ ] **Step 6: Refactor, cite ranges, and commit**

Add comments linking the range choices to `https://www.rfc-editor.org/rfc/rfc2544`, `https://www.rfc-editor.org/rfc/rfc3849`, and `.invalid` to `https://www.rfc-editor.org/rfc/rfc2606`.

~~~bash
gofumpt -w internal/privacy/technical.go internal/privacy/technical_test.go internal/plugin/pii/recognizers.go internal/plugin/pii/recognizers_test.go
go test -race ./internal/privacy ./internal/plugin/pii -run 'Technical|CIDR' -count=1
git diff --check
git add internal/privacy/technical.go internal/privacy/technical_test.go internal/plugin/pii/recognizers.go internal/plugin/pii/recognizers_test.go
git commit -m "feat(privacy): add scoped technical pseudonyms"
~~~

### Task 5: Move standard PII behavior behind `privacy.Service`

**Files:**
- Create: `internal/privacy/actions.go`
- Create: `internal/privacy/context.go`
- Create: `internal/privacy/receipt.go`
- Create: `internal/privacy/errors.go`
- Create: `internal/privacy/service.go`
- Create: `internal/privacy/actions_test.go`
- Create: `internal/privacy/context_test.go`
- Create: `internal/privacy/receipt_test.go`
- Create: `internal/privacy/service_standard_test.go`
- Modify: `internal/plugin/pii/pii.go`
- Modify: `internal/plugin/pii/ner.go`
- Modify: `internal/plugin/pii/modes.go`
- Modify: `internal/plugin/pii/encrypt.go`
- Modify: `internal/plugin/pii/walk.go`
- Modify: `internal/plugin/pii/summary.go`
- Modify: `internal/plugin/pii/pii_test.go`
- Modify: `internal/plugin/pii/modes_test.go`
- Modify: `internal/plugin/pii/encrypt_test.go`
- Modify: `internal/plugin/pii/walk_test.go`
- Modify: `internal/plugin/pii/summary_test.go`
- Modify: `internal/plugin/pii/ner_test.go`
- Modify: `internal/plugin/pii/contextual_test.go`

**Interfaces:** Use the locked core interfaces above, plus:

~~~go
func NewPIIClassifier(recognizers []Recognizer, enabled []string, nerEnabled bool) privacy.Classifier
func OneWaySecretLabel(aliasKey []byte, scopeID, entity, canonical string) string

type PIIRedactionHook struct { Service *privacy.Service }
func (h *PIIRedactionHook) Name() string
func (h *PIIRedactionHook) Describe() (string, map[string]any)
func (h *PIIRedactionHook) Before(context.Context, *canonical.ChatRequest) (*canonical.ChatResponse, error)
func (h *PIIRedactionHook) After(context.Context, *canonical.ChatRequest, *canonical.ChatResponse) error
~~~

- [ ] **Step 1: RED — pin standard compatibility before migration**

Add black-box fixtures for all canonical string locations, all five existing actions, entity overrides, counter identity, AES wrapped and bare-payload restoration, streaming downgrade, summary counts, and the full 16-regex-plus-NER inventory. For each fixture, record the current output before changing the hook.

Run: `go test ./internal/plugin/pii -run StandardCompatibility -count=1`
Expected: FAIL because the new compatibility suite and classifier constructor are absent.

- [ ] **Step 2: GREEN — implement context, receipts, errors, and actions**

`RequestState` owns only bounded metadata, counters, the acquired lease pointer, authorized inbound tokens, and the encoded receipt behind a mutex. Receipt JSON field order and values are fixed:

~~~go
type Receipt struct {
	Version     int     `json:"version"`
	Profile     Profile `json:"profile"`
	Scope       string  `json:"scope"`
	Coverage    string  `json:"coverage"`
	Result      string  `json:"result"`
	Transformed int     `json:"transformed"`
	Restored    int     `json:"restored"`
	Blocked     int     `json:"blocked"`
}
~~~

Use raw URL-safe base64 without padding and reject any encoded receipt over 512 bytes. `StampHTTPContext` extracts the two privacy headers plus workload from `X-GW-Skill` then `X-Flow-Name`, caps workload at 64 characters, and never trusts a surface value from the caller. Error strings contain only the stable code and stage.

Run: `go test ./internal/privacy -run 'Actions|Context|Receipt|Error' -count=1`
Expected: PASS.

- [ ] **Step 3: GREEN — make the PII package a classifier and compatibility adapter**

Move action/encryption/walk/summary authority into `internal/privacy`. Keep package-level PII functions only as thin aliases or wrappers needed by existing internal callers. `NewPIIClassifier` assigns technical vs personal categories and emits NER findings at lowest priority. `PIIRedactionHook` delegates to one non-nil service.

~~~go
func (h *PIIRedactionHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if h == nil || h.Service == nil { return nil, nil }
	return h.Service.Before(ctx, req)
}
~~~

Run: `go test ./internal/plugin/pii -run StandardCompatibility -count=1`
Expected: PASS with byte-equivalent standard outputs.

- [ ] **Step 4: RED/GREEN — lock standard receipt behavior**

With a stamped request state, standard inbound sets `profile=standard`, `coverage=input`, `result=pass`; aggregated standard output may upgrade to `coverage=full`. A true streaming response keeps `coverage=input`. No raw value or per-entity map appears in the encoded receipt.

Run: `go test ./internal/privacy -run ServiceStandard -count=1`
Expected after GREEN: PASS.

- [ ] **Step 5: Refactor, run legacy coverage, and commit**

~~~bash
gofumpt -w internal/privacy internal/plugin/pii
go test -race ./internal/privacy ./internal/plugin/pii -count=1
go test ./internal/plugin ./internal/adapter/ollama ./internal/adapter/openai ./internal/adapter/anthropic -count=1
git diff --check
git add internal/privacy internal/plugin/pii
git commit -m "refactor(privacy): route standard PII through service"
~~~

### Task 6: Enforce strict inbound privacy before worker dispatch

**Files:**
- Create: `internal/privacy/service_strict_inbound_test.go`
- Modify: `internal/privacy/service.go`
- Modify: `internal/privacy/context.go`
- Modify: `internal/privacy/errors.go`
- Modify: `internal/engine/engine_test.go`
- Modify: `internal/plugin/pii/pii_test.go`

**Interfaces:**

~~~go
func (s *Service) resolveProfile(*RequestState) (Profile, error)
func (s *Service) transformInbound(context.Context, *RequestState, *canonical.ChatRequest) error
~~~

- [ ] **Step 1: RED — lock profile, scope, and lifecycle decisions**

Test the global minimum/requested maximum rule: a request may raise `standard` to an allowed `strict` profile but may never lower a strict global default. Reject an unknown or unavailable profile with `privacy_profile_unavailable`. Accept caller scope IDs matching `[A-Za-z0-9._:-]{1,128}`; reject other non-empty IDs with `privacy_request_invalid`; generate a cryptographically random `req-<base32>` scope when the header is absent. A closed caller scope returns `privacy_scope_closed`.

Run: `go test ./internal/privacy -run 'ServiceStrict_(Profile|Scope)' -count=1`
Expected: FAIL because strict resolution and scope acquisition are absent.

- [ ] **Step 2: GREEN — resolve strict state and acquire one lease**

Resolve profile and scope before walking content. Store the lease on `RequestState`, make cleanup idempotent, and release it inside `Before` if any later inbound step fails. Set `req.Stream=false` for strict before returning to the engine.

Run: `go test ./internal/privacy -run 'ServiceStrict_(Profile|Scope)' -count=1`
Expected: PASS.

- [ ] **Step 3: RED — lock complete inbound transformation**

Cover every canonical string-bearing location, including structured tool arguments and extension key/value content. Assert deterministic overlap arbitration, one-way `[SECRET:<ENTITY>_<HMAC>]` labels with no secret ledger entry, valid technical pseudonyms, configured personal actions, and authorized AES token plus bare-payload registration. Assert transformed values are stable within a scope and unlinkable between scopes.

Run: `go test ./internal/privacy -run ServiceStrict_InboundTransform -count=1`
Expected: FAIL because the strict transform pipeline is absent.

- [ ] **Step 4: GREEN — classify and transform one bounded walk**

Run the secret classifier and PII classifier together, arbitrate once, and rewrite accepted spans from right to left. Secrets use the configured one-way `replace` or `drop` action and never enter the scope store; replacement labels use a domain-separated HMAC. An explicit compatible `PII_ENTITY_ACTIONS` entry remains the first authority for an existing PII entity. Without one, technical entities use `PRIVACY_TECHNICAL_ACTION` (`pseudonymize|drop`) and personal entities use `PII_REDACTION_MODE`. Only an effective `pseudonymize` action enters the technical mapper. Record only bounded counters and authorized generated tokens on request state.

Run: `go test ./internal/privacy -run ServiceStrict_InboundTransform -count=1`
Expected: PASS.

- [ ] **Step 5: RED/GREEN — prove the independent residual pass blocks dispatch**

After transformation, perform a fresh traversal and fresh classification of the transformed request. Do not reuse accepted spans or mutate while scanning. Return `privacy_input_blocked` when any protected original, unrecognized privacy token, invalid technical alias, or generated secret-like value remains. In the engine test, use a worker spy and assert its call count remains zero on the 422 path.

Run: `go test ./internal/privacy ./internal/engine -run 'ServiceStrict_Residual|PrivacyInputBlocked_NoDispatch' -count=1`
Expected after GREEN: PASS with zero worker calls.

- [ ] **Step 6: RED/GREEN — recover privacy panics as typed failures**

Inject panicking classifier, mapper, and observer fakes. The compatibility hook must recover at its existing engine boundary, release any acquired lease, set a bounded error receipt when state exists, and return `privacy_internal_error`; it must never dispatch or bypass strict processing. The panic value must not enter the returned error, receipt, metric labels, or log fields.

Run: `go test ./internal/privacy ./internal/plugin/pii ./internal/engine -run PrivacyPanic -count=1`
Expected after GREEN: PASS with no leaked lease and zero worker calls.

- [ ] **Step 7: Refactor, race-test, and commit**

~~~bash
gofumpt -w internal/privacy/service.go internal/privacy/context.go internal/privacy/errors.go internal/privacy/service_strict_inbound_test.go internal/engine/engine_test.go
go test -race ./internal/privacy ./internal/engine -run 'ServiceStrict|PrivacyInputBlocked' -count=5
git diff --check
git add internal/privacy internal/engine/engine_test.go
git commit -m "feat(privacy): enforce strict inbound boundary"
~~~

### Task 7: Validate strict output, restore input values, and issue receipts

**Files:**
- Create: `internal/privacy/service_strict_outbound_test.go`
- Create: `internal/privacy/service_lifecycle_test.go`
- Modify: `internal/privacy/service.go`
- Modify: `internal/privacy/context.go`
- Modify: `internal/privacy/receipt.go`
- Modify: `internal/privacy/store.go`

**Interfaces:**

~~~go
func (s *Service) transformOutbound(context.Context, *RequestState, *canonical.ChatResponse) error
func (s *Service) AllowSensitiveTrace(context.Context) bool
func (s *Service) TraceSummary(context.Context) map[string]any
~~~

- [ ] **Step 1: RED — distinguish input, generated, and unknown output values**

Test exact input aliases/tokens, generated technical values, generated personal values, secrets, malformed aliases, unknown aliases inside reserved synthetic ranges, and ordinary output. Only exact input-provenance entries and authorized inbound encryption tokens are restoration candidates. Generated technical entries remain pseudonymized with `ProvenanceGenerated`; generated personal values receive a one-way replacement; generated secrets cause `privacy_output_blocked`.

Run: `go test ./internal/privacy -run ServiceStrict_OutboundProvenance -count=1`
Expected: FAIL because strict output handling is absent.

- [ ] **Step 2: GREEN — validate aliases and transform generated output**

First validate worker output against the active lease and authorized token set. Transform newly generated protected values without making them restoration-eligible. Reject unknown values in privacy-reserved namespaces rather than passing them through. Do not restore caller originals yet.

Run: `go test ./internal/privacy -run ServiceStrict_OutboundProvenance -count=1`
Expected: PASS.

- [ ] **Step 3: RED/GREEN — scan independently, then selectively restore**

Freshly scan the fully transformed but not-yet-restored response. Block protected originals, secrets, unknown aliases, and invalid privacy tokens with `privacy_output_blocked`. Only after that independent pass succeeds, restore exact input-provenance mappings and authorized input encryption tokens. Finish with an authorization-only token/alias integrity sweep that permits those exact restorations but cannot classify-and-forgive a new raw value. Assert the response object is not made available to a writer on failure.

Run: `go test ./internal/privacy -run ServiceStrict_OutboundResidual -count=1`
Expected after GREEN: PASS.

- [ ] **Step 4: RED/GREEN — lock receipts for pass, block, and internal error**

Strict success produces `version=1`, `profile=strict`, `coverage=full`, and `result=pass`. Strict blocks use `result=block`; internal failures use `result=error`. Counters are aggregate integers only. Receipt encoding must remain at most 512 bytes and contain no entity names, originals, pseudonyms, tokens, keys, or failure details.

Run: `go test ./internal/privacy -run 'ServiceStrict_Receipt|Receipt_NoValues' -count=1`
Expected after GREEN: PASS.

- [ ] **Step 5: RED/GREEN — prove exactly-once cleanup under errors and clear races**

Cover nil response, worker error, post-hook error, concurrent clear, expiry during an active request, repeated `After`, shutdown during activity, and the service's expiry worker lifecycle. The reaper interval is `max(100ms, min(ScopeTTL/2, 1m))` and uses the injected ticker. The active request may finish after clear returns `closing`; no new request may acquire that scope; the final release wipes the mapping. `Close` must stop and join the reaper without retaining raw requests or leaking goroutines. Run each race case 50 times.

Run: `go test -race ./internal/privacy -run 'ServiceStrict_(Cleanup|ClearRace|ExpiryRace)' -count=50`
Expected after GREEN: PASS without negative counters or leaked leases.

- [ ] **Step 6: Refactor and commit**

~~~bash
gofumpt -w internal/privacy
go test -race ./internal/privacy -run 'ServiceStrict|Receipt' -count=5
git diff --check
git add internal/privacy
git commit -m "feat(privacy): enforce strict outbound boundary"
~~~

### Task 8: Wire one service into the hook chain and make traces profile-aware

**Files:**
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`
- Modify: `internal/plugin/trace.go`
- Modify: `internal/plugin/trace_test.go`
- Modify: `internal/plugin/pii/pii_test.go`
- Modify: `internal/engine/engine_test.go`
- Modify: `.go-arch-lint.yml`

**Interfaces:**

~~~go
type ChatTracePrivacy interface {
	AllowSensitiveTrace(context.Context) bool
	TraceSummary(context.Context) map[string]any
}

type ChatTraceHook struct {
	// existing fields remain
	Privacy ChatTracePrivacy
}
~~~

- [ ] **Step 1: RED — lock construction, ordering, and strict availability**

Test that main constructs exactly one privacy service, passes it to `PIIRedactionHook`, metrics, admin projections, and trace policy, and closes it during shutdown. Assert inbound order is ChatTrace observer when enabled, RequestID, Auth, JSON steering, Compression, PIIRedactionHook, Logging; ChatTrace never mutates content. Assert post order is PIIRedactionHook, Logging, then ChatTrace when enabled, so successful strict traces can see only validated state. After `ENABLED_HOOKS` filtering, startup must fail when strict is configured but `PIIRedactionHook` is missing.

Run: `go test ./cmd/otto-gateway -run 'PrivacyService|HookOrder|StrictRequiresHook' -count=1`
Expected: FAIL because main still constructs the legacy hook and compression follows PII.

- [ ] **Step 2: GREEN — build and inject one service**

Build the PII classifier from the 16 recognizers plus optional PERSON/LOCATION NER, build the shared secret classifier, validate service configuration, and register the compatibility hook under the unchanged name. Move compression before privacy. Update `.go-arch-lint.yml` so `privacy` imports only `canonical`, while adapters and `plugin_pii` may import `privacy`; admin receives narrow interfaces from main rather than importing privacy.

Run: `go test ./cmd/otto-gateway -run 'PrivacyService|HookOrder|StrictRequiresHook' -count=1`
Expected: PASS.

- [ ] **Step 3: RED — lock raw-trace policy**

Assert standard requests retain the current raw chat trace behavior. Strict, unresolved, blocked, or errored requests must emit only request ID, surface, workload, profile, coverage, result, and aggregate counts. No canonical request/response body or per-entity data may be included.

Run: `go test ./internal/plugin -run ChatTracePrivacy -count=1`
Expected: FAIL because traces are unconditionally raw.

- [ ] **Step 4: GREEN — delegate trace decisions to the service**

Have `ChatTraceHook` consult `Privacy` before serializing request or response content. Because its Pre observer remains first to preserve standard compatibility, the service must conservatively resolve the configured minimum and requested profile from stamped metadata without depending on `Service.Before` having run. Treat missing/invalid metadata as unsafe. Keep the existing raw payload shape byte-compatible only when `AllowSensitiveTrace` returns true.

Run: `go test ./internal/plugin -run 'ChatTracePrivacy|ChatTrace' -count=1`
Expected: PASS.

- [ ] **Step 5: Verify architecture, lifecycle, and commit**

~~~bash
gofumpt -w cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/plugin/trace.go internal/plugin/trace_test.go internal/plugin/pii/pii_test.go internal/engine/engine_test.go
go test -race ./cmd/otto-gateway ./internal/plugin ./internal/engine -run 'Privacy|HookOrder|ChatTrace' -count=3
make arch-lint
git diff --check
git add cmd/otto-gateway internal/plugin internal/engine/engine_test.go .go-arch-lint.yml
git commit -m "feat(gateway): wire privacy service into hook chain"
~~~

### Task 9: Integrate privacy semantics with both Ollama endpoints

**Files:**
- Modify: `internal/adapter/ollama/handlers.go`
- Modify: `internal/adapter/ollama/adapter.go`
- Modify: `internal/adapter/ollama/ndjson.go`
- Modify: `internal/adapter/ollama/handlers_test.go`
- Modify: `internal/adapter/ollama/ndjson_test.go`
- Modify: `internal/adapter/ollama/ndjson_posthook_test.go`

**Interfaces:**

~~~go
func writePrivacyError(http.ResponseWriter, error) bool
func writePrivacyReceipt(http.ResponseWriter, context.Context)
~~~

- [ ] **Step 1: RED — lock headers and native errors for chat and generate**

For `/api/chat` and `/api/generate`, stamp `X-GW-Privacy-Profile`, `X-GW-Privacy-Scope`, surface, and capped workload before engine dispatch. Map typed errors to the approved 400/409/422/502/503 statuses and Ollama's native `{"error":"<stable-code>"}` body. Set `X-GW-Privacy-Receipt` on every response once request state exists, including blocks and internal failures.

Run: `go test ./internal/adapter/ollama -run Privacy -count=1`
Expected: FAIL because metadata, receipts, and typed mappings are absent.

- [ ] **Step 2: GREEN — add one shared adapter error/receipt path**

Use `privacy.StampHTTPContext`, `privacy.ErrorInfo`, and `privacy.SetReceiptHeader`. Never use the error cause as the client message. Preserve all non-privacy status/body behavior.

Run: `go test ./internal/adapter/ollama -run Privacy -count=1`
Expected: PASS for non-streaming cases.

- [ ] **Step 3: RED/GREEN — buffer strict streaming before synthetic replay**

Test both endpoints with streaming requested. The engine must collect the entire worker stream, run `After`, and only then write headers and native NDJSON frames. On an output block, assert zero success bytes and no 200 status were written. Standard streaming must retain the existing incremental path.

Run: `go test ./internal/adapter/ollama -run 'Privacy.*Stream|Stream.*Privacy' -count=1`
Expected after GREEN: PASS.

- [ ] **Step 4: Refactor and commit**

~~~bash
gofumpt -w internal/adapter/ollama
go test -race ./internal/adapter/ollama -run 'Privacy|Stream' -count=3
git diff --check
git add internal/adapter/ollama
git commit -m "feat(ollama): enforce privacy response boundary"
~~~

### Task 10: Integrate privacy semantics with both OpenAI endpoints

**Files:**
- Modify: `internal/adapter/openai/handlers.go`
- Modify: `internal/adapter/openai/adapter.go`
- Modify: `internal/adapter/openai/sse.go`
- Modify: `internal/adapter/openai/errors.go`
- Modify: `internal/adapter/openai/handlers_reroute_test.go`
- Modify: `internal/adapter/openai/completions_test.go`
- Modify: `internal/adapter/openai/sse_test.go`
- Modify: `internal/adapter/openai/sse_posthook_test.go`

**Interfaces:**

~~~go
type errorInner struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}
~~~

- [ ] **Step 1: RED — lock OpenAI wire compatibility**

Cover `/v1/chat/completions` and `/v1/completions`. Privacy 4xx failures use `invalid_request_error`; 5xx failures use `api_error`; `error.code` is the stable privacy code and `error.message` contains no cause or protected value. Assert receipt and scope/profile behavior matches Ollama.

Run: `go test ./internal/adapter/openai -run Privacy -count=1`
Expected: FAIL because privacy codes and headers are absent.

- [ ] **Step 2: GREEN — stamp metadata and render typed failures**

Share the same local helper between both OpenAI routes. Keep existing non-privacy error shapes unchanged by leaving `Code=nil` outside the typed privacy path.

Run: `go test ./internal/adapter/openai -run Privacy -count=1`
Expected: PASS for non-streaming cases.

- [ ] **Step 3: RED/GREEN — validate before SSE replay**

For a strict request that asked for streaming, buffer worker output, run the full output boundary, then replay valid synthetic SSE chunks and `[DONE]`. Assert an output block writes no success event and returns the native error body. Standard streaming remains incremental.

Run: `go test ./internal/adapter/openai -run 'Privacy.*Stream|Stream.*Privacy' -count=1`
Expected after GREEN: PASS.

- [ ] **Step 4: Refactor and commit**

~~~bash
gofumpt -w internal/adapter/openai
go test -race ./internal/adapter/openai -run 'Privacy|Stream' -count=3
git diff --check
git add internal/adapter/openai
git commit -m "feat(openai): enforce privacy response boundary"
~~~

### Task 11: Integrate privacy semantics with Anthropic messages

**Files:**
- Modify: `internal/adapter/anthropic/handlers.go`
- Modify: `internal/adapter/anthropic/adapter.go`
- Modify: `internal/adapter/anthropic/sse.go`
- Modify: `internal/adapter/anthropic/errors.go`
- Modify: `internal/adapter/anthropic/handlers_test.go`
- Modify: `internal/adapter/anthropic/sse_test.go`
- Modify: `internal/adapter/anthropic/sse_posthook_test.go`

- [ ] **Step 1: RED — lock Anthropic wire compatibility**

For `/v1/messages`, stamp the common metadata, emit the receipt on pass/block/error, map privacy 4xx failures to Anthropic `invalid_request_error` and 5xx failures to `api_error`, and use only the stable code as the safe message.

Run: `go test ./internal/adapter/anthropic -run Privacy -count=1`
Expected: FAIL because the adapter does not understand privacy state.

- [ ] **Step 2: GREEN — add typed errors and receipts**

Use the same privacy package helpers as the other adapters while retaining Anthropic's current top-level error envelope and all non-privacy behavior.

Run: `go test ./internal/adapter/anthropic -run Privacy -count=1`
Expected: PASS for non-streaming cases.

- [ ] **Step 3: RED/GREEN — validate before Anthropic SSE replay**

Buffer strict streams through engine collection, validate the complete response, then replay valid Anthropic events. On block, write no content-block or message event. Preserve incremental standard streaming.

Run: `go test ./internal/adapter/anthropic -run 'Privacy.*Stream|Stream.*Privacy' -count=1`
Expected after GREEN: PASS.

- [ ] **Step 4: Refactor and commit**

~~~bash
gofumpt -w internal/adapter/anthropic
go test -race ./internal/adapter/anthropic -run 'Privacy|Stream' -count=3
git diff --check
git add internal/adapter/anthropic
git commit -m "feat(anthropic): enforce privacy response boundary"
~~~

### Task 12: Export bounded privacy metrics through the existing registry

**Files:**
- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/metrics_test.go`
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`
- Modify: `internal/privacy/types.go`
- Modify: `internal/privacy/service.go`

**Interfaces:**

~~~go
type PrivacyStats struct {
	ScopesActive, RequestsInFlight, Entries int
	MaxScopes, MaxEntriesPerScope, MaxTotalEntries int
	ScopeTTL, OldestScopeAge time.Duration
	TriageEnabled bool
}

func (m *Metrics) RegisterPrivacy(func() PrivacyStats)
func (m *Metrics) RecordPrivacyRequest(profile, surface, workload, result string)
func (m *Metrics) RecordPrivacyTransformation(profile, entity, action string)
func (m *Metrics) RecordPrivacyRestoration(profile, entity, result string)
func (m *Metrics) RecordPrivacyBlock(profile, stage, reason string)
func (m *Metrics) RecordPrivacyResidual(profile, stage, entity string)
func (m *Metrics) RecordPrivacyReceipt(profile, result string)
func (m *Metrics) ObservePrivacyDuration(profile, stage string, elapsed time.Duration)
func (m *Metrics) RecordPrivacyScopeEvent(event string)
func (m *Metrics) RecordPrivacyCapacityRejection(resource string)
func (m *Metrics) RecordPrivacyMappingOperation(operation, result string)
func (m *Metrics) RecordPrivacyError(stage, reason string)
func (m *Metrics) RecordPrivacyTriage(operation, result string)
~~~

- [ ] **Step 1: RED — register the exact metric contract**

Assert existence and HELP/TYPE metadata for:

- `gw_privacy_requests_total{profile,surface,workload,result}`
- `gw_privacy_transformations_total{profile,entity,action}`
- `gw_privacy_restorations_total{profile,entity,result}`
- `gw_privacy_blocks_total{profile,stage,reason}`
- `gw_privacy_residual_findings_total{profile,stage,entity}`
- `gw_privacy_receipts_total{profile,result}`
- `gw_privacy_processing_duration_seconds{profile,stage}`
- `gw_privacy_scope_events_total{event}`
- `gw_privacy_capacity_rejections_total{resource}`
- `gw_privacy_mapping_operations_total{operation,result}`
- `gw_privacy_errors_total{stage,reason}`
- `gw_privacy_triage_requests_total{operation,result}`
- `gw_privacy_scopes_active`, `gw_privacy_scope_requests_in_flight`, `gw_privacy_mapping_entries`, `gw_privacy_scope_capacity`, `gw_privacy_mapping_capacity`, `gw_privacy_mapping_per_scope_capacity`, `gw_privacy_scope_ttl_seconds`, `gw_privacy_oldest_scope_age_seconds`, and `gw_privacy_triage_enabled` pull gauges.

Run: `go test ./internal/metrics -run Privacy -count=1`
Expected: FAIL because no privacy collectors exist.

- [ ] **Step 2: GREEN — implement fixed-label collectors and pull gauges**

Follow the existing optional registration pattern used by compression. Reuse the skills/workload limiter so `X-GW-Skill` then `X-Flow-Name` is normalized and capped at 64 observed values. Reject or map every unexpected label value to `other`; never add scope, request ID, route, value, alias, token, key, or error-message labels.

Run: `go test ./internal/metrics -run Privacy -count=1`
Expected: PASS.

- [ ] **Step 3: RED/GREEN — wire service observers and safe state**

Inject observer callbacks at service construction. Assert pass/block/error, transforms, restoration, residual, receipts, mapping, capacity, lifecycle, duration, and triage events increment exactly once. Scrape `/metrics` after inputs containing unique protected canaries and assert none appear in names, labels, HELP text, or values.

Run: `go test ./internal/metrics ./cmd/otto-gateway -run 'Privacy|MetricsNoLeak' -count=1`
Expected after GREEN: PASS.

- [ ] **Step 4: Refactor, test cardinality, and commit**

~~~bash
gofumpt -w internal/metrics internal/privacy/types.go internal/privacy/service.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
go test -race ./internal/metrics ./internal/privacy ./cmd/otto-gateway -run 'Privacy|MetricsNoLeak' -count=3
git diff --check
git add internal/metrics internal/privacy/types.go internal/privacy/service.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
git commit -m "feat(metrics): expose bounded privacy telemetry"
~~~

### Task 13: Add localhost-only authenticated mapping triage APIs

**Files:**
- Create: `internal/admin/privacy.go`
- Create: `internal/admin/privacy_test.go`
- Modify: `internal/admin/admin.go`
- Modify: `internal/admin/handlers_test.go`
- Modify: `internal/admin/testmain_test.go`
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`

**Interfaces:**

~~~go
type PrivacyScopeRow struct {
	ID string `json:"id"`
	Profile string `json:"profile"`
	State string `json:"state"`
	Entries int `json:"entries"`
	InFlight int `json:"in_flight"`
	CreatedAt time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
type PrivacyMappingRow struct {
	Entity string `json:"entity"`
	Original string `json:"original"`
	Synthetic string `json:"synthetic"`
	Provenance string `json:"provenance"`
	CreatedAt time.Time `json:"created_at"`
}
type PrivacyClearResult struct { State string `json:"state"` }
type PrivacyTriageSource interface {
	ListPrivacyScopes() []PrivacyScopeRow
	InspectPrivacyScope(string) ([]PrivacyMappingRow, error)
	ClearPrivacyScope(string) (PrivacyClearResult, error)
	ClearAllPrivacyScopes() PrivacyClearResult
}
~~~

- [ ] **Step 1: RED — lock disabled, locality, token, and cache behavior**

When triage is disabled, all four paths return 404 because they are not registered. When enabled, require the actual TCP peer parsed from `RemoteAddr`, unmapped with `netip.Addr.Unmap`, to be loopback; ignore `Forwarded` and `X-Forwarded-For`. Require a separate bearer `PRIVACY_TRIAGE_TOKEN` using constant-time comparison. Every response, including errors, sets `Cache-Control: no-store` and emits no CORS allow header.

Run: `go test ./internal/admin -run PrivacyTriage -count=1`
Expected: FAIL because the routes do not exist.

- [ ] **Step 2: GREEN — register the protected route group**

Register only:

~~~text
GET    /admin/api/privacy/scopes
GET    /admin/api/privacy/scopes/{scope-id}/mapping
DELETE /admin/api/privacy/scopes/{scope-id}
DELETE /admin/api/privacy/scopes
~~~

These are the external paths; register their `/api/privacy/...` suffixes inside the router that the server mounts at `/admin`. Use the narrow admin-owned interface. Validate and path-unescape the scope ID once. Return bounded JSON and stable error codes only.

Run: `go test ./internal/admin -run PrivacyTriage -count=1`
Expected: PASS for list and inspect.

- [ ] **Step 3: RED/GREEN — lock safe clear and audit semantics**

Single-scope clear returns 204 when wiped and 202 with `{"state":"closing"}` when active. Clear-all additionally requires `X-GW-Privacy-Confirm: clear-all`; otherwise return 400. Safe audit/metrics contain timestamp, operation, result, caller loopback address, and scope HMAC prefix—not scope text or mapping values. Cover inactive, active, missing, repeated, and all-scope clears.

Run: `go test -race ./internal/admin ./internal/privacy -run 'PrivacyTriage|Clear' -count=10`
Expected after GREEN: PASS.

- [ ] **Step 4: Refactor and commit**

~~~bash
gofumpt -w internal/admin/privacy.go internal/admin/privacy_test.go internal/admin/admin.go internal/admin/admin_test.go internal/admin/testmain_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
go test -race ./internal/admin ./cmd/otto-gateway -run Privacy -count=5
git diff --check
git add internal/admin cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
git commit -m "feat(admin): add protected privacy triage API"
~~~

### Task 14: Manage secrets, expose privacy CLI, and unify support redaction

**Files:**
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`
- Modify: `internal/admin/capture.go`
- Modify: `internal/admin/capture_test.go`
- Modify: `internal/admin/capture_redact.go`
- Modify: `scripts/gw`
- Modify: `scripts/gw.ps1`
- Modify: `scripts/.env.example`
- Modify: `scripts/lib/redact.sh`
- Modify: `scripts/lib/redact.ps1`
- Modify: `scripts/install.sh`
- Modify: `scripts/install.ps1`
- Create: `tests/install/privacy_secrets_posix_test.sh`
- Create: `tests/install/privacy_secrets_windows_test.ps1`
- Modify: `tests/scripts/test-support-redact.sh`
- Modify: `tests/scripts/test-support-redact.ps1`
- Modify: `tests/scripts/test-support-bundle.sh`
- Modify: `tests/scripts/test-support-bundle.ps1`
- Create: `tests/scripts/test-privacy-cli.sh`
- Create: `tests/scripts/test-privacy-cli.ps1`

**Interfaces:**

~~~go
func runUtility(args []string, in io.Reader, out io.Writer) (handled bool, err error)

type SecretRedactor interface {
	Redact(key, value string) string
}
~~~

- [ ] **Step 1: RED — lock managed-key lifecycle on POSIX and PowerShell**

Extend init/install fixtures so `PRIVACY_ALIAS_KEY` and `PRIVACY_TRIAGE_TOKEN` are generated with cryptographic randomness, written to `.env`, preserved across normal upgrades, and overridden by `overrides.env`. `--regenerate-secrets` rotates all five managed secrets together and prints the mapping-loss/restart warning before mutation. Neither key may appear in output.

Run: `bash tests/install/privacy_secrets_posix_test.sh`
Expected: FAIL because the two privacy secrets are unmanaged.

Run when PowerShell is available: `pwsh -NoProfile -File tests/install/privacy_secrets_windows_test.ps1`
Expected: FAIL for the same reason.

- [ ] **Step 2: GREEN — extend the existing secret manager symmetrically**

Reuse current atomic environment-file updates. Preserve comments and unrelated settings. Set safe example values/descriptions in `scripts/.env.example`; never commit real generated keys.

Run the four commands from Step 1.
Expected: PASS.

- [ ] **Step 3: RED — lock CLI parity without token exposure**

Test `gw privacy status`, `scopes`, `inspect <scope>`, `clear <scope>`, and `clear --all --yes` plus PowerShell equivalents. Read the triage token from effective `.env`/`overrides.env`; never place it in process arguments or echo it. Require explicit `--yes` for clear-all. Render disabled, unauthorized, closing, and unavailable states safely.

Run: `bash tests/scripts/test-privacy-cli.sh`
Expected: FAIL because the commands do not exist.

Run when PowerShell is available: `pwsh -NoProfile -File tests/scripts/test-privacy-cli.ps1`
Expected: FAIL.

- [ ] **Step 4: GREEN — implement local API clients**

On POSIX, pass the authorization header through `curl --config -` stdin; in PowerShell, build the header in-process for `Invoke-RestMethod`. Do not enable redirects to non-loopback hosts. `status` uses the safe ordinary snapshot endpoint; mapping commands use the protected triage API.

Run both Step 3 commands.
Expected: PASS.

- [ ] **Step 5: RED — prove capture and support artifacts share the Go classifier**

Run the secret corpus through admin capture redaction, POSIX support collection, and PowerShell support collection. Assert identical redacted output for every fixture and fail-closed artifact omission when the shared redactor process fails.

Run: `go test ./internal/admin ./cmd/otto-gateway -run 'SharedSecretRedactor|RedactSupport' -count=1 && bash tests/scripts/test-support-bundle.sh`
Expected: FAIL because each path has separate redaction rules.

- [ ] **Step 6: GREEN — add a hidden stdin utility and inject the classifier**

Handle `otto-gateway redact-support` before normal config/network startup. It reads bounded UTF-8 records from stdin, writes redacted records to stdout, and never logs content. Inject the same `SecretClassifier` into admin capture. Make both support wrappers pipe candidate content through this binary and omit the artifact on any non-zero exit.

Run: `go test ./internal/admin ./cmd/otto-gateway -run 'SharedSecretRedactor|RedactSupport' -count=1 && bash tests/scripts/test-support-bundle.sh`
Expected: PASS.

- [ ] **Step 7: Refactor, verify both shells, and commit**

~~~bash
gofumpt -w cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/capture.go internal/admin/capture_redact.go internal/admin/capture_test.go
go test -race ./internal/admin ./cmd/otto-gateway -run 'Secret|Privacy|Redact' -count=3
bash tests/install/privacy_secrets_posix_test.sh
bash tests/scripts/test-privacy-cli.sh
bash tests/scripts/test-support-redact.sh
bash tests/scripts/test-support-bundle.sh
git diff --check
git add cmd/otto-gateway internal/admin scripts tests/scripts
git commit -m "feat(cli): manage and inspect privacy state safely"
~~~

If `pwsh` is installed, run the matching PowerShell tests before the commit; CI remains the mandatory parity gate on hosts without it.

### Task 15: Add read-only privacy status to dashboard, About, and health

**Files:**
- Modify: `internal/admin/snapshot.go`
- Modify: `internal/admin/snapshot_test.go`
- Modify: `internal/admin/admin.go`
- Modify: `internal/admin/handlers_test.go`
- Modify: `internal/admin/templates/dashboard.html.tmpl`
- Modify: `internal/admin/templates/about.html.tmpl`
- Modify: `internal/admin/templates/base.html.tmpl`
- Create: `internal/admin/templates/privacy.html.tmpl`
- Modify: `internal/admin/static/js/admin.js`
- Modify: `internal/admin/static/css/admin.css`
- Modify: `internal/admin/admin_js_test.js`
- Create: `internal/admin/privacy_template_test.go`
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`

**Interfaces:**

~~~go
type PrivacySnapshot struct {
	DefaultProfile string `json:"default_profile"`
	RequestProfiles []string `json:"request_profiles"`
	StrictAvailable bool `json:"strict_available"`
	TriageEnabled bool `json:"triage_enabled"`
	AliasKeyPresent bool `json:"alias_key_present"`
	TriageTokenPresent bool `json:"triage_token_present"`
	PIIEnabled bool `json:"pii_enabled"`
	NEREnabled bool `json:"ner_enabled"`
	SecretAction string `json:"secret_action"`
	TechnicalAction string `json:"technical_action"`
	PIIMode string `json:"pii_mode"`
	Recognizers []string `json:"recognizers"`
	EntityActions map[string]string `json:"entity_actions"`
	StrictFullBuffering bool `json:"strict_full_buffering"`
	ReceiptVersion int `json:"receipt_version"`
	ScopesActive int `json:"scopes_active"`
	RequestsInFlight int `json:"requests_in_flight"`
	Entries int `json:"entries"`
	MaxScopes int `json:"max_scopes"`
	MaxEntriesPerScope int `json:"max_entries_per_scope"`
	MaxTotalEntries int `json:"max_total_entries"`
	ScopeTTLSeconds float64 `json:"scope_ttl_seconds"`
	OldestScopeAgeSeconds float64 `json:"oldest_scope_age_seconds"`
	RequestsProtected uint64 `json:"requests_protected"`
	RequestsBlocked uint64 `json:"requests_blocked"`
	LastErrorCode string `json:"last_error_code,omitempty"`
}
type PrivacyStatusSource interface { PrivacySnapshot() PrivacySnapshot }
~~~

- [ ] **Step 1: RED — lock the safe snapshot and health projection**

Assert the ordinary admin snapshot and `/health/hooks` expose only profile availability, enabled flags, actions, configured limits, aggregate usage, aggregate totals, oldest age, and last stable error code. Alias/triage key values, scope IDs, mappings, entity values, request IDs, and tokens must be absent. Keep the hook name `PIIRedactionHook`.

Run: `go test ./internal/admin ./cmd/otto-gateway -run 'PrivacySnapshot|PrivacyHealth' -count=1`
Expected: FAIL because the projections are absent.

- [ ] **Step 2: GREEN — inject one safe read-only projection**

Adapt `privacy.SafeSnapshot` in main to the admin-owned interface. Do not give ordinary admin handlers access to inspection or clear methods. Include key-presence booleans instead of key contents.

Run: `go test ./internal/admin ./cmd/otto-gateway -run 'PrivacySnapshot|PrivacyHealth' -count=1`
Expected: PASS.

- [ ] **Step 3: RED — lock dashboard and About content**

Dashboard shows current default profile, strict availability, protected/blocked totals, active/in-flight/entry usage, limits, oldest age, triage enabled, and last stable error. About documents every `PRIVACY_*` and retained `PII_*` setting, defaults, actions, and the 16-regex-plus-PERSON/LOCATION inventory. The privacy page is read-only: no form controls, mutation requests, or mapping content.

Run: `go test ./internal/admin -run 'PrivacyPage|DashboardPrivacy|AboutPrivacy' -count=1 && node --test internal/admin/admin_js_test.js`
Expected: FAIL because the UI does not render privacy state.

- [ ] **Step 4: GREEN — render accessible, reduced-motion-safe status**

Use semantic headings, definition lists, text plus color for state, keyboard-reachable help links, and existing responsive/reduced-motion conventions. Render limits as current/max and mark restart-required settings explicitly.

Run: `go test ./internal/admin -run 'PrivacyPage|DashboardPrivacy|AboutPrivacy' -count=1 && node --test internal/admin/admin_js_test.js`
Expected: PASS.

- [ ] **Step 5: Refactor and commit**

~~~bash
gofumpt -w internal/admin cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
go test -race ./internal/admin ./cmd/otto-gateway -run 'Privacy|Dashboard|About|Health' -count=3
node --test internal/admin/admin_js_test.js
git diff --check
git add internal/admin cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
git commit -m "feat(admin): show read-only privacy status"
~~~

### Task 16: Document privacy operations and add Grafana reporting

**Files:**
- Modify: `README.md`
- Modify: `docs/operating.md`
- Modify: `docs/operator-quickstart.md`
- Modify: `docs/INSTALL.md`
- Create: `docs/privacy-boundary.md`
- Create: `docs/releases/2026-07-31-privacy-boundary.md`
- Modify: `docs/grafana/otto-gateway-dashboard.json`
- Modify: `scripts/gen_grafana_dashboard.py`
- Modify: `scripts/test_gen_grafana_dashboard.py`
- Modify: `scripts/.env.example`
- Create: `scripts/test_privacy_docs.py`
- Modify: `internal/admin/templates/docs.html.tmpl`

- [ ] **Step 1: RED — lock dashboard panels, alerts, and bounded queries**

Add failing generator tests for request results by profile/surface/workload, transformations/restorations, block and residual reasons, processing latency, scope/capacity usage, receipt outcomes, triage operations, and internal errors. Add alerts for strict blocks, residual findings, capacity pressure, mapping growth, privacy errors, and missing strict receipts. Assert no query groups by scope, request ID, route, raw error, token, alias, or value.

Run: `python3 scripts/test_gen_grafana_dashboard.py`
Expected: FAIL because privacy panels and alerts are absent.

- [ ] **Step 2: GREEN — extend the generator and regenerate JSON**

Build panels from the exact Task 12 metric names. Use rate windows and percentages that tolerate idle series, and show configured maxima next to current gauges. Regenerate `otto-gateway-dashboard.json`; do not edit generated panel JSON independently of the generator.

Run: `python3 scripts/gen_grafana_dashboard.py && python3 scripts/test_gen_grafana_dashboard.py`
Expected: PASS.

- [ ] **Step 3: RED — create an operator cold-read checklist**

Add docs checks requiring:

- standard remains enabled by default and strict is selectable/configurable;
- all `PRIVACY_*` defaults and retained `PII_*` behavior;
- restart-required environment/override workflow;
- profile/scope headers and receipt decode/validation examples;
- exact status, triage, and clear CLI/API behavior and localhost/token security boundary;
- mapping TTL/capacity/restart loss and debugging implications;
- hook order and compressed-input behavior;
- strict full-buffer streaming semantics and native error codes;
- workflow-engine responsibilities for parsing, minimization, routing, scope propagation, receipt enforcement, schema/tool execution, and artifacts;
- upgrade/rollback implications and NER compiled default.

Run: `python3 scripts/test_privacy_docs.py`
Expected: FAIL until the new page and cross-links exist.

- [ ] **Step 4: GREEN — write operator, workflow, and release guidance**

Keep secrets out of command examples. Explain that direct worker access bypasses the boundary and that strict workflows must reject an absent, malformed, non-strict, non-full, or non-pass receipt. State that docs and artifact handling remain workflow-engine responsibilities, not Gateway mapping duties.

Run: `python3 scripts/test_privacy_docs.py && rg -n 'PRIVACY_|X-GW-Privacy|privacy_(input|output|capacity|internal)' README.md docs scripts/.env.example`
Expected: PASS with every public key/header/error linked to the canonical privacy page.

- [ ] **Step 5: Verify generated artifacts and commit**

~~~bash
python3 scripts/gen_grafana_dashboard.py
python3 scripts/test_gen_grafana_dashboard.py
python3 scripts/test_privacy_docs.py
git diff --check
git add README.md docs scripts/.env.example scripts/gen_grafana_dashboard.py scripts/test_gen_grafana_dashboard.py scripts/test_privacy_docs.py internal/admin/templates/docs.html.tmpl
git commit -m "docs: publish privacy boundary operations guide"
~~~

### Task 17: Prove cross-surface privacy, concurrency, leakage, and release readiness

**Files:**
- Create: `tests/privacy/privacy_boundary_test.go`
- Create: `tests/privacy/privacy_leakage_test.go`
- Create: `internal/privacy/benchmark_test.go`
- Modify: `tests/e2e/plugin_chain_test.go`
- Modify: `scripts/test-pii.sh`
- Modify: `scripts/test-pii.ps1`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: RED — add one cross-surface conformance matrix**

Start a real in-process server with deterministic test keys and a fake worker. Run the same fixtures through all five routes. Assert identical profile/scope resolution, transformations, restoration eligibility, receipt fields, error/status mapping, and strict stream buffering. Cover every canonical string location, compressed requests, secrets, all technical formats, personal data, generated values, unknown aliases, and direct worker non-dispatch on input block. Add a consumer-side strict receipt validator fixture and prove it rejects a missing header, malformed base64/JSON, non-strict profile, non-full coverage, and non-pass result, including a simulated direct-worker response.

Run: `go test ./tests/privacy -run Conformance -count=1`
Expected: FAIL until the integration harness and all surfaces satisfy the matrix.

- [ ] **Step 2: GREEN — implement the conformance harness and run it against completed behavior**

Implement only the server harness, fixtures, and assertions in `tests/privacy`. The completed Tasks 1–16 must satisfy it without adapter-specific privacy policy. If a row exposes a product defect, stop this task, return to the owning task's focused RED/GREEN loop, commit that correction in the owning package, and then rerun the matrix before continuing.

Run: `go test ./tests/privacy -run Conformance -count=1`
Expected: PASS.

- [ ] **Step 3: RED/GREEN — add canary leakage and lifecycle stress tests**

Use unique protected canaries and assert absence from ordinary logs, strict chat traces, metrics, health, dashboard snapshots, receipts, errors, captures, and support bundles. In parallel, run at least 100 scopes and 500 requests while reaping, inspecting, and clearing; assert stable mappings, exact caps, no active eviction, no cross-scope restoration, and eventual zero entries after clear/expiry.

Run: `go test -race ./tests/privacy ./internal/privacy -run 'Leakage|Parallel|Lifecycle' -count=10`
Expected after GREEN: PASS with no race report.

- [ ] **Step 4: RED/GREEN — enforce the performance contract**

Benchmark standard and strict inbound/outbound with representative 1 KiB, 64 KiB, and maximum accepted payloads plus 100 parallel scopes. Record allocations. Set generous regression ceilings based on a clean three-run median and require no Gateway-wide classification lock via block profiling.

Run: `go test ./internal/privacy -run '^$' -bench 'Privacy(Standard|Strict|Parallel)' -benchmem -count=3`
Expected after GREEN: stable medians with documented ceilings in the benchmark test.

- [ ] **Step 5: Add focused privacy gates to local CI and GitHub Actions**

Add `test-privacy` and `test-privacy-race` Make targets. Run shell parity tests on Linux/macOS and PowerShell parity tests on Windows. Keep the full existing CI contract; do not replace broader race, architecture, vulnerability, or cross-build gates with the focused target.

Run: `make test-privacy && make test-privacy-race`
Expected: PASS.

- [ ] **Step 6: Run the complete release gate**

~~~bash
make fmt-check
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
make lint
make arch-lint
python3 scripts/test_gen_grafana_dashboard.py
python3 scripts/test_privacy_docs.py
node --test internal/admin/admin_js_test.js
bash tests/install/privacy_secrets_posix_test.sh
bash tests/scripts/test-privacy-cli.sh
bash tests/scripts/test-support-redact.sh
bash tests/scripts/test-support-bundle.sh
bash scripts/test-pii.sh
govulncheck ./...
privacy_build_dir="$(mktemp -d)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$privacy_build_dir/otto-gateway-linux-amd64" ./cmd/otto-gateway
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o "$privacy_build_dir/otto-gateway-darwin-arm64" ./cmd/otto-gateway
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o "$privacy_build_dir/otto-gateway-darwin-amd64" ./cmd/otto-gateway
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$privacy_build_dir/otto-gateway-windows-amd64.exe" ./cmd/otto-gateway
shasum -a 256 "$privacy_build_dir"/*
make ci
git diff --check
~~~

When `pwsh` is available, also run `tests/install/privacy_secrets_windows_test.ps1`, `tests/scripts/test-privacy-cli.ps1`, `tests/scripts/test-support-redact.ps1`, `tests/scripts/test-support-bundle.ps1`, and `scripts/test-pii.ps1`. Every command must pass from a clean checkout. The temporary cross-build directory may be removed after recording its hashes; do not delete user files.

- [ ] **Step 7: Commit the conformance and release gates**

~~~bash
git add tests/privacy tests/e2e/plugin_chain_test.go internal/privacy/benchmark_test.go scripts/test-pii.sh scripts/test-pii.ps1 Makefile .github/workflows/ci.yml
git diff --cached --check
git commit -m "test(privacy): add cross-surface release gates"
~~~

## Acceptance Traceability

| Approved design obligation | Implementation tasks |
|---|---:|
| Default-on standard profile, selectable strict profile, configuration and NER alignment | 1, 8 |
| Shared secrets classification and deterministic overlap policy | 2, 6, 14 |
| Scoped in-memory mappings, TTL/capacity, concurrency, clear, restart loss | 3, 6, 7, 13, 17 |
| Valid technical aliases and relationship preservation | 4, 6, 7 |
| Standard compatibility and unchanged hook identity | 5, 8 |
| Strict inbound/outbound residual enforcement and selective restoration | 6, 7 |
| Strict full buffering and no partial response on block | 7, 9, 10, 11, 17 |
| Native Ollama/OpenAI/Anthropic errors and bounded receipts | 5, 7, 9, 10, 11 |
| Compression-before-privacy and strict-safe chat tracing | 8 |
| Bounded Prometheus metrics and Grafana reporting | 12, 16 |
| Localhost-only authenticated inspection and clear | 13, 14 |
| Managed keys, POSIX/PowerShell parity, shared capture/support redaction | 14, 17 |
| Read-only dashboard/About/health and operator documentation | 15, 16 |
| Workflow-engine ownership of schema, tools, receipt enforcement, and artifacts | 16 |
| Race, leakage, performance, vulnerability, architecture, and cgo-free release gates | 17 |

## Final Review Gate

- [ ] Every task has a focused RED command whose failure is attributable to missing behavior, followed by the smallest GREEN implementation and a passing focused command.
- [ ] Standard profile compatibility fixtures pass unchanged on every public route.
- [ ] Strict input blocks before worker dispatch; strict output blocks before headers or body bytes.
- [ ] No test, log, metric, error, receipt, snapshot, trace, capture, or support artifact leaks its protected canary.
- [ ] All mapping and clear lifecycle race tests pass under `-race` with repeated counts.
- [ ] The generated Grafana JSON exactly matches its generator and contains no unbounded labels.
- [ ] POSIX and PowerShell wrapper suites pass in CI, including secret preservation and token-safe triage calls.
- [ ] `make ci`, `govulncheck ./...`, architecture checks, and all three cgo-free cross-builds pass from a clean checkout.
- [ ] The implementation branch contains one reviewable commit per task and no unrelated repository changes.
