package privacy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/adapter/anthropic"
	"otto-gateway/internal/adapter/ollama"
	"otto-gateway/internal/adapter/openai"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/compress"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/privacy"
)

const (
	conformanceScope  = "task17-conformance-scope"
	conformanceEmail  = "alice.task17@example.com"
	conformanceIPv4   = "10.23.45.67"
	conformanceSecret = "sk-proj-Task17ConformanceSecret1234567890AB"
)

type conformanceServer struct {
	server      *httptest.Server
	service     *privacy.Service
	worker      *captureWorker
	compression *compress.Hook
	logs        *bytes.Buffer
	trace       *bytes.Buffer
}

type routeFixture struct {
	name    string
	path    string
	body    string
	headers http.Header
}

type conformanceDataCase struct {
	name       string
	prompt     string
	originals  []string
	reversible bool
}

func comprehensiveConformanceCases() []conformanceDataCase {
	return []conformanceDataCase{
		{name: "ipv4_cidr", prompt: "10.20.30.7/24", originals: []string{"10.20.30.7/24"}, reversible: true},
		{name: "ipv6_cidr", prompt: "2001:4860:1234:5678::abcd/64", originals: []string{"2001:4860:1234:5678::abcd/64"}, reversible: true},
		{name: "sip_uri", prompt: "sip:alice@invalid", originals: []string{"sip:alice@invalid"}, reversible: true},
		{name: "imei", prompt: "IMEI: 490154203237518", originals: []string{"490154203237518"}, reversible: true},
		{name: "imsi", prompt: "IMSI 310150123456789", originals: []string{"310150123456789"}, reversible: true},
		{name: "msisdn", prompt: "MSISDN +442071838750", originals: []string{"+442071838750"}, reversible: true},
		{name: "mac", prompt: "00:1B:44:11:3A:B7", originals: []string{"00:1B:44:11:3A:B7"}, reversible: true},
		{name: "coordinates", prompt: "42.3601 N, 71.0589 W", originals: []string{"42.3601 N, 71.0589 W"}, reversible: true},
		{name: "site", prompt: "site-A12_NYC01", originals: []string{"site-A12_NYC01"}, reversible: true},
		{name: "email", prompt: "personal.task17@example.com", originals: []string{"personal.task17@example.com"}, reversible: true},
		{name: "ssn", prompt: "123-45-6789", originals: []string{"123-45-6789"}, reversible: true},
		{name: "payment_card", prompt: "4111-1111-1111-1111", originals: []string{"4111-1111-1111-1111"}, reversible: true},
		{name: "address", prompt: "1111 Main Street, Austin, TX 27584", originals: []string{"1111 Main Street, Austin, TX 27584"}, reversible: true},
		{name: "bearer", prompt: "Authorization: Bearer task17BearerTokenValue1234567890", originals: []string{"task17BearerTokenValue1234567890"}},
		{name: "basic", prompt: "Proxy-Authorization: Basic dXNlcjp0YXNrMTdzZWNyZXQ=", originals: []string{"dXNlcjp0YXNrMTdzZWNyZXQ="}},
		{name: "github_key", prompt: "ghp_Task17GitHubTokenValue123456789012345", originals: []string{"ghp_Task17GitHubTokenValue123456789012345"}},
		{name: "openai_key", prompt: "sk-proj-Task17OpenAIKey12345678901234567890", originals: []string{"sk-proj-Task17OpenAIKey12345678901234567890"}},
		{name: "json_password", prompt: `{"password":"task17-json-password"}`, originals: []string{"task17-json-password"}},
		{name: "yaml_secret", prompt: "client_secret: task17-yaml-client-secret", originals: []string{"task17-yaml-client-secret"}},
		{name: "dotenv_token", prompt: "REFRESH_TOKEN=task17-dotenv-refresh-token", originals: []string{"task17-dotenv-refresh-token"}},
		{name: "cli_token", prompt: "--access-token=task17-cli-access-token", originals: []string{"task17-cli-access-token"}},
		{name: "credential_url", prompt: "postgres://task17:dbpassword@db.example.invalid/app", originals: []string{"dbpassword"}},
		{name: "private_key", prompt: "-----BEGIN PRIVATE KEY-----\nTASK17PRIVATEKEYMATERIAL\n-----END PRIVATE KEY-----", originals: []string{"TASK17PRIVATEKEYMATERIAL"}},
	}
}

