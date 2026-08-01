package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	privacyTriageMaxScopes   = 128
	privacyTriageMaxMappings = 4096
	privacyTriageMaxJSON     = 8 << 20
	privacyScopeHMACChars    = 12
)

var (
	privacyTriageScopePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	errPrivacyTriageJSONLimit = errors.New("privacy triage response too large")
)

// PrivacyScopeRow is the admin-owned wire projection for safe scope metadata.
type PrivacyScopeRow struct {
	ID         string    `json:"id"`
	Profile    string    `json:"profile"`
	State      string    `json:"state"`
	Entries    int       `json:"entries"`
	InFlight   int       `json:"in_flight"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// PrivacyMappingRow is the admin-owned wire projection for one authorized,
// reversible mapping. Secrets never enter the underlying reversible ledger.
type PrivacyMappingRow struct {
	Entity     string    `json:"entity"`
	Original   string    `json:"original"`
	Synthetic  string    `json:"synthetic"`
	Provenance string    `json:"provenance"`
	CreatedAt  time.Time `json:"created_at"`
}

type PrivacyClearResult struct {
	State string `json:"state"`
}

// PrivacyTriageSource is intentionally narrower than the privacy service. It
// grants only the four mapping-lifecycle operations exposed by this route.
type PrivacyTriageSource interface {
	ListPrivacyScopes() []PrivacyScopeRow
	InspectPrivacyScope(string) ([]PrivacyMappingRow, error)
	ClearPrivacyScope(string) (PrivacyClearResult, error)
	ClearAllPrivacyScopes() PrivacyClearResult
}

type privacyTriagePeerContextKey struct{}

func (h *handler) privacyTriageProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		peer, ok := privacyTriagePeer(r.RemoteAddr)
		if !ok || !peer.IsLoopback() {
			h.auditPrivacyTriage(r, "denied", netip.Addr{}, "")
			writePrivacyTriageError(w, http.StatusForbidden, "loopback_required")
			return
		}
		if !privacyTriageAuthorized(r.Header.Get("Authorization"), h.deps.PrivacyTriageToken) {
			h.auditPrivacyTriage(r, "denied", peer, "")
			writePrivacyTriageError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		redactPrivacyTriageRequestPath(r)
		ctx := context.WithValue(r.Context(), privacyTriagePeerContextKey{}, peer)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// redactPrivacyTriageRequestPath prevents outer request-logging middleware
// from serializing a raw scope ID after this mounted handler returns. chi has
// already captured the URL parameter in RouteContext, so validation still
// receives the original parameter exactly once.
func redactPrivacyTriageRequestPath(r *http.Request) {
	const marker = "/api/privacy/scopes/"
	markerIndex := strings.Index(r.URL.Path, marker)
	if markerIndex < 0 {
		return
	}
	prefix := r.URL.Path[:markerIndex]
	if strings.HasSuffix(r.URL.Path, "/mapping") {
		r.URL.Path = prefix + marker + "{scope-id}/mapping"
	} else {
		r.URL.Path = prefix + marker + "{scope-id}"
	}
	r.URL.RawPath = ""
}

func privacyTriagePeer(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return peer.Unmap(), true
}

func privacyTriageAuthorized(header, expected string) bool {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.Contains(token, " ") {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(token))
	return expected != "" && subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func (h *handler) listPrivacyScopes(w http.ResponseWriter, r *http.Request) {
	if h.deps.PrivacyTriage == nil {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), "")
		writePrivacyTriageError(w, http.StatusServiceUnavailable, "triage_unavailable")
		return
	}
	rows := h.deps.PrivacyTriage.ListPrivacyScopes()
	if len(rows) > privacyTriageMaxScopes {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), "")
		writePrivacyTriageError(w, http.StatusInternalServerError, "response_too_large")
		return
	}
	result := "completed"
	if !writePrivacyTriageJSON(w, http.StatusOK, rows, h) {
		result = "failed"
	}
	h.auditPrivacyTriage(r, result, privacyTriagePeerFromContext(r), "")
}

func (h *handler) inspectPrivacyScope(w http.ResponseWriter, r *http.Request) {
	scopeID, ok := privacyTriageScopeID(r)
	if !ok {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), "")
		writePrivacyTriageError(w, http.StatusBadRequest, "invalid_scope")
		return
	}
	if h.deps.PrivacyTriage == nil {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), scopeID)
		writePrivacyTriageError(w, http.StatusServiceUnavailable, "triage_unavailable")
		return
	}
	rows, err := h.deps.PrivacyTriage.InspectPrivacyScope(scopeID)
	if err != nil {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), scopeID)
		writePrivacyTriageError(w, http.StatusNotFound, "scope_not_found")
		return
	}
	if len(rows) > privacyTriageMaxMappings {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), scopeID)
		writePrivacyTriageError(w, http.StatusInternalServerError, "response_too_large")
		return
	}
	result := "completed"
	if !writePrivacyTriageJSON(w, http.StatusOK, rows, h) {
		result = "failed"
	}
	h.auditPrivacyTriage(r, result, privacyTriagePeerFromContext(r), scopeID)
}

func (h *handler) clearPrivacyScope(w http.ResponseWriter, r *http.Request) {
	scopeID, ok := privacyTriageScopeID(r)
	if !ok {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), "")
		writePrivacyTriageError(w, http.StatusBadRequest, "invalid_scope")
		return
	}
	if h.deps.PrivacyTriage == nil {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), scopeID)
		writePrivacyTriageError(w, http.StatusServiceUnavailable, "triage_unavailable")
		return
	}
	result, err := h.deps.PrivacyTriage.ClearPrivacyScope(scopeID)
	if err != nil {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), scopeID)
		writePrivacyTriageError(w, http.StatusNotFound, "scope_not_found")
		return
	}
	h.writePrivacyClearResult(w, r, result, scopeID)
}

func (h *handler) clearAllPrivacyScopes(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-GW-Privacy-Confirm") != "clear-all" {
		h.auditPrivacyTriage(r, "denied", privacyTriagePeerFromContext(r), "")
		writePrivacyTriageError(w, http.StatusBadRequest, "confirmation_required")
		return
	}
	if h.deps.PrivacyTriage == nil {
		h.auditPrivacyTriage(r, "failed", privacyTriagePeerFromContext(r), "")
		writePrivacyTriageError(w, http.StatusServiceUnavailable, "triage_unavailable")
		return
	}
	h.writePrivacyClearResult(w, r, h.deps.PrivacyTriage.ClearAllPrivacyScopes(), "")
}

func (h *handler) writePrivacyClearResult(w http.ResponseWriter, r *http.Request, result PrivacyClearResult, scopeID string) {
	peer := privacyTriagePeerFromContext(r)
	if result.State == "closing" {
		auditResult := "closing"
		if !writePrivacyTriageJSON(w, http.StatusAccepted, PrivacyClearResult{State: "closing"}, h) {
			auditResult = "failed"
		}
		h.auditPrivacyTriage(r, auditResult, peer, scopeID)
		return
	}
	h.auditPrivacyTriage(r, "completed", peer, scopeID)
	w.WriteHeader(http.StatusNoContent)
}

func privacyTriageScopeID(r *http.Request) (string, bool) {
	encoded := chi.URLParam(r, "scope-id")
	scopeID, err := url.PathUnescape(encoded)
	if err != nil || !privacyTriageScopePattern.MatchString(scopeID) {
		return "", false
	}
	return scopeID, true
}

func privacyTriagePeerFromContext(r *http.Request) netip.Addr {
	if r == nil {
		return netip.Addr{}
	}
	peer, _ := r.Context().Value(privacyTriagePeerContextKey{}).(netip.Addr)
	return peer
}

func (h *handler) auditPrivacyTriage(r *http.Request, result string, peer netip.Addr, scopeID string) {
	operation := privacyTriageOperation(r)
	if observe := h.deps.PrivacyTriageObserver; observe != nil {
		observe(operation, result)
	}
	attrs := []any{"operation", operation, "result", result}
	if peer.IsValid() {
		attrs = append(attrs, "peer", peer.String())
	}
	if scopeID != "" {
		attrs = append(attrs, "scope_hmac", privacyTriageScopeHMAC(h.deps.PrivacyTriageToken, scopeID))
	}
	h.deps.Logger.Info("admin: privacy triage", attrs...)
}

func privacyTriageOperation(r *http.Request) string {
	if r == nil {
		return "status"
	}
	if strings.HasSuffix(r.URL.Path, "/mapping") {
		return "inspect"
	}
	if r.Method == http.MethodDelete {
		if chi.URLParam(r, "scope-id") == "" {
			return "clear_all"
		}
		return "clear"
	}
	if r.Method == http.MethodGet {
		return "list"
	}
	return "status"
}

func privacyTriageScopeHMAC(key, scopeID string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(scopeID))
	return fmt.Sprintf("%x", mac.Sum(nil))[:privacyScopeHMACChars]
}

func writePrivacyTriageJSON(w http.ResponseWriter, status int, value any, h *handler) bool {
	body := &privacyTriageLimitedBuffer{remaining: privacyTriageMaxJSON}
	if err := json.NewEncoder(body).Encode(value); err != nil {
		if errors.Is(err, errPrivacyTriageJSONLimit) {
			writePrivacyTriageError(w, http.StatusInternalServerError, "response_too_large")
			return false
		}
		h.deps.Logger.Warn("admin: privacy triage encode failed")
		writePrivacyTriageError(w, http.StatusInternalServerError, "encoding_failed")
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
	return true
}

func writePrivacyTriageError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

type privacyTriageLimitedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *privacyTriageLimitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, errPrivacyTriageJSONLimit
	}
	n, err := b.Buffer.Write(p)
	b.remaining -= n
	return n, err
}
