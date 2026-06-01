# PII Encrypt/Decrypt Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `encrypt` as a fifth peer to the existing `replace`/`mask`/`hash`/`drop` PII redaction actions, with round-trip semantics: detected PII is AES-256-GCM-encrypted on the request, ciphertext-token flows to the LLM, and the response Post-hook decrypts the tokens back to plaintext before the client sees the response.

**Architecture:** Single `PIIRedactionHook` struct grows to satisfy both `engine.PreHook` and `engine.PostHook` (mirrors the existing `LoggingHook` precedent). A new `EntityActions map[string]string` field overrides the global `Mode` per recognizer. Streaming is auto-disabled when encrypt is active. Token shape `[PII:Entity:base64url(nonce||ciphertext||tag)]` with the entity bound in GCM AAD. Key derived from any non-empty `PII_ENCRYPT_KEY` env var via SHA-256. Decrypt failures leave tokens verbatim and emit a WARN log.

**Tech Stack:** Go 1.23+ stdlib only (`crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/sha256`, `encoding/base64`, `regexp`).

**Spec:** `docs/superpowers/specs/2026-06-01-pii-encrypt-design.md`

---

## File Structure

**New files:**
- `internal/plugin/pii/encrypt.go` — AES-GCM helpers + key derivation (pure functions, no global state)
- `internal/plugin/pii/encrypt_test.go` — round-trip, AAD-binding, wrong-key, key-derivation tests

**Modified files:**
- `internal/plugin/pii/modes.go` — add `"encrypt"` case to `ApplyMode`, new trailing `encryptKey []byte` parameter
- `internal/plugin/pii/modes_test.go` — encrypt-mode round-trip test
- `internal/plugin/pii/pii.go` — add `EntityActions` / `EncryptKey` fields; add `actionFor` + `encryptActive` helpers; update `Before` (use `actionFor`, flip `req.Stream`); add `After` (decrypt sweep); add `decryptTokenRe`; update `Describe`; add `PostHook` compile-time assertion
- `internal/plugin/pii/pii_test.go` — per-entity action resolution, stream-disable, After round-trip + failure-mode tests
- `internal/config/config.go` — add `PIIEntityActions` + `PIIEncryptKey` fields; parser; validators; conditional boot-validation
- `internal/config/plugin_config_test.go` — env-parsing + boot-validation tests
- `cmd/otto-gateway/main.go` — wire new config fields into `PIIRedactionHook` literal; register same instance in `chain.Post`
- `docs/operating.md` — document `PII_ENCRYPT_KEY` and `PII_ENTITY_ACTIONS` env vars

---

## Task 1: Key derivation

**Files:**
- Create: `internal/plugin/pii/encrypt.go`
- Create: `internal/plugin/pii/encrypt_test.go`

- [ ] **Step 1: Write failing tests for `DeriveKey`**

`internal/plugin/pii/encrypt_test.go`:
```go
// PII-ENCRYPT-01 — key derivation tests. DeriveKey hashes any non-empty
// string via SHA-256 into a 32-byte AES-256-GCM key. Empty input is an
// error (encrypt cannot operate without a key).

package pii

import (
	"bytes"
	"testing"
)

func TestDeriveKey_Empty(t *testing.T) {
	_, err := DeriveKey("")
	if err == nil {
		t.Fatal("DeriveKey(\"\"): expected error, got nil")
	}
}

func TestDeriveKey_Length(t *testing.T) {
	k, err := DeriveKey("any-string")
	if err != nil {
		t.Fatalf("DeriveKey: unexpected error: %v", err)
	}
	if len(k) != 32 {
		t.Errorf("DeriveKey length: got %d, want 32", len(k))
	}
}

func TestDeriveKey_Deterministic(t *testing.T) {
	a, _ := DeriveKey("correct-horse-battery-staple")
	b, _ := DeriveKey("correct-horse-battery-staple")
	if !bytes.Equal(a, b) {
		t.Error("DeriveKey is not deterministic across calls with the same input")
	}
}

func TestDeriveKey_DiffersByInput(t *testing.T) {
	a, _ := DeriveKey("hello")
	b, _ := DeriveKey("Hello")
	if bytes.Equal(a, b) {
		t.Error("DeriveKey: case-different inputs produced identical keys")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run TestDeriveKey -v`
Expected: FAIL with `undefined: DeriveKey`.

- [ ] **Step 3: Implement `DeriveKey`**

`internal/plugin/pii/encrypt.go`:
```go
// Package pii encrypt — AES-256-GCM round-trip support for the
// PII_REDACTION_MODE=encrypt action (plus per-entity EntityActions
// overrides). Pure helpers; no package-level state.
//
// Spec: docs/superpowers/specs/2026-06-01-pii-encrypt-design.md
//
// Token shape on the wire toward the LLM:
//   [PII:Entity:base64url(nonce || ciphertext || tag)]
// where Entity is bound as GCM Associated Data so a relabeled token
// fails to decrypt (Open returns an error and the Post hook leaves
// the token verbatim).

package pii

import (
	"crypto/sha256"
	"errors"
)

// DeriveKey returns the 32-byte AES-256-GCM key derived from any
// non-empty string via SHA-256. Deterministic across restarts so the
// same PII_ENCRYPT_KEY env value reproduces the same key — tokens
// encrypted before a restart still decrypt after.
//
// SHA-256 (not scrypt/argon2) is chosen because our threat model is
// "LLM provider sees plaintext", not offline ciphertext brute-force.
// See spec §3.5 for rationale.
func DeriveKey(envValue string) ([]byte, error) {
	if envValue == "" {
		return nil, errors.New("pii: PII_ENCRYPT_KEY is empty")
	}
	sum := sha256.Sum256([]byte(envValue))
	return sum[:], nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/plugin/pii/ -run TestDeriveKey -v`
Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/pii/encrypt.go internal/plugin/pii/encrypt_test.go
git commit -m "feat(pii): add DeriveKey for any-string -> 32B AES key (encrypt mode)"
```

---

## Task 2: AES-GCM encrypt / decrypt

**Files:**
- Modify: `internal/plugin/pii/encrypt.go`
- Modify: `internal/plugin/pii/encrypt_test.go`

- [ ] **Step 1: Append failing tests for `EncryptValue` / `DecryptToken`**

Append to `internal/plugin/pii/encrypt_test.go`:
```go
// PII-ENCRYPT-02 — round-trip + AAD + wrong-key + nonce-randomness.

import "strings"  // (add to existing import block)