func newConformanceServer(t *testing.T) *conformanceServer {
	t.Helper()
	return newConformanceServerWith(t, nil, nil)
}

func newConformanceServerWith(t *testing.T, mutate func(*privacy.Config), worker *captureWorker) *conformanceServer {
	t.Helper()

	config := privacy.Config{
		DefaultProfile:     privacy.ProfileStandard,
		RequestProfiles:    []privacy.Profile{privacy.ProfileStandard, privacy.ProfileStrict},
		AliasKey:           []byte("task17-deterministic-alias-key-32b"),
		SecretAction:       privacy.ActionReplace,
		TechnicalAction:    privacy.ActionPseudonymize,
		ScopeTTL:           time.Hour,
		MaxScopes:          256,
		MaxEntriesPerScope: 4096,
		MaxTotalEntries:    32768,
		PIIEnabled:         true,
		PIIMode:            privacy.ActionEncrypt,
		PIIEncryptKey:      []byte("0123456789abcdef0123456789abcdef"),
		Recognizers:        pii.SourceAuditNames(),
		Classifier:         pii.NewPIIClassifier(pii.Recognizers, nil, false),
		SecretClassifier:   privacy.NewSecretClassifier(),
	}
	if mutate != nil {
		mutate(&config)
	}
	service, err := privacy.NewService(config)
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)

	if worker == nil {
		worker = &captureWorker{}
	}
	logs := &bytes.Buffer{}
	trace := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	compressionHook := &compress.Hook{
		Enabled: false, TriggerTokens: 32, BudgetTokens: 24, ProtectTail: 1, ToolKeep: 32, Logger: logger,
	}
	chatTraceHook := &plugin.ChatTraceHook{Writer: trace, Enabled: true, Logger: logger, Privacy: service}
	privacyHook := &pii.PIIRedactionHook{Service: service}
	loggingHook := &plugin.LoggingHook{Logger: logger}
	core := engine.New(engine.Config{
		Logger: logger,
		ACP:    worker,
		PreHooks: []engine.PreHook{
			chatTraceHook, compressionHook, privacyHook, loggingHook,
		},
		PostHooks: []engine.PostHook{privacyHook, loggingHook, chatTraceHook},
	})

	router := chi.NewRouter()
	ollamaAdapter := ollama.New(ollama.Config{Engine: testOllamaEngine{engine: core}})
	openAIAdapter := openai.New(openai.Config{Engine: testOpenAIEngine{engine: core}})
	anthropicAdapter := anthropic.New(anthropic.Config{Engine: testAnthropicEngine{engine: core}})
	router.Route("/api", func(r chi.Router) { ollamaAdapter.RegisterRoutes(r) })
	router.Route("/v1", func(r chi.Router) {
		openAIAdapter.RegisterRoutes(r)
		anthropicAdapter.RegisterRoutes(r)
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &conformanceServer{
		server: server, service: service, worker: worker, compression: compressionHook, logs: logs, trace: trace,
	}
}

func (s *conformanceServer) assertStrictRoundTripAcrossFiveRoutes(t *testing.T) {
	t.Helper()
	fixtures := conformanceRouteFixtures(false)
	var referenceReceipt *privacy.Receipt
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			before := s.worker.dispatchCount()
			resp, body := s.post(t, fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, body)
			}
			if got := s.worker.dispatchCount(); got != before+1 {
				t.Fatalf("worker dispatches=%d, want %d", got, before+1)
			}
			clientText := assertNativeSuccess(t, fixture, resp, body)
			if !strings.Contains(clientText, conformanceEmail) || !strings.Contains(clientText, conformanceIPv4) {
				t.Fatalf("caller-input values were not restored; content=%s", clientText)
			}
			if strings.Contains(clientText, conformanceSecret) {
				t.Fatalf("one-way credential was restored to client: %s", clientText)
			}

			receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
			want := privacy.Receipt{
				Version: 1, Profile: privacy.ProfileStrict, Scope: conformanceScope,
				Coverage: "full", Result: "pass", Transformed: 3, Restored: 2,
			}
			if receipt != want {
				t.Fatalf("receipt=%+v, want exact %+v", receipt, want)
			}
			if referenceReceipt == nil {
				referenceReceipt = &receipt
			} else if receipt != *referenceReceipt {
				t.Fatalf("route receipt=%+v, want exact cross-route receipt %+v", receipt, *referenceReceipt)
			}
		})
	}

	records := s.worker.recordsCopy()
	if len(records) != len(fixtures) {
		t.Fatalf("worker records=%d, want %d", len(records), len(fixtures))
	}
	var stableAlias string
	for _, record := range records {
		joined := record.joinedText()
		if strings.Contains(joined, conformanceEmail) || strings.Contains(joined, conformanceIPv4) || strings.Contains(joined, conformanceSecret) {
			t.Fatalf("worker received protected caller input on %s: %s", record.sessionID, joined)
		}
		alias := firstBenchmarkIPv4(joined)
		if alias == "" {
			t.Fatalf("worker did not receive a valid synthetic IPv4: %s", joined)
		}
		if stableAlias == "" {
			stableAlias = alias
		} else if alias != stableAlias {
			t.Fatalf("same-scope IPv4 alias drifted: got %q, want %q", alias, stableAlias)
		}
	}
}

