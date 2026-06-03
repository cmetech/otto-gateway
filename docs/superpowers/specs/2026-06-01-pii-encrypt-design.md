# PII Encrypt/Decrypt Mode — Design Spec

**Date:** 2026-06-01
**Status:** Design — pending implementation plan
**Scope:** `internal/plugin/pii/` + slice-5 boot wiring
**Prior art:** Microsoft Presidio's `encrypt` operator
([tutorial](https://microsoft.github.io/presidio/tutorial/12_encryption/))

## 1. Motivation

The PII plugin today supports four one-way redaction modes: `replace`,
`mask`, `hash`, `drop`. All four destroy information — the LLM sees a
sanitized token, and the client receives the LLM's response with that
same sanitized token intact.

For a class of use cases (LangFlow flows that pass user data through an
LLM and back; Pi-SDK CLIs that ask the model to draft emails), the
desired property is different: **the LLM should never see the PII in
plaintext, but the client should see plaintext in the response as if no
redaction happened**. This is the round-trip property.

`encrypt` adds that property as a fifth action peer to the existing
four. On the request side, detected PII values are encrypted with a
local key into ciphertext tokens that flow to the LLM. On the response
side, those tokens are decrypted back to plaintext before the response
returns to the client. From the client's perspective, PII is transparent.

## 2. Goals & Non-Goals

**Goals:**
- Add `encrypt` as a peer action alongside `replace`/`mask`/`hash`/`drop`.
- Round-trip is invisible to clients: request-side encryption is matched
  by response-side decryption, transparently.
- Per-entity action configuration: operators can set
  `EntityActions={"Email":"encrypt","SSN":"mask"}` while leaving a
  global `Mode` default for unlisted entities.
- Any-string key UX: `PII_ENCRYPT_KEY` accepts any non-empty string;
  gateway derives a 32-byte AES-256-GCM key via SHA-256 at boot.
- Fail fast at boot when encrypt is configured but the key is missing.
- No new dependencies beyond Go stdlib.

**Non-Goals:**
- **Response-side PII detection.** If the LLM hallucinates a fresh
  SSN in its reply, the client sees it raw. Adding response-side
  recognizer sweeps is a separable feature.
- **Streaming during encrypt.** Streaming is auto-disabled when encrypt
  is active for any entity. Per-chunk decryption with cross-chunk
  ciphertext buffering is explicitly deferred — the complexity is not
  justified for a laptop product where non-streamed response latency
  is acceptable.
- **Key rotation infrastructure.** No key-version byte in the payload,
  no multi-key registry. On a laptop product an operator restarts with
  a new `PII_ENCRYPT_KEY`; chat history is ephemeral.
- **External key storage** (Vault, KMS, macOS Keychain). Env var only.

## 3. Locked Design Decisions

Each decision below was reached interactively; the rationale is the
reason it was chosen over alternatives, not just a restatement.

### 3.1 Streaming policy: auto-disable

When `encrypt` is active for any entity (global `Mode=="encrypt"` OR
any value in `EntityActions` equals `"encrypt"`), `Before()` sets
`req.Stream = false` at entry. This routes the response through
`Engine.Collect` (aggregated path), where the Post hook sees the
complete `*ChatResponse` before bytes hit the wire. One `INFO` log
per affected request: `pii.encrypt.streaming_disabled`.

**Rejected:** per-chunk decrypt with cross-chunk buffering (would
require new chunk-transform seams in all three adapters; failure modes
around malformed/never-closing tokens too thorny for v1).

### 3.2 Per-entity action config

New struct field `EntityActions map[string]string` on
`PIIRedactionHook`. Lookup is `EntityActions[entityName]` with fallback
to `h.Mode` for unlisted entities. Empty `EntityActions` reproduces
today's behavior exactly.

**Wire shape:** single env var
`PII_ENTITY_ACTIONS=Email:encrypt,SSN:mask,CreditCard:drop`.
Slice-5 parses on `,` then `:`. Unknown entity names or unknown action
values cause boot failure with a message naming the offending pair.

### 3.3 Token marker

```
[PII:Entity:base64url(nonce ‖ ciphertext ‖ tag)]
```

- `Entity` is the recognizer Name (`Email`, `SSN`, `IPv4`, `IPv6`,
  `CreditCard`, `USPhone`).
- 12-byte random nonce, AES-256-GCM ciphertext, 16-byte GCM tag.
- `base64url` (RFC 4648 §5), unpadded — `[A-Za-z0-9_-]+`. Standard
  base64's `+/=` chars trigger more LLM mangling.
- **Entity name is bound as GCM Associated Data (AAD)** so the tag
  authenticates the entity label. A token relabeled `[PII:SSN:...]`
  when it was encrypted as `Email` fails decrypt — defense against
  silent label drift.

**Square brackets, not angle brackets.** Per commit `a3160e1`
(`fix(260531-pt8): swap angle brackets for square brackets in pii
sentinels`): angle brackets `<EMAIL_1>` cause kiro-cli / Claude to
treat them as opening XML tags and hang the ACP prompt for the full
120s timeout. `<<PII:...>>` would have re-introduced that exact bug.
The existing `[ENTITY]` and `[ENTITY:h-XXXX]` shapes already follow
this rule.

**Decrypt regex:**
```go
regexp.MustCompile(`\[PII:([A-Za-z]+):([A-Za-z0-9_-]+)\]`)
```
Group 1 = entity (passed as AAD to GCM `Open`); group 2 = base64url
payload. No collision with existing `[ENTITY]` (replace) or
`[ENTITY:h-XXXX]` (hash) shapes — the `PII:` namespace prefix is
distinctive.

### 3.4 Hook shape: one struct, both interfaces

Existing `PIIRedactionHook` grows an `After(ctx, req, resp) error`
method satisfying `engine.PostHook`. Same struct, same config — encrypt
and decrypt share the key, the action map, the logger, the
enabled-entities filter. Impossible to wire the Pre half without the
Post half.

**Precedent:** `LoggingHook` already satisfies both interfaces (the
"FOURTH Pre hook AND the only Post hook" per
`internal/plugin/pii/summary.go:12`). Chain machinery, `/health/hooks`,
and slice-5 wiring all handle this pattern.

**Naming:** `PIIRedactionHook` is preserved. "Redaction" in the
Presidio vocabulary already encompasses reversible transforms.
Renaming would touch `/health/hooks`, slice-5 wiring, every test that
asserts hook name — not worth the churn.

### 3.5 Key storage: any-string + SHA-256

`PII_ENCRYPT_KEY` accepts any non-empty string. At boot:

```go
func DeriveKey(envValue string) ([]byte, error) {
    if envValue == "" {
        return nil, errors.New("pii: PII_ENCRYPT_KEY is empty")
    }
    sum := sha256.Sum256([]byte(envValue))
    return sum[:], nil
}
```

**Why SHA-256 not scrypt/argon2:** KDFs are slow by design to defend
against offline brute-force of stolen ciphertext. Our threat model is
"upstream LLM provider sees plaintext PII" and "logs leak PII", not
offline ciphertext attack. Fast deterministic derivation buys the same
restart property as raw bytes (same env → same key → in-flight tokens
still decrypt after restart) without operator key-format friction.

**Boot validation** (slice-5 main.go):

| `PII_REDACTION_ENABLED` | `encrypt` active anywhere? | `PII_ENCRYPT_KEY` | Boot |
|---|---|---|---|
| `0` | n/a | n/a | starts; PII chain disabled |
| `1` | no | empty or set | starts; key ignored |
| `1` | yes | empty | **fatal exit** |
| `1` | yes | non-empty | starts; key derived once |

`encryptActive` predicate: `Mode == "encrypt" || any(value == "encrypt"
for value in EntityActions)`.

### 3.6 Decrypt failure mode: leave in place + WARN

When `gcm.Open` fails for a matched token (mangled base64, AAD
mismatch from relabeling, key rotated mid-session, ciphertext
corruption), the Post hook:

- Leaves the token verbatim in the response text.
- Emits one `slog.Warn` per failure with entity + reason category
  (`bad_token_shape`, `bad_base64`, `gcm_open`).
- Does not abort the response; siblings in the same message decrypt
  independently.

The client sees the visible defect (`[PII:Email:N4kQ...corrupted...]`)
and can report it; operators have WARN logs for diagnosis. Aborting on
one mangled token would let an over-helpful LLM kill an entire
otherwise-correct response.

## 4. Components

### 4.1 `internal/plugin/pii/encrypt.go` (NEW)

Pure functions, no global state:

```go
// DeriveKey returns SHA-256(envValue) as a 32-byte AES-256-GCM key.
// Errors only when envValue is empty.
func DeriveKey(envValue string) ([]byte, error)

// EncryptValue encrypts plaintext under key, binding entity as AAD,
// and returns the full "[PII:Entity:base64url]" token.
func EncryptValue(key []byte, entity, plaintext string) (string, error)

// DecryptToken takes the base64url payload from a parsed token and
// returns plaintext. Entity is passed as AAD and must match what was
// used at encrypt time or GCM Open fails.
func DecryptToken(key []byte, entity, payload string) (string, error)
```

Uses `crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/sha256`,
`encoding/base64` — all stdlib.

### 4.2 `internal/plugin/pii/modes.go` (EXTEND)

`ApplyMode` today is
`ApplyMode(mode, entity, value string, counter int, hashKey []byte)
string`. It gains one new trailing parameter `encryptKey []byte`
(becoming the sixth parameter) and a fifth `case` arm:

```go
case "encrypt":
    tok, err := EncryptValue(encryptKey, entity, value)
    if err != nil {
        slog.Default().Warn("pii.encrypt.failed",
            "entity", entity, "err", err)
        return value  // fail-safe: leave plaintext rather than emit a broken token
    }
    return tok
```

Callers update accordingly (one call site in `pii.go`).

### 4.3 `internal/plugin/pii/pii.go` (EXTEND)

Struct gains two fields:

```go
type PIIRedactionHook struct {
    // ... existing fields ...
    EntityActions map[string]string  // NEW
    EncryptKey    []byte             // NEW
}
```

New small helper:

```go
// actionFor returns the action this hook should apply to a given
// entity. EntityActions[entity] wins when set; otherwise h.Mode.
func (h *PIIRedactionHook) actionFor(entity string) string {
    if a, ok := h.EntityActions[entity]; ok {
        return a
    }
    return h.Mode
}

// encryptActive reports whether any active entity is configured for
// encrypt mode (used by Before's stream-disable + by After's no-op
// fast path).
func (h *PIIRedactionHook) encryptActive() bool {
    if h.Mode == "encrypt" {
        return true
    }
    for _, a := range h.EntityActions {
        if a == "encrypt" {
            return true
        }
    }
    return false
}
```

`Before` changes:
1. After the existing nil/enabled guard, if `h.encryptActive() &&
   req.Stream`, set `req.Stream = false` and emit
   `slog.Info("pii.encrypt.streaming_disabled", ...)`.
2. Per-match replacement uses `h.actionFor(r.Name)` instead of the
   global `h.Mode`. Counter logic unchanged.
3. `ApplyMode` call passes `h.EncryptKey` as the new parameter.

`After` (NEW):
```go
func (h *PIIRedactionHook) After(ctx context.Context,
    req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
    if !h.Enabled || resp == nil || !h.encryptActive() {
        return nil
    }
    for i := range resp.Message.Content {
        cp := &resp.Message.Content[i]
        if cp.Kind != canonical.ContentKindText {
            continue
        }
        cp.Text = decryptTokenRe.ReplaceAllStringFunc(cp.Text,
            func(match string) string {
                sub := decryptTokenRe.FindStringSubmatch(match)
                if len(sub) != 3 {
                    h.logger().Warn("pii.decrypt.failed",
                        "reason", "bad_token_shape")
                    return match
                }
                entity, payload := sub[1], sub[2]
                pt, err := DecryptToken(h.EncryptKey, entity, payload)
                if err != nil {
                    h.logger().Warn("pii.decrypt.failed",
                        "entity", entity, "reason", err)
                    return match
                }
                return pt
            })
    }
    return nil
}

var _ engine.PostHook = (*PIIRedactionHook)(nil)  // compile-time
```

Package-level regex:
```go
var decryptTokenRe = regexp.MustCompile(
    `\[PII:([A-Za-z]+):([A-Za-z0-9_-]+)\]`)
```

`Describe` updates: `kind` becomes `"Pre,Post"`; published config
gains `"decrypt_active": h.encryptActive()`. `HashKey` and `EncryptKey`
are NEVER published.

### 4.4 slice-5 main.go wiring

- Parse `PII_ENTITY_ACTIONS` env var into `map[string]string`. Validate
  every key against `pii.SourceAuditNames()` (must be a known
  recognizer) and every value against the set `{replace, mask, hash,
  drop, encrypt}`. Boot-fail with a clear message on any mismatch.
- If `encryptActive == true`, call `pii.DeriveKey(os.Getenv(
  "PII_ENCRYPT_KEY"))`. Empty → boot-fail.
- Construct one `*PIIRedactionHook` populated with both `HashKey` and
  `EncryptKey` (each only when its respective mode is active).
- Register the SAME pointer in both `cfg.PreHooks` and `cfg.PostHooks`.

## 5. Data Flow

```
[client]
   │  wire request
   ▼
[adapter]  parse → canonical.ChatRequest
   │
   ▼
[Engine.Run]
   │  Pre chain: RequestID → Auth → PIIRedactionHook.Before → Logging
   │            │
   │            └─► encryptActive?
   │                   ├─ yes → req.Stream=false; matches → [PII:E:b64url]
   │                   └─ no  → matches → [E]/[E_N]/[E:h-…]/mask/""
   ▼
[Engine.Collect]  kiro-cli prompts upstream LLM → aggregated ChatResponse
   │
   │  Post chain: PIIRedactionHook.After → LoggingHook.After
   │            │
   │            └─► encryptActive?
   │                   ├─ yes → regex-sweep resp text, decrypt each token
   │                   └─ no  → no-op
   ▼
[adapter]  canonical.ChatResponse → wire response
   │
   ▼
[client]  sees plaintext PII (or visible defect on decrypt failure)
```

Same-plaintext-twice for encrypt: each match gets a fresh GCM nonce,
producing two different ciphertexts. Both decrypt correctly. The LLM
cannot tell that two tokens reference the same underlying value — a
mild privacy benefit. The existing per-canonical-value counter still
increments for `Summary` bookkeeping but the encrypt token doesn't
embed it (counter is only meaningful for `replace` mode's `[E_N]`).

## 6. Configuration Surface

New env vars:

```bash
# Required when encrypt is active anywhere. Any non-empty string.
# Derived to 32 bytes via SHA-256 at boot.
PII_ENCRYPT_KEY=correct-horse-battery-staple

# Optional per-entity action overrides. Entries override the global
# PII_REDACTION_MODE for the named entities.
PII_ENTITY_ACTIONS=Email:encrypt,SSN:encrypt,CreditCard:drop
```

Existing vars (unchanged):
- `PII_REDACTION_ENABLED`
- `PII_REDACTION_MODE` (now usable with value `encrypt`)
- `PII_HASH_KEY`
- `PII_REDACTION_ENABLED_ENTITIES`

Backward compatibility: zero. If `PII_ENTITY_ACTIONS` is unset and
`PII_REDACTION_MODE` is not `encrypt`, behavior is bit-identical to
today.

## 7. Error Handling & Invariants

| Condition | Behavior |
|---|---|
| `gcm.Open` failure on a matched token | Leave verbatim, `slog.Warn("pii.decrypt.failed", entity, reason)` |
| Regex matched something that doesn't parse | Same as above, reason `bad_token_shape` |
| `EncryptValue` failure during `Before` | Fail-safe: log warn, leave plaintext unredacted (better visible than silent token-with-no-payload). Should be unreachable: stdlib AES-GCM Seal only fails on programmer errors (wrong key size). |
| `PII_ENTITY_ACTIONS` with unknown entity | Boot fatal, message names the offending entity |
| `PII_ENTITY_ACTIONS` with unknown action | Boot fatal, message names the offending action |
| encrypt active + `PII_ENCRYPT_KEY` empty | Boot fatal |
| Client requests `stream=true` + encrypt active | `req.Stream=false`, one `INFO` log per request |
| `resp.Message.Content` empty / `cp.Kind != Text` | `After` is no-op (range falls through; non-text parts skipped) |
| `h.Enabled == false` | `Before` and `After` are total no-ops (existing two-knob model) |
| `req == nil` / `resp == nil` | Guarded same as existing `Before` |
| Encrypt mode key drift across requests | Out of scope; each request derives from the boot-time key |

## 8. Threat Model & Security Notes

**In scope:**
- Hide PII from the upstream LLM provider (the primary threat).
- Hide PII from kiro-cli logs and ACP wire traffic.
- Bind the entity-class label to the ciphertext (GCM AAD) so a
  relabeled token fails to decrypt rather than silently returning
  the wrong-typed plaintext.

**Out of scope:**
- An attacker with read access to the gateway's environment can read
  `PII_ENCRYPT_KEY` and decrypt any token. This is acceptable on a
  laptop product — the operator already has direct access to the
  plaintext via the chat session.
- An attacker with offline ciphertext and a brute-force budget can
  attack weak passphrases. Threat model does not include
  ciphertext-at-rest leakage.
- Side channels (timing, memory dumps): not addressed.

**T-8-LEAK extension:** `EncryptKey` is NEVER serialized by `Describe`
or logged by any code path. Same discipline as the existing `HashKey`.

## 9. Testing Strategy

New file `internal/plugin/pii/encrypt_test.go` (mirrors the
modes_test.go / pii_test.go layout):

- **Round-trip happy path:** Encrypt then Decrypt → original plaintext.
- **AAD binding:** Encrypt as `Email`, attempt Decrypt as `SSN` → error.
- **Wrong key:** Encrypt under k1, attempt Decrypt under k2 → error.
- **DeriveKey determinism:** `DeriveKey("x")` twice → identical 32B.
- **DeriveKey divergence:** `DeriveKey("x")` vs `DeriveKey("X")` → different.
- **DeriveKey empty:** `DeriveKey("")` → error.
- **Token shape:** `EncryptValue` output matches `decryptTokenRe`.
- **Same plaintext twice → different ciphertexts** (nonce randomness).

Extensions to existing files:

- `modes_test.go`: `ApplyMode("encrypt", ...)` round-trip with a real
  key; unknown-mode fallback still hits `replace`.
- `pii_test.go`:
  - `Before` flips `req.Stream` when encrypt is active; leaves it
    alone otherwise.
  - `actionFor` returns `EntityActions[entity]` when set, else `Mode`.
  - `EntityActions={"Email":"encrypt"}` + `Mode="mask"` → email becomes
    a `[PII:Email:...]` token; SSN becomes the mask shape.
  - `After` decrypts a response containing two encrypted Email tokens;
    mangled SSN token stays verbatim + warn.
  - `After` is a no-op when `encryptActive == false`.
  - `var _ engine.PostHook = (*PIIRedactionHook)(nil)` (compile-time).
  - `Describe` returns `kind="Pre,Post"` and surfaces `decrypt_active`.

Slice-5 boot validation tests (in whatever file slice-5 boot tests
live in today):

- Unknown entity in `PII_ENTITY_ACTIONS` → boot error.
- Unknown action in `PII_ENTITY_ACTIONS` → boot error.
- encrypt active + empty `PII_ENCRYPT_KEY` → boot error.
- encrypt active + valid key → boot succeeds; hook registered in both
  PreHooks and PostHooks.
- encrypt not active + empty `PII_ENCRYPT_KEY` → boot succeeds, key
  field zero.

Integration coverage at the adapter level is intentionally NOT added
in this scope — the existing `chat_trace_e2e_test.go` suites in each
adapter exercise the Pre/Post chain generically; once `encrypt` is one
of the actions ApplyMode supports, those suites cover it without
adapter-specific additions.

## 10. Open Items

None. All five originally-flagged ambiguity areas (pipeline seam,
token marker, streaming, key storage, per-entity action config) plus
two follow-on questions (env-var wire shape for `EntityActions`,
decrypt failure mode) are resolved above.

The next step is `writing-plans` — turning this spec into an
executable implementation plan with task breakdown, dependency
analysis, and verification gates.

## 11. Recognizer Expansion (2026-06-03)

The encrypt round-trip described in §1–§10 is recognizer-agnostic: any
entity name parsed by `redact()` flows through `ApplyMode("encrypt", …)`
into a `[PII:Entity:base64url]` token and is decrypted by the `After`
sweep. This section records the recognizer set that ships in the
2026-06-03 expansion plan (`docs/superpowers/specs/2026-06-03-pii-ner-and-telecom-recognizers-plan.md`).

### 11.1 Telecom Regex Recognizers

Seven additional regex recognizers ported from the loop_24 Privacy
Vault project:

| Entity | Pattern (shape) | Context anchor |
|---|---|---|
| `SIP_URI` | `sips?:user@host[:port]` | None — pattern is distinctive |
| `IMEI` | 15-digit run | Required: `imei`, `international mobile equipment identity` |
| `IMSI` | 15-digit run (shares IMEI regex) | Required: `imsi`, `international mobile subscriber identity` |
| `MSISDN` | `+E.164` | Required: `msisdn`, `subscriber number`, `calling number`, `called number` |
| `MAC_ADDRESS` | Six hex pairs with `:` or `-` | None |
| `COORDINATES` | Decimal-degrees lat/long with N/S/E/W | None |
| `SITE` | `site-XX_YYY` or `ENB/BTS/…-XXXX` | Required: site/cell/base station/network element terms |

Context-anchored recognizers run against a ±50 byte window
(`defaultContextWindow`) around each regex match. The `Recognizer`
struct gains `ContextKeywords []string`; nil means "no context
required" (preserves the existing six recognizers unchanged).

**Why position-based `redact()` refactor:** Go regex has no
variable-width lookbehind, so context anchoring cannot be expressed in
the regex itself. The `redact()` function in `pii.go` was refactored
from sequential `ReplaceAllStringFunc` calls to two-phase span-collect
+ rewrite, which also enables NER overlap arbitration (§11.3).

**IMSI vs IMEI disambiguation:** both share the 15-digit shape; the
context-keyword filter at the redact pipeline decides which label
applies. When both keywords appear near the same span, registration
order (IMEI first) wins.

### 11.2 prose NER (PERSON + LOCATION)

`jdkato/prose/v2` is added as a pure-Go NER engine emitting `PERSON`
and `LOCATION` spans. Opt-in via `PII_NER_ENABLED=true`; default off so
the prose model is not allocated on installs that do not need it.

**Why prose (not spaCy / BERT / transformers):**
- Pure Go: no CGo. Single-static-binary distribution preserved.
- Bundled model: averaged-perceptron NER weights ship inside the Go
  module. No model download, no first-run bootstrap, no network at
  install time. `curl|sh` install remains one command.
- Binary size delta: 10MB → 17MB (+7MB), well under the 30MB threshold
  flagged in the implementation plan.

**Accuracy ceiling (known v1 limitation):**
- English-only.
- Decent on common Western names ("John Smith", "Jane Doe", "Barack
  Obama") and major place names ("Boston", "Paris", "New York").
- Weaker on Asian / multilingual names and unusual locations.
- Sentence-initial capitalized words are sometimes mislabeled as GPE.
- prose's `GPE` (geo-political entity) is normalized to the canonical
  `LOCATION` name internally so the redact pipeline sees a uniform
  vocabulary.
- Roughly: spaCy small ≤ prose < spaCy large < BERT.

A future v2 may add an opt-in transformer-backed engine (first-run ONNX
model download), which is explicitly out of scope here.

**Byte-offset reconstruction:** prose emits `Entity.Text` but not byte
offsets. `nerEngine.Detect` reconstructs offsets by sequentially
scanning the original text with a moving cursor; duplicates resolve to
distinct matches. Pathological cases (substring overlap with another
entity, tokenizer normalization that changes the printed form) fall
back to skipping the entity rather than emitting wrong offsets.

### 11.3 Regex + NER Merge

Regex spans are collected first against the original input; NER spans
are collected second and merged greedily via `mergeSpansGreedy`. NER
candidates that overlap any accepted regex span are dropped. Within
NER, intra-source overlaps are resolved by the same greedy step.
Mirrors loop_24's `_merge_results` non-overlap policy.

NER spans bump the same per-canonical-value counter / Summary
bookkeeping that regex spans do, so `[PERSON_1] / [PERSON_2]` (replace
mode) and Summary counts behave identically across both recognizer
sources.

### 11.4 Configuration Surface

New env var:

```bash
# Default false. When true, main.go constructs a *nerEngine and
# attaches it to PIIRedactionHook.NER. When false, no prose state is
# allocated.
PII_NER_ENABLED=true
```

Extended allowlists:
- `PII_ENABLED_ENTITIES` accepts: `Email`, `IPv4`, `IPv6`, `SSN`,
  `CreditCard`, `USPhone`, `SIP_URI`, `IMEI`, `IMSI`, `MSISDN`,
  `MAC_ADDRESS`, `COORDINATES`, `SITE`, `PERSON`, `LOCATION`.
- `PII_ENTITY_ACTIONS` accepts the same expanded entity set.

Backward compatibility is preserved: when `PII_NER_ENABLED` is unset
and `PII_ENABLED_ENTITIES` does not include any new name, behavior is
bit-identical to the pre-expansion build.

### 11.5 Threat-Model Notes

- **PERSON / LOCATION redaction is best-effort.** Unlike SSN/CreditCard
  where a regex captures all canonical forms, prose's NER will miss
  names. T-8-PII-BYPASS already documents that v1 has accepted
  recall < 100%; NER does not change this property, only marginally
  improves it for English text.
- **No leakage through the encrypt key.** EncryptKey continues to be
  redacted from `Describe()` output regardless of recognizer source.
- **No new dependencies at runtime beyond prose.** No network call,
  no model download, no file-system writes by prose at runtime.