func TestEncryptValue_TokenShape(t *testing.T) {
	k, _ := DeriveKey("test-key")
	tok, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	if !strings.HasPrefix(tok, "[PII:Email:") {
		t.Errorf("token prefix: got %q, want [PII:Email:...]", tok)
	}
	if !strings.HasSuffix(tok, "]") {
		t.Errorf("token suffix: got %q, want trailing ]", tok)
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	k, _ := DeriveKey("test-key")
	tok, _ := EncryptValue(k, "Email", "corey@cmetech.io")
	// Strip "[PII:Email:" prefix and "]" suffix to recover the payload.
	payload := tok[len("[PII:Email:") : len(tok)-1]
	got, err := DecryptToken(k, "Email", payload)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if got != "corey@cmetech.io" {
		t.Errorf("round-trip: got %q, want %q", got, "corey@cmetech.io")
	}
}

func TestDecryptToken_AADMismatch(t *testing.T) {
	k, _ := DeriveKey("test-key")
	tok, _ := EncryptValue(k, "Email", "corey@cmetech.io")
	payload := tok[len("[PII:Email:") : len(tok)-1]
	// Attempt decrypt with the WRONG entity name. AAD binding must fail.
	if _, err := DecryptToken(k, "SSN", payload); err == nil {
		t.Error("DecryptToken: AAD mismatch should fail; got nil error")
	}
}

func TestDecryptToken_WrongKey(t *testing.T) {
	k1, _ := DeriveKey("key-one")
	k2, _ := DeriveKey("key-two")
	tok, _ := EncryptValue(k1, "Email", "corey@cmetech.io")
	payload := tok[len("[PII:Email:") : len(tok)-1]
	if _, err := DecryptToken(k2, "Email", payload); err == nil {
		t.Error("DecryptToken: wrong key should fail; got nil error")
	}
}

func TestDecryptToken_BadBase64(t *testing.T) {
	k, _ := DeriveKey("test-key")
	if _, err := DecryptToken(k, "Email", "not-valid-base64!!!"); err == nil {
		t.Error("DecryptToken: malformed payload should fail; got nil error")
	}
}

func TestEncryptValue_SamePlaintextDifferentCiphertext(t *testing.T) {
	k, _ := DeriveKey("test-key")
	a, _ := EncryptValue(k, "Email", "corey@cmetech.io")
	b, _ := EncryptValue(k, "Email", "corey@cmetech.io")
	if a == b {
		t.Error("nonce should randomize each encryption; got identical tokens for the same plaintext")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run "TestEncrypt|TestDecrypt" -v`
Expected: FAIL with `undefined: EncryptValue` / `undefined: DecryptToken`.

- [ ] **Step 3: Implement `EncryptValue` and `DecryptToken`**

Append to `internal/plugin/pii/encrypt.go`:
```go
import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// tokenPrefix and tokenSuffix bracket the wire token. Square brackets
// (NOT angle brackets) per commit a3160e1 — angle brackets cause
// kiro-cli / Claude to treat the marker as an opening XML tag and
// hang the ACP prompt until the 120s timeout.
const (
	tokenPrefix = "[PII:"
	tokenSuffix = "]"
)

// EncryptValue encrypts plaintext under key with AES-256-GCM, binds
// entity as Associated Data (so a relabeled token fails Open), and
// returns the full wire-shaped token "[PII:<entity>:<base64url>]".
// Each call generates a fresh nonce, so two calls with the same
// plaintext produce different tokens (both decrypt correctly).
func EncryptValue(key []byte, entity, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("pii: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("pii: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("pii: rand: %w", err)
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), []byte(entity))
	blob := make([]byte, 0, len(nonce)+len(ct))
	blob = append(blob, nonce...)
	blob = append(blob, ct...)
	payload := base64.RawURLEncoding.EncodeToString(blob)
	return tokenPrefix + entity + ":" + payload + tokenSuffix, nil
}

// DecryptToken inverts EncryptValue. payload is the base64url-encoded
// blob between "[PII:<entity>:" and "]". entity is passed as AAD and
// must match what was used at encrypt time. Returns an error on any
// failure: bad base64, payload too short for the nonce, or GCM Open
// failure (wrong key, AAD mismatch, tag corruption).
func DecryptToken(key []byte, entity, payload string) (string, error) {
	blob, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("pii: bad base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("pii: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("pii: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("pii: payload too short")
	}
	nonce, ct := blob[:ns], blob[ns:]
	pt, err := gcm.Open(nil, nonce, ct, []byte(entity))
	if err != nil {
		return "", fmt.Errorf("pii: gcm open: %w", err)
	}
	return string(pt), nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/plugin/pii/ -run "TestEncrypt|TestDecrypt" -v`
Expected: all six PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/pii/encrypt.go internal/plugin/pii/encrypt_test.go
git commit -m "feat(pii): add AES-256-GCM EncryptValue/DecryptToken with entity-in-AAD"
```

---

## Task 3: Extend `ApplyMode` with the encrypt case

**Files:**
- Modify: `internal/plugin/pii/modes.go`
- Modify: `internal/plugin/pii/modes_test.go`
- Modify: `internal/plugin/pii/pii.go` (one-line call-site update)

- [ ] **Step 1: Write failing test for `ApplyMode("encrypt", ...)`**

Append to `internal/plugin/pii/modes_test.go`:
```go
// PII-ENCRYPT-03 — ApplyMode encrypt case. Round-trips through
// EncryptValue / DecryptToken via the wire-token regex parsing.

func TestApplyMode_Encrypt(t *testing.T) {
	key, _ := DeriveKey("test-key")
	tok := ApplyMode("encrypt", "Email", "corey@cmetech.io", 0, nil, key)
	if !strings.HasPrefix(tok, "[PII:Email:") {
		t.Fatalf("encrypt token shape: got %q", tok)
	}
	// Round-trip: strip prefix/suffix, decrypt, expect plaintext.
	payload := tok[len("[PII:Email:") : len(tok)-1]
	got, err := DecryptToken(key, "Email", payload)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "corey@cmetech.io" {
		t.Errorf("decrypt: got %q, want %q", got, "corey@cmetech.io")
	}
}

func TestApplyMode_Encrypt_EmptyKeyFailSafe(t *testing.T) {
	// Empty key would make EncryptValue fail; ApplyMode logs warn and
	// returns the plaintext (visible rather than silent broken token).
	got := ApplyMode("encrypt", "Email", "corey@cmetech.io", 0, nil, nil)
	if got != "corey@cmetech.io" {
		t.Errorf("encrypt fail-safe: got %q, want plaintext fallback %q", got, "corey@cmetech.io")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run TestApplyMode_Encrypt -v`
Expected: FAIL — existing `ApplyMode` signature has 5 parameters; passing 6 won't compile.

- [ ] **Step 3: Extend `ApplyMode` signature and add the encrypt arm**

In `internal/plugin/pii/modes.go`, change the function signature and add the case:
```go
// ApplyMode dispatches on mode and returns the redacted token for a
// single recognizer match. Counter is the per-request occurrence
// number for this entity (1, 2, 3, ...); a counter of 0 emits the
// non-suffixed "[ENTITY]" form.
//
// encryptKey is the 32-byte AES-256-GCM key used by the "encrypt"
// mode (nil for all other modes). When mode=="encrypt" and the key
// is unusable (nil or wrong length), the function logs a warning
// and returns the PLAINTEXT — visible failure is preferable to
// emitting a broken token that the Post hook cannot decrypt.
//
// Unknown modes log a warning and fall back to "replace" — defense
// in depth against a slice-5 config validation regression that might
// let a typo slip through. NEVER panics on an unknown mode.
func ApplyMode(mode, entity, value string, counter int, hashKey, encryptKey []byte) string {
	entUpper := strings.ToUpper(entity)
	switch mode {
	case "replace":
		if counter > 0 {
			return fmt.Sprintf("[%s_%d]", entUpper, counter)
		}
		return fmt.Sprintf("[%s]", entUpper)
	case "mask":
		return maskValue(value)
	case "hash":
		return fmt.Sprintf("[%s:h-%s]", entUpper, hashTag(hashKey, value))
	case "drop":
		return ""
	case "encrypt":
		tok, err := EncryptValue(encryptKey, entity, value)
		if err != nil {
			slog.Default().Warn(
				"pii.ApplyMode: encrypt failed, leaving plaintext",
				"entity", entity, "err", err,
			)
			return value
		}
		return tok
	default:
		slog.Default().Warn(
			"pii.ApplyMode: unknown mode, falling back to replace",
			"mode", mode,
		)
		return ApplyMode("replace", entity, value, counter, hashKey, encryptKey)
	}
}
```

- [ ] **Step 4: Update the existing `ApplyMode` call site in `pii.go`**

In `internal/plugin/pii/pii.go`, locate the call inside `redact`:
```go
return ApplyMode(h.Mode, r.Name, match, n, h.HashKey)
```
Change to:
```go
return ApplyMode(h.Mode, r.Name, match, n, h.HashKey, h.EncryptKey)
```
(`EncryptKey` doesn't exist yet — that's OK; this task only needs the call site updated. Task 4 adds the field.)

Update the 16 pre-existing `ApplyMode` call sites in `internal/plugin/pii/modes_test.go` to pass a trailing `nil` for the new `encryptKey` parameter. The two NEW tests added in Step 1 (`TestApplyMode_Encrypt` and `TestApplyMode_Encrypt_EmptyKeyFailSafe`) already use the 6-arg shape — leave them alone.

Find every line matching this pattern in `modes_test.go` (16 of them):
```bash
grep -n "ApplyMode(" internal/plugin/pii/modes_test.go
```

Each one currently has the 5-arg shape and ends in `, nil)`, `, testHashKey)`, or `, []byte("..."))`. Insert `, nil` before the final `)` on every line EXCEPT the two new test functions (lines inside `TestApplyMode_Encrypt*`). Concretely, the edit pattern per line:

| Before | After |
|---|---|
| `ApplyMode("replace", "Email", "x@y.z", 0, nil)` | `ApplyMode("replace", "Email", "x@y.z", 0, nil, nil)` |
| `ApplyMode("hash", "SSN", "...", 0, testHashKey)` | `ApplyMode("hash", "SSN", "...", 0, testHashKey, nil)` |

Verify with `go build ./internal/plugin/pii/` — it must compile cleanly before running tests.

- [ ] **Step 5: Add the field to keep this task self-contained**

In `internal/plugin/pii/pii.go` `PIIRedactionHook` struct, add ONE field (the full `EntityActions` lives in Task 4):
```go
	// EncryptKey is the 32-byte AES-256-GCM key for the "encrypt" action
	// (Mode=="encrypt" or EntityActions[X]=="encrypt"). Nil when encrypt is
	// not active. Boot validation guarantees non-nil when encrypt IS active.
	EncryptKey []byte
```

- [ ] **Step 6: Run all PII tests**

Run: `go test ./internal/plugin/pii/ -v`
Expected: PASS. Existing tests still green (added `nil` trailing arg); new encrypt tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/plugin/pii/modes.go internal/plugin/pii/modes_test.go internal/plugin/pii/pii.go
git commit -m "feat(pii): add encrypt case to ApplyMode + EncryptKey field"
```

---

## Task 4: `EntityActions` + `actionFor` + `encryptActive` helpers

**Files:**
- Modify: `internal/plugin/pii/pii.go`
- Modify: `internal/plugin/pii/pii_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/plugin/pii/pii_test.go`:
```go
// PII-ENCRYPT-04 — per-entity action resolution and encryptActive predicate.

func TestActionFor_FallbackToMode(t *testing.T) {
	h := &PIIRedactionHook{Mode: "mask"}
	if got := h.actionFor("Email"); got != "mask" {
		t.Errorf("actionFor with no override: got %q, want %q", got, "mask")
	}
}

func TestActionFor_OverrideWins(t *testing.T) {
	h := &PIIRedactionHook{
		Mode:          "mask",
		EntityActions: map[string]string{"Email": "encrypt"},
	}
	if got := h.actionFor("Email"); got != "encrypt" {
		t.Errorf("actionFor with override: got %q, want %q", got, "encrypt")
	}
	if got := h.actionFor("SSN"); got != "mask" {
		t.Errorf("actionFor unlisted entity: got %q, want fallback %q", got, "mask")
	}
}

func TestEncryptActive_GlobalMode(t *testing.T) {
	h := &PIIRedactionHook{Mode: "encrypt"}
	if !h.encryptActive() {
		t.Error("encryptActive: Mode=encrypt should report active")
	}
}

func TestEncryptActive_EntityOverride(t *testing.T) {
	h := &PIIRedactionHook{
		Mode:          "replace",
		EntityActions: map[string]string{"Email": "encrypt"},
	}
	if !h.encryptActive() {
		t.Error("encryptActive: any encrypt in EntityActions should report active")
	}
}

func TestEncryptActive_Inactive(t *testing.T) {
	h := &PIIRedactionHook{
		Mode:          "mask",
		EntityActions: map[string]string{"Email": "drop", "SSN": "hash"},
	}
	if h.encryptActive() {
		t.Error("encryptActive: no encrypt anywhere should report inactive")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run "TestActionFor|TestEncryptActive" -v`
Expected: FAIL — `actionFor` / `encryptActive` / `EntityActions` undefined.

- [ ] **Step 3: Add field + helpers to `pii.go`**

In `internal/plugin/pii/pii.go`, add the field to `PIIRedactionHook` struct (alongside `EncryptKey` from Task 3):
```go
	// EntityActions overrides the global Mode per recognizer Name.
	// e.g., {"Email":"encrypt","SSN":"mask"} → Email matches use encrypt,
	// SSN matches use mask, all other entities fall back to Mode.
	// Empty map reproduces today's behavior exactly (Mode applies to all).
	EntityActions map[string]string
```

Add these helper methods (next to `activeEntityNames` / `activeRecognizers`):
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
// encrypt mode. Used by Before's stream-disable side effect and by
// After's no-op fast path. Cheap O(len(EntityActions)).
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

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/plugin/pii/ -run "TestActionFor|TestEncryptActive" -v`
Expected: all five PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/pii/pii.go internal/plugin/pii/pii_test.go
git commit -m "feat(pii): add EntityActions + actionFor + encryptActive helpers"
```

---

## Task 5: Update `Before` — use `actionFor`, flip `req.Stream`

**Files:**
- Modify: `internal/plugin/pii/pii.go`
- Modify: `internal/plugin/pii/pii_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/plugin/pii/pii_test.go`:
```go
import "otto-gateway/internal/canonical"  // ensure imported

// PII-ENCRYPT-05 — Before flips req.Stream when encrypt is active.

func TestBefore_StreamDisabledWhenEncryptActive(t *testing.T) {
	k, _ := DeriveKey("test")
	h := &PIIRedactionHook{
		Recognizers: Recognizers,
		Enabled:     true,
		Mode:        "encrypt",
		EncryptKey:  k,
	}
	req := &canonical.ChatRequest{Stream: true}
	if _, err := h.Before(context.Background(), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if req.Stream {
		t.Error("Before: req.Stream should be false after Before when encrypt is active")
	}
}

func TestBefore_StreamUnchangedWhenEncryptInactive(t *testing.T) {
	h := &PIIRedactionHook{
		Recognizers: Recognizers,
		Enabled:     true,
		Mode:        "replace",
	}
	req := &canonical.ChatRequest{Stream: true}
	if _, err := h.Before(context.Background(), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if !req.Stream {
		t.Error("Before: req.Stream should remain true when encrypt is NOT active")
	}
}

func TestBefore_PerEntityActionResolution(t *testing.T) {
	k, _ := DeriveKey("test")
	h := &PIIRedactionHook{
		Recognizers:   Recognizers,
		Enabled:       true,
		Mode:          "mask",
		EncryptKey:    k,
		EntityActions: map[string]string{"Email": "encrypt"},
	}
	req := &canonical.ChatRequest{
		System: "contact corey@cmetech.io and 123-45-6789",
	}
	if _, err := h.Before(context.Background(), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	// Email should be encrypted (token shape).
	if !strings.Contains(req.System, "[PII:Email:") {
		t.Errorf("Email should be encrypted: got System=%q", req.System)
	}
	// SSN should be masked (contains '*'), NOT encrypted.
	if strings.Contains(req.System, "[PII:SSN:") {
		t.Errorf("SSN should be masked, not encrypted: got System=%q", req.System)
	}
	if !strings.Contains(req.System, "*") {
		t.Errorf("SSN mask should contain '*': got System=%q", req.System)
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run TestBefore_ -v`
Expected: FAIL — `req.Stream` is unchanged (no stream-disable logic yet) and the per-entity test sees only `Mode=="mask"` applied uniformly.

- [ ] **Step 3: Update `Before` in `pii.go`**

In `internal/plugin/pii/pii.go`, modify `Before`:
```go
func (h *PIIRedactionHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if !h.Enabled || req == nil {
		return nil, nil
	}

	// Encrypt round-trip needs the full response buffered before decrypt
	// can run, so we flip req.Stream off here. The Post hook (After) runs
	// in engine.Collect on the aggregated response — streaming branches
	// would bypass it because their bytes hit the wire before Collect
	// finishes. Spec §3.1.
	if h.encryptActive() && req.Stream {
		req.Stream = false
		h.logger().Info("pii.encrypt.streaming_disabled",
			"reason", "decrypt requires aggregated response",
		)
	}

	summary, _ := SummaryFromContext(ctx)
	if summary == nil {
		summary = NewSummary()
	}

	recs := h.activeRecognizers()
	counters := make(map[string]int)
	nextN := make(map[string]int)

	redact := func(s string) string {
		out := s
		for _, r := range recs {
			out = r.Pattern.ReplaceAllStringFunc(out, func(match string) string {
				if r.Validate != nil && !r.Validate(match) {
					return match
				}
				key := r.Name + "|" + canonicalForm(match)
				n, seen := counters[key]
				if !seen {
					nextN[r.Name]++
					n = nextN[r.Name]
					counters[key] = n
				}
				summary.Add(r.Name)
				// Per-entity action resolution: EntityActions[r.Name]
				// wins, else h.Mode (Task 4 helper).
				return ApplyMode(h.actionFor(r.Name), r.Name, match, n, h.HashKey, h.EncryptKey)
			})
		}
		return out
	}

	req.System = redact(req.System)

	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			cp := &req.Messages[i].Content[j]
			switch cp.Kind {
			case canonical.ContentKindText:
				cp.Text = redact(cp.Text)
			case canonical.ContentKindToolUse:
				if cp.ToolUse == nil || cp.ToolUse.Input == nil {
					continue
				}
				walked := WalkStrings(cp.ToolUse.Input, redact)
				if m, ok := walked.(map[string]any); ok {
					cp.ToolUse.Input = m
				}
			case canonical.ContentKindToolResult:
				if cp.ToolResult == nil {
					continue
				}
				cp.ToolResult.Content = redact(cp.ToolResult.Content)
			}
		}
	}

	h.logger().Debug("pii.redact.done",
		"active_recognizers", len(recs),
		"mode", h.Mode,
	)

	return nil, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/plugin/pii/ -v`
Expected: all tests PASS — new Before tests green, existing Before tests still green.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/pii/pii.go internal/plugin/pii/pii_test.go
git commit -m "feat(pii): Before uses actionFor + auto-disables streaming on encrypt"
```

---

## Task 6: `After` (PostHook decrypt sweep) + regex + compile assertion

**Files:**
- Modify: `internal/plugin/pii/pii.go`
- Modify: `internal/plugin/pii/pii_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/plugin/pii/pii_test.go`:
```go
// PII-ENCRYPT-06 — After (PostHook) decrypt sweep.

func TestAfter_NoopWhenEncryptInactive(t *testing.T) {
	h := &PIIRedactionHook{Enabled: true, Mode: "replace"}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "hello [PII:Email:fakepayload]"},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	// No-op: text is unchanged even though a token-shaped string is present.
	if resp.Message.Content[0].Text != "hello [PII:Email:fakepayload]" {
		t.Errorf("After should be no-op when encrypt inactive: got %q", resp.Message.Content[0].Text)
	}
}

func TestAfter_RoundTripDecrypt(t *testing.T) {
	k, _ := DeriveKey("test")
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	tok, _ := EncryptValue(k, "Email", "corey@cmetech.io")
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Sure, I'll email " + tok + " for you."},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	want := "Sure, I'll email corey@cmetech.io for you."
	if resp.Message.Content[0].Text != want {
		t.Errorf("After decrypt: got %q, want %q", resp.Message.Content[0].Text, want)
	}
}

func TestAfter_MangledTokenLeftInPlace(t *testing.T) {
	k, _ := DeriveKey("test")
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	// Hand-crafted shape-valid but cryptographically-garbage token.
	garbage := "[PII:Email:AAAAAAAAAAAAAAAA]"
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "see " + garbage + " for details"},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	if !strings.Contains(resp.Message.Content[0].Text, garbage) {
		t.Errorf("mangled token should be left verbatim: got %q", resp.Message.Content[0].Text)
	}
}

func TestAfter_MultipleTokens(t *testing.T) {
	k, _ := DeriveKey("test")
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	t1, _ := EncryptValue(k, "Email", "alice@example.com")
	t2, _ := EncryptValue(k, "Email", "bob@example.com")
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "From " + t1 + " to " + t2},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	want := "From alice@example.com to bob@example.com"
	if resp.Message.Content[0].Text != want {
		t.Errorf("After multi-token: got %q, want %q", resp.Message.Content[0].Text, want)
	}
}

func TestAfter_SkipsNonTextParts(t *testing.T) {
	k, _ := DeriveKey("test")
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindThinking, Text: "this should not be scanned [PII:Email:fake]"},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	// Non-text parts are skipped — text field stays verbatim.
	if !strings.Contains(resp.Message.Content[0].Text, "[PII:Email:fake]") {
		t.Errorf("non-Text part should be skipped: got %q", resp.Message.Content[0].Text)
	}
}

func TestAfter_CompileTimePostHookSatisfied(t *testing.T) {
	// Compile-time guard: var _ engine.PostHook = (*PIIRedactionHook)(nil)
	// will fail to BUILD if the After signature drifts. This test is a
	// belt-and-suspenders marker that exercises the interface at runtime.
	var _ interface {
		After(context.Context, *canonical.ChatRequest, *canonical.ChatResponse) error
	} = (*PIIRedactionHook)(nil)
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run TestAfter_ -v`
Expected: FAIL — `After` method undefined.

- [ ] **Step 3: Add `decryptTokenRe`, `After`, and the PostHook compile assertion to `pii.go`**

At package-level scope in `internal/plugin/pii/pii.go`, alongside other vars:
```go
// decryptTokenRe matches the encrypt-mode wire token shape
// "[PII:<entity>:<base64url-payload>]". Group 1 = entity, Group 2 =
// payload. base64url alphabet is [A-Za-z0-9_-], unpadded — see
// EncryptValue (encrypt.go). Pre-compiled at package init.
var decryptTokenRe = regexp.MustCompile(`\[PII:([A-Za-z]+):([A-Za-z0-9_-]+)\]`)
```

Add the `regexp` import to the file's import block.

Add the `After` method after `Before`:
```go
// After is the PostHook entry for the encrypt round-trip. It scans
// every ContentKindText part in resp.Message.Content for
// "[PII:<entity>:<base64url>]" tokens and decrypts each via
// DecryptToken. Failures (mangled token shape, bad base64, GCM Open
// error from wrong key / AAD mismatch / corruption) leave the token
// verbatim and emit a WARN — the client sees a visible defect, not
// a silent lie.
//
// No-op when encryptActive() is false (engine.Collect still ranges
// PostHooks; this hook just returns nil immediately).
//
// Non-text content parts (image, thinking, tool_use, tool_result) are
// skipped — encrypt round-trip is scoped to Text only in v1. The
// tool_use/tool_result surfaces ARE encrypted on the Pre side (Before
// walks them via WalkStrings) but the kiro-cli response surface is
// text-only for the assistant turn, so decrypt only needs Text.
func (h *PIIRedactionHook) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	if !h.Enabled || resp == nil || !h.encryptActive() {
		return nil
	}
	for i := range resp.Message.Content {
		cp := &resp.Message.Content[i]
		if cp.Kind != canonical.ContentKindText {
			continue
		}
		cp.Text = decryptTokenRe.ReplaceAllStringFunc(cp.Text, func(match string) string {
			sub := decryptTokenRe.FindStringSubmatch(match)
			if len(sub) != 3 {
				h.logger().Warn("pii.decrypt.failed", "reason", "bad_token_shape")
				return match
			}
			entity, payload := sub[1], sub[2]
			pt, err := DecryptToken(h.EncryptKey, entity, payload)
			if err != nil {
				h.logger().Warn("pii.decrypt.failed",
					"entity", entity, "err", err)
				return match
			}
			return pt
		})
	}
	return nil
}

// Compile-time PostHook interface satisfaction (mirrors the existing
// PreHook line below). Drift in engine.PostHook surfaces here at the
// right blame target.
var _ engine.PostHook = (*PIIRedactionHook)(nil)
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/plugin/pii/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/pii/pii.go internal/plugin/pii/pii_test.go
git commit -m "feat(pii): add After PostHook for decrypt sweep + decryptTokenRe"
```

---

## Task 7: Update `Describe` for the dual-interface hook

**Files:**
- Modify: `internal/plugin/pii/pii.go`
- Modify: `internal/plugin/pii/pii_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/plugin/pii/pii_test.go`:
```go
// PII-ENCRYPT-07 — Describe surfaces both Pre and Post kinds, plus
// decrypt_active and entity_actions for operator visibility on
// /health/hooks. EncryptKey is NEVER published (T-8-LEAK extension).

func TestDescribe_KindIncludesPost(t *testing.T) {
	h := &PIIRedactionHook{Recognizers: Recognizers, Mode: "encrypt"}
	kind, _ := h.Describe()
	if kind != "Pre,Post" {
		t.Errorf("Describe kind: got %q, want %q", kind, "Pre,Post")
	}
}

func TestDescribe_DecryptActiveFlag(t *testing.T) {
	tests := []struct {
		name        string
		hook        *PIIRedactionHook
		wantDecrypt bool
	}{
		{"mode=encrypt", &PIIRedactionHook{Recognizers: Recognizers, Mode: "encrypt"}, true},
		{"entity-override", &PIIRedactionHook{Recognizers: Recognizers, Mode: "replace", EntityActions: map[string]string{"Email": "encrypt"}}, true},
		{"no encrypt", &PIIRedactionHook{Recognizers: Recognizers, Mode: "mask"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg := tc.hook.Describe()
			got, _ := cfg["decrypt_active"].(bool)
			if got != tc.wantDecrypt {
				t.Errorf("decrypt_active: got %v, want %v", got, tc.wantDecrypt)
			}
		})
	}
}

func TestDescribe_NeverPublishesEncryptKey(t *testing.T) {
	h := &PIIRedactionHook{
		Recognizers: Recognizers,
		Mode:        "encrypt",
		EncryptKey:  []byte("SECRET-SHOULD-NOT-LEAK"),
	}
	_, cfg := h.Describe()
	// Walk every published value as JSON; the key MUST NOT appear.
	for k, v := range cfg {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, "SECRET-SHOULD-NOT-LEAK") {
			t.Errorf("Describe published EncryptKey via field %q: %q", k, s)
		}
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/plugin/pii/ -run TestDescribe_ -v`
Expected: FAIL — `kind` is `"Pre"`, `decrypt_active` field missing.

- [ ] **Step 3: Update `Describe`**

In `internal/plugin/pii/pii.go`:
```go
// Describe publishes the hook's safe-to-publish config for /health/hooks
// (OBSV-04). Kind is "Pre,Post" — this hook is dual-interface (matches
// the LoggingHook precedent). The encrypt round-trip puts the same
// instance in chain.Pre (encrypt) and chain.Post (decrypt).
//
// HashKey and EncryptKey are NEVER published (T-8-LEAK).
func (h *PIIRedactionHook) Describe() (kind string, config map[string]any) {
	entities := h.activeEntityNames()
	return "Pre,Post", map[string]any{
		"enabled":        h.Enabled,
		"mode":           h.Mode,
		"entities":       entities,
		"decrypt_active": h.encryptActive(),
		"entity_actions": h.EntityActions, // safe: action names only
	}
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/plugin/pii/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/pii/pii.go internal/plugin/pii/pii_test.go
git commit -m "feat(pii): Describe surfaces Pre,Post kind + decrypt_active + entity_actions"
```

---

## Task 8: `PIIEntityActions` config field + parser + validator

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/plugin_config_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/config/plugin_config_test.go`:
```go
// PII-ENCRYPT-08 — PII_ENTITY_ACTIONS parser + validator.

func TestLoad_PIIEntityActions_Empty(t *testing.T) {
	t.Setenv("PII_ENTITY_ACTIONS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PIIEntityActions) != 0 {
		t.Errorf("empty env: got %v, want empty map", cfg.PIIEntityActions)
	}
}

func TestLoad_PIIEntityActions_Parses(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_REDACTION_MODE", "mask")
	t.Setenv("PII_ENTITY_ACTIONS", "Email:encrypt,SSN:drop")
	t.Setenv("PII_ENCRYPT_KEY", "any-string") // required because encrypt is active
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.PIIEntityActions["Email"]; got != "encrypt" {
		t.Errorf("Email action: got %q, want %q", got, "encrypt")
	}
	if got := cfg.PIIEntityActions["SSN"]; got != "drop" {
		t.Errorf("SSN action: got %q, want %q", got, "drop")
	}
}

func TestLoad_PIIEntityActions_UnknownEntity(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENTITY_ACTIONS", "Emial:encrypt") // typo
	t.Setenv("PII_ENCRYPT_KEY", "any-string")
	_, err := Load()
	if err == nil {
		t.Fatal("expected boot error for unknown entity, got nil")
	}
	if !strings.Contains(err.Error(), "Emial") {
		t.Errorf("error should name the offending entity: got %v", err)
	}
}

func TestLoad_PIIEntityActions_UnknownAction(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENTITY_ACTIONS", "Email:shred") // unknown action
	_, err := Load()
	if err == nil {
		t.Fatal("expected boot error for unknown action, got nil")
	}
	if !strings.Contains(err.Error(), "shred") {
		t.Errorf("error should name the offending action: got %v", err)
	}
}

func TestLoad_PIIEntityActions_MalformedPair(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENTITY_ACTIONS", "Email-encrypt") // missing colon
	_, err := Load()
	if err == nil {
		t.Fatal("expected boot error for malformed pair, got nil")
	}
}
```

(Add `import "strings"` if not already present in this test file.)

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/config/ -run TestLoad_PIIEntityActions -v`
Expected: FAIL — `cfg.PIIEntityActions` undefined.

- [ ] **Step 3: Add field, parser, validator to `config.go`**

In the `Config` struct (after `PIIHashKey`), add:
```go
	// PIIEntityActions is the per-entity action override map parsed from
	// PII_ENTITY_ACTIONS. Empty map reproduces today's behavior (global
	// PIIRedactionMode applies to every recognizer). When non-empty,
	// EntityActions[entity] wins over PIIRedactionMode for the named
	// entities. Unknown entity names or unknown action values cause
	// Load() to return an error. The five allowed action values are
	// "replace" | "mask" | "hash" | "drop" | "encrypt".
	PIIEntityActions map[string]string

	// PIIEncryptKey is the raw PII_ENCRYPT_KEY env value (any non-empty
	// string). It is passed through to the slice-5 wiring, which calls
	// pii.DeriveKey to produce the 32-byte AES-256-GCM key. Required
	// when encrypt is active (PII_REDACTION_MODE=encrypt OR any value
	// in PII_ENTITY_ACTIONS is "encrypt"); empty otherwise. Boot error
	// when encrypt is active and this is empty.
	PIIEncryptKey string
```

Add the parser block in `Load` (after the existing `piiHashKey` block, around line 312):
```go
	piiEntityActions, err := parsePIIEntityActions(getEnvStr("PII_ENTITY_ACTIONS", ""))
	if err != nil {
		errs = append(errs, fmt.Errorf("PII_ENTITY_ACTIONS: %w", err))
	}

	piiEncryptKey := getEnvStr("PII_ENCRYPT_KEY", "")
	// Encrypt is "active" if the global mode is encrypt OR any override
	// is encrypt. When active, the key MUST be non-empty — there is no
	// silent fallback that would produce decryptable tokens.
	encryptActive := piiMode == "encrypt"
	for _, a := range piiEntityActions {
		if a == "encrypt" {
			encryptActive = true
			break
		}
	}
	if encryptActive && piiEncryptKey == "" {
		errs = append(errs, fmt.Errorf("PII_ENCRYPT_KEY: required when encrypt is active (PII_REDACTION_MODE=encrypt or any PII_ENTITY_ACTIONS value is encrypt)"))
	}
```

Wire both into the `return Config{...}` block at the bottom of `Load`:
```go
		PIIEntityActions:     piiEntityActions,
		PIIEncryptKey:        piiEncryptKey,
```

Also update `validatePIIMode` to accept `"encrypt"` as a fifth allowed value:
```go
func validatePIIMode(m string) error {
	switch m {
	case "replace", "mask", "hash", "drop", "encrypt":
		return nil
	default:
		return fmt.Errorf("unknown mode %q (allowed: replace, mask, hash, drop, encrypt)", m)
	}
}
```

Add the new parser function next to `validatePIIMode` / `validatePIIEntities`:
```go
// parsePIIEntityActions parses the PII_ENTITY_ACTIONS env value.
// Shape: "Entity:action,Entity:action,..." e.g. "Email:encrypt,SSN:drop".
// Returns (nil, nil) for an empty input. Validates every entity name
// against the canonical six-entity set and every action against the
// five-action set.
func parsePIIEntityActions(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	allowedEntities := map[string]struct{}{
		"Email": {}, "IPv4": {}, "IPv6": {},
		"SSN": {}, "CreditCard": {}, "USPhone": {},
	}
	allowedActions := map[string]struct{}{
		"replace": {}, "mask": {}, "hash": {}, "drop": {}, "encrypt": {},
	}
	out := make(map[string]string)
	var errs []error
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			errs = append(errs, fmt.Errorf("malformed pair %q (expected Entity:action)", pair))
			continue
		}
		entity, action := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if _, ok := allowedEntities[entity]; !ok {
			errs = append(errs, fmt.Errorf("unknown entity %q (allowed: Email, IPv4, IPv6, SSN, CreditCard, USPhone)", entity))
			continue
		}
		if _, ok := allowedActions[action]; !ok {
			errs = append(errs, fmt.Errorf("unknown action %q for entity %q (allowed: replace, mask, hash, drop, encrypt)", action, entity))
			continue
		}
		out[entity] = action
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/config/ -v`
Expected: all PASS (including pre-existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/plugin_config_test.go
git commit -m "feat(config): PII_ENTITY_ACTIONS + PII_ENCRYPT_KEY env vars w/ validation"
```

---

## Task 9: Boot-validation predicate test (encrypt active + empty key)

**Files:**
- Modify: `internal/config/plugin_config_test.go`

This is a dedicated, focused test for the conditional boot-failure matrix from spec §3.5. The Task 8 tests touched it incidentally; this task pins each row of the table.

- [ ] **Step 1: Add the matrix-driven boot test**

Append to `internal/config/plugin_config_test.go`:
```go
// PII-ENCRYPT-09 — boot validation matrix per spec §3.5.

func TestLoad_EncryptKeyBootValidation(t *testing.T) {
	tests := []struct {
		name      string
		enabled   string
		mode      string
		actions   string
		key       string
		wantError bool
	}{
		{"pii off — key unset", "false", "replace", "", "", false},
		{"pii on, mode=replace, no encrypt anywhere — key irrelevant", "true", "replace", "", "", false},
		{"pii on, mode=encrypt, key missing", "true", "encrypt", "", "", true},
		{"pii on, mode=encrypt, key present", "true", "encrypt", "", "any-string", false},
		{"pii on, entity-action=encrypt, key missing", "true", "replace", "Email:encrypt", "", true},
		{"pii on, entity-action=encrypt, key present", "true", "replace", "Email:encrypt", "any-string", false},
		{"pii on, mode=mask, no encrypt — key irrelevant even if set", "true", "mask", "", "set-but-ignored", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PII_REDACTION_ENABLED", tc.enabled)
			t.Setenv("PII_REDACTION_MODE", tc.mode)
			t.Setenv("PII_ENTITY_ACTIONS", tc.actions)
			t.Setenv("PII_ENCRYPT_KEY", tc.key)
			_, err := Load()
			if tc.wantError && err == nil {
				t.Error("expected boot error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test, verify all rows pass**

Run: `go test ./internal/config/ -run TestLoad_EncryptKeyBootValidation -v`
Expected: all seven sub-tests PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/config/plugin_config_test.go
git commit -m "test(config): pin encrypt-key boot-validation matrix (spec §3.5)"
```

---

## Task 10: Wire new config fields into `main.go`

**Files:**
- Modify: `cmd/otto-gateway/main.go`

- [ ] **Step 1: Locate the existing PII hook construction**

`cmd/otto-gateway/main.go` around line 203 contains:
```go
&pii.PIIRedactionHook{
    Recognizers:     filterRecognizers(pii.Recognizers, cfg.PIIEnabledEntities),
    Enabled:         cfg.PIIRedactionEnabled,
    Mode:            cfg.PIIRedactionMode,
    HashKey:         []byte(cfg.PIIHashKey),
    EnabledEntities: cfg.PIIEnabledEntities,
    Logger:          logger,
},
```

- [ ] **Step 2: Construct a single named instance and wire it into both Pre and Post**

Replace the inline literal with a named local construction before the `chain := plugin.Chain{...}` block:
```go
// PII hook is a single instance shared between Pre (encrypt + redact)
// and Post (decrypt sweep). Same precedent as loggingHook below.
// When encrypt is NOT active anywhere, the Post side is a cheap
// no-op (encryptActive() returns false; After returns nil immediately).
piiHook := &pii.PIIRedactionHook{
    Recognizers:     filterRecognizers(pii.Recognizers, cfg.PIIEnabledEntities),
    Enabled:         cfg.PIIRedactionEnabled,
    Mode:            cfg.PIIRedactionMode,
    HashKey:         []byte(cfg.PIIHashKey),
    EnabledEntities: cfg.PIIEnabledEntities,
    EntityActions:   cfg.PIIEntityActions,
    Logger:          logger,
}
// Derive EncryptKey only when encrypt is active; Config validation
// already guarantees PIIEncryptKey is non-empty in that case.
if piiHook.Mode == "encrypt" || hasEncryptAction(cfg.PIIEntityActions) {
    key, err := pii.DeriveKey(cfg.PIIEncryptKey)
    if err != nil {
        return fmt.Errorf("pii: derive encrypt key: %w", err)
    }
    piiHook.EncryptKey = key
}
```

Then in the `chain := plugin.Chain{...}` literal, replace the inline `&pii.PIIRedactionHook{...}` with `piiHook` and append the SAME instance to Post:
```go
chain := plugin.Chain{
    Pre: []engine.PreHook{
        &plugin.RequestIDHook{Logger: logger},
        &plugin.AuthHook{Tokens: cfg.AuthToken},
        piiHook,
        loggingHook,
    },
    Post: []engine.PostHook{
        piiHook,    // encrypt round-trip decrypt sweep
        loggingHook,
    },
}
```

Add the small helper next to `filterRecognizers` (further down in `main.go`):
```go
// hasEncryptAction returns true if any value in actions is "encrypt".
// Used to gate EncryptKey derivation — we only call pii.DeriveKey
// when the encrypt action is actually active.
func hasEncryptAction(actions map[string]string) bool {
    for _, a := range actions {
        if a == "encrypt" {
            return true
        }
    }
    return false
}
```

- [ ] **Step 3: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: build succeeds, all tests pass. Existing real-binary e2e in `cmd/otto-gateway/` will exercise the chain literal; the PostHook chain now contains two entries instead of one but the dedup in `/health/hooks` (slice-5 Task 4 per existing docstring) handles that.

- [ ] **Step 4: Commit**

```bash
git add cmd/otto-gateway/main.go
git commit -m "feat(main): wire EntityActions + EncryptKey; register PII hook in Post chain"
```

---

## Task 11: Document new env vars in `docs/operating.md`

**Files:**
- Modify: `docs/operating.md`

- [ ] **Step 1: Locate the PII env-var table**

In `docs/operating.md` around lines 215-225, find the table row for `PII_REDACTION_MODE` and the row for `PII_HASH_KEY`.

- [ ] **Step 2: Update `PII_REDACTION_MODE` row to list `encrypt`**

Change the `PII_REDACTION_MODE` row to include `encrypt` in the allowed values:
```markdown
| `PII_REDACTION_MODE` | `replace` | One of `replace`, `mask`, `hash`, `drop`, `encrypt`. `replace` substitutes `[EMAIL_N]` tokens with a per-canonical-value counter; `mask` substitutes a partial obfuscation (e.g., `co***@cm***.io`); `hash` substitutes `[EMAIL:h-XXXXXXXX]` with the first 8 hex chars of `HMAC-SHA256(PII_HASH_KEY, canonical(value))`; `drop` substitutes an empty string; `encrypt` substitutes `[PII:EMAIL:base64url]` with an AES-256-GCM ciphertext that the response Post-hook decrypts back to plaintext before the client sees the response (round-trip). Unknown values → boot error. |
```

(Note: incidentally fixes the stale `<EMAIL_N>` / `<EMAIL:h-...>` shapes in the existing row to match the actual `[...]` shapes shipped in commit `a3160e1`.)

- [ ] **Step 3: Add `PII_ENCRYPT_KEY` and `PII_ENTITY_ACTIONS` rows**

After the `PII_HASH_KEY` row, append:
```markdown
| `PII_ENCRYPT_KEY` | _(empty)_ | Key for `PII_REDACTION_MODE=encrypt` (or any per-entity encrypt override via `PII_ENTITY_ACTIONS`). Accepts **any non-empty string** — the gateway derives a 32-byte AES-256-GCM key via SHA-256 at boot. **Required when encrypt is active anywhere** — boot error otherwise (no silent fallback). Rotating this key invalidates prior round-trip tokens (in-flight chat history affected; new requests after restart use the new key). |
| `PII_ENTITY_ACTIONS` | _(empty)_ | Per-entity action overrides. Shape: `Entity:action,Entity:action,...` e.g. `Email:encrypt,SSN:drop`. When non-empty, the listed entities use the specified action instead of the global `PII_REDACTION_MODE`. Unlisted entities fall back to the global mode. Unknown entity names or unknown action values → boot error. Allowed actions: `replace`, `mask`, `hash`, `drop`, `encrypt`. |
```

- [ ] **Step 4: Add a boot-error case**

In the "Boot errors" section around line 260, add to the list:
```markdown
- `PII_ENTITY_ACTIONS` contains an unknown entity name or action
  value → boot error names the offending pair.
- `PII_REDACTION_MODE=encrypt` (or `PII_ENTITY_ACTIONS` contains
  `:encrypt`) AND `PII_ENCRYPT_KEY` is empty → boot error names
  `PII_ENCRYPT_KEY`. There is no silent fallback.
```

- [ ] **Step 5: Commit**

```bash
git add docs/operating.md
git commit -m "docs(operating): document PII_ENCRYPT_KEY + PII_ENTITY_ACTIONS"
```

---

## Self-Review Coverage Map

| Spec section | Covered by task(s) |
|---|---|
| §3.1 Streaming auto-disable | Task 5 (Before flips req.Stream) |
| §3.2 Per-entity action config | Task 4 (struct field + helpers), Task 5 (Before uses actionFor), Task 8 (env var parser) |
| §3.3 Token marker `[PII:E:b64url]` | Task 2 (EncryptValue/DecryptToken), Task 6 (decryptTokenRe regex) |
| §3.4 Single struct, both interfaces | Task 6 (After + PostHook compile assertion), Task 10 (registered in both chains) |
| §3.5 Key storage (any-string + SHA-256) | Task 1 (DeriveKey), Task 8 (conditional boot validation), Task 10 (gated derivation) |
| §3.6 Decrypt failure mode | Task 6 (mangled-token-left-in-place tests + Warn log) |
| §4.1 `encrypt.go` | Tasks 1, 2 |
| §4.2 `modes.go` extension | Task 3 |
| §4.3 `pii.go` extension | Tasks 4, 5, 6, 7 |
| §4.4 slice-5 wiring | Tasks 8, 9, 10 |
| §6 Configuration surface | Tasks 8, 11 |
| §7 Error handling matrix | Task 6 (decrypt failures), Task 9 (boot matrix), Task 3 (encrypt fail-safe) |
| §8 Threat model — EncryptKey not published | Task 7 (`TestDescribe_NeverPublishesEncryptKey`) |
| §9 Testing strategy | Tasks 1, 2 (encrypt_test.go), Tasks 3-7 (mode/Before/After/Describe tests), Tasks 8, 9 (config tests) |