func conformanceRouteFixtures(stream bool) []routeFixture {
	protected := "contact " + conformanceEmail + " from " + conformanceIPv4 + " using " + conformanceSecret
	return conformanceRouteFixturesFor(stream, protected, conformanceScope)
}

func conformanceRouteFixturesFor(stream bool, protected, scope string) []routeFixture {
	streamJSON := "false"
	if stream {
		streamJSON = "true"
	}
	baseHeaders := strictHeaders(scope)
	return []routeFixture{
		{
			name: "ollama_chat", path: "/api/chat", headers: baseHeaders.Clone(),
			body: fmt.Sprintf(`{"model":"auto","messages":[{"role":"system","content":"system policy"},{"role":"user","content":%q}],"stream":%s}`, protected, streamJSON),
		},
		{
			name: "ollama_generate", path: "/api/generate", headers: baseHeaders.Clone(),
			body: fmt.Sprintf(`{"model":"auto","system":"system policy","prompt":%q,"stream":%s}`, protected, streamJSON),
		},
		{
			name: "openai_chat", path: "/v1/chat/completions", headers: baseHeaders.Clone(),
			body: fmt.Sprintf(`{"model":"auto","messages":[{"role":"system","content":"system policy"},{"role":"user","content":%q}],"stream":%s}`, protected, streamJSON),
		},
		{
			name: "openai_completion", path: "/v1/completions", headers: baseHeaders.Clone(),
			body: fmt.Sprintf(`{"model":"auto","prompt":%q,"stream":%s}`, protected, streamJSON),
		},
		{
			name: "anthropic_messages", path: "/v1/messages", headers: func() http.Header {
				h := baseHeaders.Clone()
				h.Set("anthropic-version", "2023-06-01")
				return h
			}(),
			body: fmt.Sprintf(`{"model":"auto","max_tokens":256,"system":"system policy","messages":[{"role":"user","content":%q}],"stream":%s}`, protected, streamJSON),
		},
	}
}

type conformanceResponse struct {
	StatusCode int
	Header     http.Header
}

func (s *conformanceServer) post(t *testing.T, fixture routeFixture) (conformanceResponse, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.server.URL+fixture.path, strings.NewReader(fixture.body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header = fixture.headers.Clone()
	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do %s: %v", fixture.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return conformanceResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone()}, string(body)
}

func assertConformanceDataCase(t *testing.T, dataCase conformanceDataCase, workerText, clientText string) {
	t.Helper()
	for _, original := range dataCase.originals {
		if strings.Contains(workerText, original) {
			t.Fatalf("worker received raw %s value %q: %s", dataCase.name, original, workerText)
		}
		if dataCase.reversible && !strings.Contains(clientText, original) {
			t.Fatalf("caller-input %s value %q was not restored: %s", dataCase.name, original, clientText)
		}
		if !dataCase.reversible && strings.Contains(clientText, original) {
			t.Fatalf("one-way %s value %q was restored: %s", dataCase.name, original, clientText)
		}
	}
}

func assertExactPassReceipt(t *testing.T, receipt privacy.Receipt, scope string, reversible bool) {
	t.Helper()
	if receipt.Version != 1 || receipt.Profile != privacy.ProfileStrict || receipt.Scope != scope ||
		receipt.Coverage != "full" || receipt.Result != "pass" || receipt.Transformed <= 0 || receipt.Blocked != 0 {
		t.Fatalf("receipt=%+v, want exact strict/full/pass scope=%q with transformations and zero blocks", receipt, scope)
	}
	if reversible && receipt.Restored != receipt.Transformed {
		t.Fatalf("reversible receipt=%+v, want restored == transformed", receipt)
	}
	if !reversible && receipt.Restored != 0 {
		t.Fatalf("one-way receipt=%+v, want restored=0", receipt)
	}
}

func assertNativeSuccess(t *testing.T, fixture routeFixture, resp conformanceResponse, body string) string {
	t.Helper()
	wantStreaming := strings.Contains(fixture.body, `"stream":true`)
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type %q: %v", resp.Header.Get("Content-Type"), err)
	}
	if !wantStreaming {
		if mediaType != "application/json" {
			t.Fatalf("Content-Type=%q, want application/json", mediaType)
		}
		return decodeNativeJSONSuccess(t, fixture, body)
	}

	switch {
	case strings.HasPrefix(fixture.path, "/api/"):
		if mediaType != "application/x-ndjson" {
			t.Fatalf("Content-Type=%q, want application/x-ndjson", mediaType)
		}
		return decodeOllamaNDJSONSuccess(t, fixture, body)
	case fixture.path == "/v1/messages":
		if mediaType != "text/event-stream" {
			t.Fatalf("Content-Type=%q, want text/event-stream", mediaType)
		}
		return decodeAnthropicSSESuccess(t, body)
	default:
		if mediaType != "text/event-stream" {
			t.Fatalf("Content-Type=%q, want text/event-stream", mediaType)
		}
		return decodeOpenAISSESuccess(t, fixture, body)
	}
}

func decodeNativeJSONSuccess(t *testing.T, fixture routeFixture, body string) string {
	t.Helper()
	object := decodeJSONObject(t, body)
	if got := requireStringField(t, object, "model"); got != "auto" {
		t.Fatalf("model=%q, want auto", got)
	}
	switch fixture.path {
	case "/api/chat":
		if done, ok := object["done"].(bool); !ok || !done {
			t.Fatalf("Ollama chat done=%v, want true", object["done"])
		}
		message := requireObjectField(t, object, "message")
		if role := requireStringField(t, message, "role"); role != "assistant" {
			t.Fatalf("Ollama chat role=%q, want assistant", role)
		}
		return requireStringField(t, message, "content")
	case "/api/generate":
		if done, ok := object["done"].(bool); !ok || !done {
			t.Fatalf("Ollama generate done=%v, want true", object["done"])
		}
		return requireStringField(t, object, "response")
	case "/v1/chat/completions":
		if got := requireStringField(t, object, "object"); got != "chat.completion" {
			t.Fatalf("OpenAI object=%q, want chat.completion", got)
		}
		choice := requireFirstObject(t, object, "choices")
		message := requireObjectField(t, choice, "message")
		if role := requireStringField(t, message, "role"); role != "assistant" {
			t.Fatalf("OpenAI role=%q, want assistant", role)
		}
		return requireStringField(t, message, "content")
	case "/v1/completions":
		if got := requireStringField(t, object, "object"); got != "text_completion" {
			t.Fatalf("OpenAI object=%q, want text_completion", got)
		}
		return requireStringField(t, requireFirstObject(t, object, "choices"), "text")
	case "/v1/messages":
		if got := requireStringField(t, object, "type"); got != "message" {
			t.Fatalf("Anthropic type=%q, want message", got)
		}
		if role := requireStringField(t, object, "role"); role != "assistant" {
			t.Fatalf("Anthropic role=%q, want assistant", role)
		}
		content := requireFirstObject(t, object, "content")
		if got := requireStringField(t, content, "type"); got != "text" {
			t.Fatalf("Anthropic content type=%q, want text", got)
		}
		return requireStringField(t, content, "text")
	default:
		t.Fatalf("unsupported conformance route %q", fixture.path)
		return ""
	}
}

func decodeOllamaNDJSONSuccess(t *testing.T, fixture routeFixture, body string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 2 {
		t.Fatalf("Ollama stream has %d lines, want content and terminal frames: %s", len(lines), body)
	}
	var content strings.Builder
	for index, line := range lines {
		frame := decodeJSONObject(t, line)
		if got := requireStringField(t, frame, "model"); got != "auto" {
			t.Fatalf("Ollama stream model=%q, want auto", got)
		}
		done, ok := frame["done"].(bool)
		if !ok {
			t.Fatalf("Ollama stream frame missing boolean done: %s", line)
		}
		if index == len(lines)-1 {
			if !done {
				t.Fatalf("Ollama terminal frame done=false: %s", line)
			}
			continue
		}
		if done {
			t.Fatalf("Ollama non-terminal frame done=true: %s", line)
		}
		if fixture.path == "/api/chat" {
			content.WriteString(requireStringField(t, requireObjectField(t, frame, "message"), "content"))
		} else {
			content.WriteString(requireStringField(t, frame, "response"))
		}
	}
	return content.String()
}

func decodeOpenAISSESuccess(t *testing.T, fixture routeFixture, body string) string {
	t.Helper()
	blocks := splitSSEBlocks(body)
	if len(blocks) < 3 {
		t.Fatalf("OpenAI stream has %d frames, want content, terminal, and [DONE]: %s", len(blocks), body)
	}
	var content strings.Builder
	sawTerminal := false
	for index, block := range blocks {
		if len(block) != 1 || !strings.HasPrefix(block[0], "data: ") {
			t.Fatalf("OpenAI frame is not one native data line: %q", block)
		}
		payload := strings.TrimPrefix(block[0], "data: ")
		if payload == "[DONE]" {
			if index != len(blocks)-1 {
				t.Fatalf("OpenAI [DONE] is not terminal: %s", body)
			}
			continue
		}
		frame := decodeJSONObject(t, payload)
		choice := requireFirstObject(t, frame, "choices")
		if fixture.path == "/v1/chat/completions" {
			if got := requireStringField(t, frame, "object"); got != "chat.completion.chunk" {
				t.Fatalf("OpenAI stream object=%q, want chat.completion.chunk", got)
			}
			delta := requireObjectField(t, choice, "delta")
			if value, ok := delta["content"].(string); ok {
				content.WriteString(value)
			}
		} else {
			if got := requireStringField(t, frame, "object"); got != "text_completion" {
				t.Fatalf("OpenAI completion stream object=%q, want text_completion", got)
			}
			if value, ok := choice["text"].(string); ok {
				content.WriteString(value)
			}
		}
		if choice["finish_reason"] != nil {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatalf("OpenAI stream omitted terminal finish_reason: %s", body)
	}
	return content.String()
}

func decodeAnthropicSSESuccess(t *testing.T, body string) string {
	t.Helper()
	blocks := splitSSEBlocks(body)
	wantEvents := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(blocks) != len(wantEvents) {
		t.Fatalf("Anthropic stream events=%d, want %d: %s", len(blocks), len(wantEvents), body)
	}
	var content strings.Builder
	for index, block := range blocks {
		if len(block) != 2 || !strings.HasPrefix(block[0], "event: ") || !strings.HasPrefix(block[1], "data: ") {
			t.Fatalf("Anthropic frame is not native event/data pair: %q", block)
		}
		event := strings.TrimPrefix(block[0], "event: ")
		if event != wantEvents[index] {
			t.Fatalf("Anthropic event[%d]=%q, want %q", index, event, wantEvents[index])
		}
		frame := decodeJSONObject(t, strings.TrimPrefix(block[1], "data: "))
		if got := requireStringField(t, frame, "type"); got != event {
			t.Fatalf("Anthropic payload type=%q, want event %q", got, event)
		}
		if event == "content_block_delta" {
			delta := requireObjectField(t, frame, "delta")
			if got := requireStringField(t, delta, "type"); got != "text_delta" {
				t.Fatalf("Anthropic delta type=%q, want text_delta", got)
			}
			content.WriteString(requireStringField(t, delta, "text"))
		}
	}
	return content.String()
}

func splitSSEBlocks(body string) [][]string {
	rawBlocks := strings.Split(strings.TrimSpace(body), "\n\n")
	blocks := make([][]string, 0, len(rawBlocks))
	for _, raw := range rawBlocks {
		if raw != "" {
			blocks = append(blocks, strings.Split(raw, "\n"))
		}
	}
	return blocks
}

func decodeJSONObject(t *testing.T, payload string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatalf("decode native JSON: %v; payload=%s", err, payload)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("native JSON has trailing payload: %s", payload)
	}
	return object
}

func requireObjectField(t *testing.T, object map[string]any, field string) map[string]any {
	t.Helper()
	value, ok := object[field].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not an object: %#v", field, object[field])
	}
	return value
}

func requireStringField(t *testing.T, object map[string]any, field string) string {
	t.Helper()
	value, ok := object[field].(string)
	if !ok {
		t.Fatalf("field %q is not a string: %#v", field, object[field])
	}
	return value
}

func requireFirstObject(t *testing.T, object map[string]any, field string) map[string]any {
	t.Helper()
	values, ok := object[field].([]any)
	if !ok || len(values) == 0 {
		t.Fatalf("field %q is not a non-empty array: %#v", field, object[field])
	}
	value, ok := values[0].(map[string]any)
	if !ok {
		t.Fatalf("field %q first value is not an object: %#v", field, values[0])
	}
	return value
}

type workerRecord struct {
	sessionID string
	blocks    []canonical.Block
}

func (r workerRecord) joinedText() string {
	var text strings.Builder
	for _, block := range r.blocks {
		if block.Kind == canonical.BlockKindText && block.Text != nil {
			text.WriteString(block.Text.Content)
			text.WriteByte('\n')
		}
	}
	return text.String()
}

type captureWorker struct {
	mu       sync.Mutex
	nextID   int
	records  []workerRecord
	response func(workerRecord) []canonical.Chunk
}

func (w *captureWorker) NewSession(_ context.Context, _ string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextID++
	return fmt.Sprintf("task17-session-%d", w.nextID), nil
}

func (*captureWorker) SetModel(context.Context, string, string) error { return nil }

func (w *captureWorker) Prompt(_ context.Context, sessionID string, blocks []canonical.Block) (engine.Stream, error) {
	copyBlocks := append([]canonical.Block(nil), blocks...)
	record := workerRecord{sessionID: sessionID, blocks: copyBlocks}
	w.mu.Lock()
	w.records = append(w.records, record)
	w.mu.Unlock()

	resultChunks := []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: record.joinedText()}}}
	if w.response != nil {
		resultChunks = w.response(record)
	}
	chunks := make(chan canonical.Chunk, len(resultChunks))
	for _, chunk := range resultChunks {
		chunks <- chunk
	}
	close(chunks)
	return &captureStream{
		chunks: chunks,
		result: &canonical.FinalResult{SessionID: sessionID, ChunkCount: len(resultChunks), StopReason: canonical.StopEndTurn},
	}, nil
}

func (*captureWorker) Cancel(string) {}

func (w *captureWorker) dispatchCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.records)
}

func (w *captureWorker) recordsCopy() []workerRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]workerRecord(nil), w.records...)
}

type captureStream struct {
	chunks <-chan canonical.Chunk
	result *canonical.FinalResult
}

func (s *captureStream) Chunks() <-chan canonical.Chunk          { return s.chunks }
func (s *captureStream) Result() (*canonical.FinalResult, error) { return s.result, nil }

func firstBenchmarkIPv4(value string) string {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		if r == '.' {
			return false
		}
		return r < '0' || r > '9'
	}) {
		if strings.HasPrefix(field, "198.18.") || strings.HasPrefix(field, "198.19.") {
			return field
		}
	}
	return ""
}

// The adapter-local wrappers mirror the production wiring seam in main.go.
// They contain no privacy behavior; they only bridge Go's invariant return
// types from *engine.Run to each consumer-owned RunHandle interface.
type testOllamaEngine struct{ engine *engine.Engine }

func (a testOllamaEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return a.engine.Collect(ctx, req)
}

func (a testOllamaEngine) Run(ctx context.Context, req *canonical.ChatRequest) (ollama.RunHandle, error) {
	run, err := a.engine.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return testOllamaRun{run: run}, nil
}

func (a testOllamaEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return a.engine.RunPostHooks(ctx, req, resp)
}

func (a testOllamaEngine) CollectFromRun(ctx context.Context, run ollama.RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	handle, ok := run.(testOllamaRun)
	if !ok {
		return nil, fmt.Errorf("unexpected Ollama RunHandle %T", run)
	}
	return a.engine.CollectFromRun(ctx, handle.run, req)
}

type testOllamaRun struct{ run *engine.Run }

func (h testOllamaRun) Stream() ollama.Stream     { return h.run.Stream() }
func (h testOllamaRun) SessionID() string         { return h.run.SessionID() }
func (h testOllamaRun) StopWatchdog() func() bool { return h.run.StopWatchdog() }
func (h testOllamaRun) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
}

type testOpenAIEngine struct{ engine *engine.Engine }

func (a testOpenAIEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return a.engine.Collect(ctx, req)
}

func (a testOpenAIEngine) Run(ctx context.Context, req *canonical.ChatRequest) (openai.RunHandle, error) {
	run, err := a.engine.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return testOpenAIRun{run: run}, nil
}

func (a testOpenAIEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return a.engine.RunPostHooks(ctx, req, resp)
}

func (a testOpenAIEngine) CollectFromRun(ctx context.Context, run openai.RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	handle, ok := run.(testOpenAIRun)
	if !ok {
		return nil, fmt.Errorf("unexpected OpenAI RunHandle %T", run)
	}
	return a.engine.CollectFromRun(ctx, handle.run, req)
}

type testOpenAIRun struct{ run *engine.Run }

func (h testOpenAIRun) Stream() openai.Stream     { return h.run.Stream() }
func (h testOpenAIRun) SessionID() string         { return h.run.SessionID() }
func (h testOpenAIRun) StopWatchdog() func() bool { return h.run.StopWatchdog() }
func (h testOpenAIRun) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
}

type testAnthropicEngine struct{ engine *engine.Engine }

func (a testAnthropicEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return a.engine.Collect(ctx, req)
}

func (a testAnthropicEngine) Run(ctx context.Context, req *canonical.ChatRequest) (anthropic.RunHandle, error) {
	run, err := a.engine.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return testAnthropicRun{run: run}, nil
}

func (a testAnthropicEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return a.engine.RunPostHooks(ctx, req, resp)
}

func (a testAnthropicEngine) CollectFromRun(ctx context.Context, run anthropic.RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	handle, ok := run.(testAnthropicRun)
	if !ok {
		return nil, fmt.Errorf("unexpected Anthropic RunHandle %T", run)
	}
	return a.engine.CollectFromRun(ctx, handle.run, req)
}

type testAnthropicRun struct{ run *engine.Run }

func (h testAnthropicRun) Stream() anthropic.Stream  { return h.run.Stream() }
func (h testAnthropicRun) SessionID() string         { return h.run.SessionID() }
func (h testAnthropicRun) StopWatchdog() func() bool { return h.run.StopWatchdog() }
func (h testAnthropicRun) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
}
