package privacy_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/privacy"
)

// TestConformanceFiveRoutes catches any public adapter bypassing the shared
// privacy service. The harness deliberately exercises the real HTTP adapters;
// no adapter-local privacy policy is permitted in this package.
func TestConformanceFiveRoutes(t *testing.T) {
	server := newConformanceServer(t)
	server.assertStrictRoundTripAcrossFiveRoutes(t)
}

// TestConformanceComprehensiveMatrixAcrossFiveRoutes proves that every data
// class uses the same policy, scope, receipt, and native response contract on
// every public inference route. A single-route spot check is insufficient:
// adapter-local drift would otherwise leave four public boundaries untested.
func TestConformanceComprehensiveMatrixAcrossFiveRoutes(t *testing.T) {
	for _, dataCase := range comprehensiveConformanceCases() {
		t.Run(dataCase.name, func(t *testing.T) {
			server := newConformanceServer(t)
			var referenceReceipt *privacy.Receipt
			for _, fixture := range conformanceRouteFixturesFor(false, dataCase.prompt, "matrix-"+dataCase.name) {
				t.Run(fixture.name, func(t *testing.T) {
					before := server.worker.dispatchCount()
					resp, body := server.post(t, fixture)
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("status=%d body=%s logs=%s", resp.StatusCode, body, server.logs.String())
					}
					clientText := assertNativeSuccess(t, fixture, resp, body)
					workerText := server.worker.recordsCopy()[before].joinedText()
					assertConformanceDataCase(t, dataCase, workerText, clientText)

					receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
					assertExactPassReceipt(t, receipt, "matrix-"+dataCase.name, dataCase.reversible)
					if referenceReceipt == nil {
						referenceReceipt = &receipt
					} else if receipt != *referenceReceipt {
						t.Fatalf("route receipt=%+v, want exact cross-route receipt %+v", receipt, *referenceReceipt)
					}
				})
			}
		})
	}
}

// TestConformanceStrictReceiptConsumer catches a workflow accepting a response
// that did not prove complete strict enforcement. "direct_worker" simulates a
// successful model-shaped response that bypassed Gateway and has no receipt.
func TestConformanceStrictReceiptConsumer(t *testing.T) {
	valid := privacy.Receipt{
		Version: 1, Profile: privacy.ProfileStrict, Scope: "run-consumer",
		Coverage: "full", Result: "pass", Transformed: 2, Restored: 1,
	}
	cases := []struct {
		name   string
		header string
		valid  bool
	}{
		{name: "valid", header: encodeReceiptFixture(t, valid), valid: true},
		{name: "missing"},
		{name: "direct_worker"},
		{name: "malformed_base64", header: "%%%"},
		{name: "malformed_json", header: base64.RawURLEncoding.EncodeToString([]byte("not-json"))},
		{name: "non_strict", header: encodeReceiptFixture(t, mutateReceipt(valid, func(r *privacy.Receipt) { r.Profile = privacy.ProfileStandard }))},
		{name: "non_full", header: encodeReceiptFixture(t, mutateReceipt(valid, func(r *privacy.Receipt) { r.Coverage = "input" }))},
		{name: "non_pass", header: encodeReceiptFixture(t, mutateReceipt(valid, func(r *privacy.Receipt) { r.Result = "block" }))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateStrictReceipt(tc.header)
			if tc.valid && err != nil {
				t.Fatalf("valid receipt rejected: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("unsafe receipt accepted")
			}
		})
	}
}

func TestConformanceCanonicalStringLocations(t *testing.T) {
	server := newConformanceServer(t)
	tests := []struct {
		fixture                       routeFixture
		canaries                      []string
		wantTransformed, wantRestored int
	}{
		{
			fixture: routeFixture{
				name:    "ollama_chat_system_message_tool_arguments_and_result",
				path:    "/api/chat",
				headers: strictHeaders("locations-ollama-chat"),
				body: `{"model":"auto","stream":false,"messages":[` +
					`{"role":"system","content":"system.ollama.one@example.com"},` +
					`{"role":"user","content":"message.ollama.two@example.com"},` +
					`{"role":"assistant","content":"","tool_calls":[{"function":{"name":"lookup","arguments":{"owner":"tool.ollama.three@example.com"}}}]},` +
					`{"role":"tool","content":"result.ollama.four@example.com"}]}`,
			},
			canaries:        []string{"system.ollama.one@example.com", "message.ollama.two@example.com", "tool.ollama.three@example.com", "result.ollama.four@example.com"},
			wantTransformed: 5,
			wantRestored:    4,
		},
		{
			fixture: routeFixture{
				name:    "ollama_generate_system_and_prompt",
				path:    "/api/generate",
				headers: strictHeaders("locations-ollama-generate"),
				body:    `{"model":"auto","stream":false,"system":"system.generate.one@example.com","prompt":"prompt.generate.two@example.com"}`,
			},
			canaries:        []string{"system.generate.one@example.com", "prompt.generate.two@example.com"},
			wantTransformed: 2,
			wantRestored:    2,
		},
		{
			fixture: routeFixture{
				name:    "openai_system_text_tool_arguments_and_result",
				path:    "/v1/chat/completions",
				headers: strictHeaders("locations-openai-chat"),
				body: `{"model":"auto","stream":false,"messages":[` +
					`{"role":"system","content":"system.openai.one@example.com"},` +
					`{"role":"user","content":[{"type":"text","text":"message.openai.two@example.com"}]},` +
					`{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"owner\":\"tool.openai.three@example.com\"}"}}]},` +
					`{"role":"tool","tool_call_id":"call-1","content":"result.openai.four@example.com"}]}`,
			},
			canaries:        []string{"system.openai.one@example.com", "message.openai.two@example.com", "tool.openai.three@example.com", "result.openai.four@example.com"},
			wantTransformed: 4,
			wantRestored:    4,
		},
		{
			fixture: routeFixture{
				name:    "openai_completion_prompt",
				path:    "/v1/completions",
				headers: strictHeaders("locations-openai-completion"),
				body:    `{"model":"auto","stream":false,"prompt":"prompt.completion.one@example.com"}`,
			},
			canaries:        []string{"prompt.completion.one@example.com"},
			wantTransformed: 1,
			wantRestored:    1,
		},
		{
			fixture: routeFixture{
				name: "anthropic_system_text_tool_use_and_result",
				path: "/v1/messages",
				headers: func() http.Header {
					h := strictHeaders("locations-anthropic")
					h.Set("anthropic-version", "2023-06-01")
					return h
				}(),
				body: `{"model":"auto","max_tokens":128,"stream":false,"system":[{"type":"text","text":"system.anthropic.one@example.com"}],"messages":[` +
					`{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"lookup","input":{"owner":"tool.anthropic.two@example.com"}}]},` +
					`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"result.anthropic.three@example.com"},{"type":"text","text":"message.anthropic.four@example.com"}]}]}`,
			},
			canaries:        []string{"system.anthropic.one@example.com", "tool.anthropic.two@example.com", "result.anthropic.three@example.com", "message.anthropic.four@example.com"},
			wantTransformed: 4,
			wantRestored:    4,
		},
	}
	for _, tc := range tests {
		t.Run(tc.fixture.name, func(t *testing.T) {
			before := server.worker.dispatchCount()
			resp, body := server.post(t, tc.fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			if server.worker.dispatchCount() != before+1 {
				t.Fatal("canonical request was not dispatched exactly once")
			}
			clientText := assertNativeSuccess(t, tc.fixture, resp, body)
			for _, canary := range tc.canaries {
				if !strings.Contains(clientText, canary) {
					t.Errorf("caller-input location %q was not restored; content=%s", canary, clientText)
				}
				if strings.Contains(server.worker.recordsCopy()[before].joinedText(), canary) {
					t.Errorf("worker received raw canonical-location canary %q", canary)
				}
			}
			receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
			if receipt.Transformed != tc.wantTransformed || receipt.Restored != tc.wantRestored || receipt.Blocked != 0 {
				t.Fatalf("receipt=%+v, want transformed=%d restored=%d blocked=0", receipt, tc.wantTransformed, tc.wantRestored)
			}
		})
	}
}

func TestConformanceCompressionRunsBeforePrivacy(t *testing.T) {
	protected := "latest " + conformanceEmail + " " + conformanceIPv4 + " " + conformanceSecret
	oldContext := strings.Repeat("old duplicate context alpha beta gamma delta epsilon zeta eta theta ", 16)
	headers := strictHeaders("compressed-before-privacy")
	headers.Set("X-Compression", "true")
	fixtures := []struct {
		routeFixture
		wantRun bool
	}{
		{routeFixture: routeFixture{
			name: "ollama_chat", path: "/api/chat", headers: headers.Clone(),
			body: `{"model":"auto","stream":false,"messages":[` +
				`{"role":"user","content":` + quoteJSON(t, oldContext) + `},` +
				`{"role":"assistant","content":"historical answer"},` +
				`{"role":"user","content":` + quoteJSON(t, oldContext) + `},` +
				`{"role":"user","content":` + quoteJSON(t, protected) + `}]}`,
		}, wantRun: true},
		{routeFixture: routeFixture{
			name: "ollama_generate", path: "/api/generate", headers: headers.Clone(),
			body: `{"model":"auto","stream":false,"system":` + quoteJSON(t, oldContext) + `,"prompt":` + quoteJSON(t, protected) + `}`,
		}},
		{routeFixture: routeFixture{
			name: "openai_chat", path: "/v1/chat/completions", headers: headers.Clone(),
			body: `{"model":"auto","stream":false,"messages":[` +
				`{"role":"user","content":` + quoteJSON(t, oldContext) + `},` +
				`{"role":"assistant","content":"historical answer"},` +
				`{"role":"user","content":` + quoteJSON(t, oldContext) + `},` +
				`{"role":"user","content":` + quoteJSON(t, protected) + `}]}`,
		}, wantRun: true},
		{routeFixture: routeFixture{
			name: "openai_completion", path: "/v1/completions", headers: headers.Clone(),
			body: `{"model":"auto","stream":false,"prompt":[` + quoteJSON(t, oldContext) + `,` + quoteJSON(t, oldContext) + `,` + quoteJSON(t, protected) + `]}`,
		}},
		{routeFixture: routeFixture{
			name: "anthropic_messages", path: "/v1/messages", headers: func() http.Header {
				h := headers.Clone()
				h.Set("anthropic-version", "2023-06-01")
				return h
			}(),
			body: `{"model":"auto","max_tokens":256,"stream":false,"messages":[` +
				`{"role":"user","content":` + quoteJSON(t, oldContext) + `},` +
				`{"role":"assistant","content":"historical answer"},` +
				`{"role":"user","content":` + quoteJSON(t, oldContext) + `},` +
				`{"role":"user","content":` + quoteJSON(t, protected) + `}]}`,
		}, wantRun: true},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			server := newConformanceServer(t)
			resp, responseBody := server.post(t, tc.routeFixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.StatusCode, responseBody)
			}
			if tc.wantRun && server.compression.Stats().Runs == 0 {
				t.Fatal("real CompressionHook did not shrink the request fixture")
			}
			clientText := assertNativeSuccess(t, tc.routeFixture, resp, responseBody)
			if !strings.Contains(clientText, conformanceEmail) || !strings.Contains(clientText, conformanceIPv4) {
				t.Fatalf("post-compression response did not restore caller input: %s", clientText)
			}
			workerText := server.worker.recordsCopy()[0].joinedText()
			for _, canary := range []string{conformanceEmail, conformanceIPv4, conformanceSecret} {
				if strings.Contains(workerText, canary) {
					t.Fatalf("post-compression worker content leaked %q: %s", canary, workerText)
				}
			}
			receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
			if receipt.Transformed != 3 || receipt.Restored != 2 || receipt.Blocked != 0 {
				t.Fatalf("compression receipt=%+v, want transformed=3 restored=2 blocked=0", receipt)
			}
		})
	}
}

func TestConformanceAllTechnicalFormatsAndPersonalData(t *testing.T) {
	technical := []struct {
		name, entity, original string
	}{
		{"ipv4_cidr", "IPv4", "10.20.30.7/24"},
		{"ipv6_cidr", "IPv6", "2001:4860:1234:5678::abcd/64"},
		{"sip_uri", "SIP_URI", "sip:alice@invalid"},
		{"imei", "IMEI", "IMEI: 490154203237518"},
		{"imsi", "IMSI", "IMSI 310150123456789"},
		{"msisdn", "MSISDN", "MSISDN +442071838750"},
		{"mac", "MAC_ADDRESS", "00:1B:44:11:3A:B7"},
		{"coordinates", "COORDINATES", "42.3601 N, 71.0589 W"},
		{"site", "SITE", "site-A12_NYC01"},
	}
	personal := []struct {
		name, original string
	}{
		{"email", "personal.task17@example.com"},
		{"ssn", "123-45-6789"},
		{"payment_card", "4111-1111-1111-1111"},
		{"address", "1111 Main Street, Austin, TX 27584"},
	}
	for _, tc := range technical {
		t.Run(tc.name, func(t *testing.T) {
			scope := "technical-" + tc.name
			server := newConformanceServer(t)
			fixture := conformanceRouteFixturesFor(false, tc.original, scope)[2]
			resp, body := server.post(t, fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s logs=%s", resp.StatusCode, body, server.logs.String())
			}
			if strings.Contains(server.worker.recordsCopy()[0].joinedText(), tc.original) || !strings.Contains(body, tc.original) {
				t.Fatalf("technical value crossed boundary incorrectly: worker=%q body=%s", server.worker.recordsCopy()[0].joinedText(), body)
			}
			entries, err := server.service.TriageCapability().InspectScope(scope)
			if err != nil {
				t.Fatalf("InspectScope: %v", err)
			}
			seen := false
			for _, entry := range entries {
				seen = seen || entry.Entity == tc.entity
			}
			if !seen {
				t.Fatalf("technical ledger missing %s: %+v", tc.entity, entries)
			}
			requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
		})
	}
	for _, tc := range personal {
		t.Run(tc.name, func(t *testing.T) {
			server := newConformanceServer(t)
			fixture := conformanceRouteFixturesFor(false, tc.original, "personal-"+tc.name)[2]
			resp, body := server.post(t, fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s logs=%s", resp.StatusCode, body, server.logs.String())
			}
			if strings.Contains(server.worker.recordsCopy()[0].joinedText(), tc.original) || !strings.Contains(body, tc.original) {
				t.Fatalf("personal value crossed boundary incorrectly: worker=%q body=%s", server.worker.recordsCopy()[0].joinedText(), body)
			}
			requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
		})
	}
}

func TestConformanceCredentialFormatsAreOneWay(t *testing.T) {
	secrets := []struct {
		name, value, prompt string
	}{
		{"bearer", "task17BearerTokenValue1234567890", "Authorization: Bearer task17BearerTokenValue1234567890"},
		{"basic", "dXNlcjp0YXNrMTdzZWNyZXQ=", "Proxy-Authorization: Basic dXNlcjp0YXNrMTdzZWNyZXQ="},
		{"github_key", "ghp_Task17GitHubTokenValue123456789012345", "ghp_Task17GitHubTokenValue123456789012345"},
		{"openai_key", "sk-proj-Task17OpenAIKey12345678901234567890", "sk-proj-Task17OpenAIKey12345678901234567890"},
		{"json_password", "task17-json-password", `{"password":"task17-json-password"}`},
		{"yaml_secret", "task17-yaml-client-secret", "client_secret: task17-yaml-client-secret"},
		{"dotenv_token", "task17-dotenv-refresh-token", "REFRESH_TOKEN=task17-dotenv-refresh-token"},
		{"cli_token", "task17-cli-access-token", "--access-token=task17-cli-access-token"},
		{"credential_url", "dbpassword", "postgres://task17:dbpassword@db.example.invalid/app"},
		{"private_key", "TASK17PRIVATEKEYMATERIAL", "-----BEGIN PRIVATE KEY-----\nTASK17PRIVATEKEYMATERIAL\n-----END PRIVATE KEY-----"},
	}
	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			server := newConformanceServer(t)
			scope := "credential-" + tc.name
			fixture := conformanceRouteFixturesFor(false, tc.prompt, scope)[0]
			resp, body := server.post(t, fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s logs=%s", resp.StatusCode, body, server.logs.String())
			}
			workerText := server.worker.recordsCopy()[0].joinedText()
			if strings.Contains(workerText, tc.value) || strings.Contains(body, tc.value) {
				t.Fatalf("credential was not one-way protected: %q", tc.value)
			}
			entries, err := server.service.TriageCapability().InspectScope(scope)
			if err != nil {
				t.Fatalf("InspectScope: %v", err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Original, tc.value) {
					t.Fatalf("credential entered reversible ledger: %+v", entry)
				}
			}
			header := resp.Header.Get("X-GW-Privacy-Receipt")
			receipt := requireStrictReceipt(t, header)
			decoded, err := base64.RawURLEncoding.DecodeString(header)
			if err != nil {
				t.Fatal(err)
			}
			if len(header) > 512 || strings.Contains(string(decoded), "task17") || receipt.Transformed < 1 {
				t.Fatalf("receipt is unbounded, value-bearing, or missing transformation: %s", decoded)
			}
		})
	}
}

func TestConformanceGeneratedValuesAndReturnedToolArguments(t *testing.T) {
	t.Run("generated_personal_and_technical_are_not_restored", func(t *testing.T) {
		const generatedEmail = "generated.task17@example.com"
		const generatedIP = "172.16.88.9"
		var referenceReceipt *privacy.Receipt
		for _, fixture := range conformanceRouteFixturesFor(false, "safe caller input", "generated-output") {
			t.Run(fixture.name, func(t *testing.T) {
				worker := &captureWorker{response: func(workerRecord) []canonical.Chunk {
					return []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: generatedEmail + " " + generatedIP}}}
				}}
				server := newConformanceServerWith(t, nil, worker)
				resp, body := server.post(t, fixture)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status=%d body=%s", resp.StatusCode, body)
				}
				clientText := assertNativeSuccess(t, fixture, resp, body)
				if strings.Contains(clientText, generatedEmail) || strings.Contains(clientText, generatedIP) {
					t.Fatalf("generated protected values reached caller: %s", clientText)
				}
				if !strings.Contains(clientText, "[EMAIL_1]") ||
					(!strings.Contains(clientText, "198.18.") && !strings.Contains(clientText, "198.19.")) {
					t.Fatalf("generated values were not safely transformed: %s", clientText)
				}
				receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
				want := privacy.Receipt{
					Version: 1, Profile: privacy.ProfileStrict, Scope: "generated-output",
					Coverage: "full", Result: "pass", Transformed: 2,
				}
				if receipt != want {
					t.Fatalf("generated receipt=%+v, want exact %+v", receipt, want)
				}
				if referenceReceipt == nil {
					referenceReceipt = &receipt
				} else if receipt != *referenceReceipt {
					t.Fatalf("route receipt=%+v, want exact cross-route receipt %+v", receipt, *referenceReceipt)
				}
			})
		}
	})

	t.Run("returned_tool_call_arguments_are_transformed", func(t *testing.T) {
		const generated = "tool.generated.task17@example.com"
		worker := &captureWorker{response: func(workerRecord) []canonical.Chunk {
			return []canonical.Chunk{{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
				ID: "call-task17", Name: "lookup", Args: map[string]any{"owner": generated},
			}}}
		}}
		server := newConformanceServerWith(t, nil, worker)
		h := strictHeaders("returned-tool-args")
		body := `{"model":"auto","stream":false,"messages":[{"role":"user","content":"safe"}],"tools":[{"type":"function","function":{"name":"lookup","description":"lookup","parameters":{"type":"object"}}}]}`
		resp, responseBody := server.post(t, routeFixture{name: "tool_args", path: "/v1/chat/completions", headers: h, body: body})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, responseBody)
		}
		clientText := assertNativeSuccess(t, routeFixture{name: "tool_args", path: "/v1/chat/completions", headers: h, body: body}, resp, responseBody)
		if strings.Contains(clientText, generated) || strings.Contains(responseBody, generated) || !strings.Contains(responseBody, "[EMAIL_1]") {
			t.Fatalf("returned tool-call arguments were not protected: %s", responseBody)
		}
		requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
	})
}

func TestConformanceStrictStreamBlocksBeforeAnyPartialBody(t *testing.T) {
	outcomes := []struct {
		name string
		text string
	}{
		{name: "unknown_alias", text: "[IPv4_999]"},
		{name: "generated_credential", text: "sk-proj-Task17GeneratedCredential123456789012345"},
	}
	for _, outcome := range outcomes {
		for _, fixture := range conformanceRouteFixturesFor(true, "safe caller input", "stream-"+outcome.name) {
			t.Run(outcome.name+"_"+fixture.name, func(t *testing.T) {
				worker := &captureWorker{response: func(workerRecord) []canonical.Chunk {
					return []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: outcome.text}}}
				}}
				server := newConformanceServerWith(t, nil, worker)
				resp, body := server.post(t, fixture)
				if resp.StatusCode != http.StatusBadGateway {
					t.Fatalf("status=%d, want 502; body=%s", resp.StatusCode, body)
				}
				assertNativePrivacyError(t, fixture, resp, body, privacy.CodeOutputBlocked)
				for _, marker := range []string{"data: ", "event: ", `"done":true`, outcome.text} {
					if strings.Contains(body, marker) {
						t.Fatalf("strict block released partial/sensitive stream marker %q: %s", marker, body)
					}
				}
				receipt := decodeReceiptFixture(t, resp.Header.Get("X-GW-Privacy-Receipt"))
				if receipt.Profile != privacy.ProfileStrict || receipt.Coverage != "full" || receipt.Result != "block" {
					t.Fatalf("blocked receipt=%+v", receipt)
				}
			})
		}
	}
}

func TestConformanceStrictStreamSuccessHasFullReceipt(t *testing.T) {
	server := newConformanceServer(t)
	var referenceReceipt *privacy.Receipt
	for _, fixture := range conformanceRouteFixtures(true) {
		t.Run(fixture.name, func(t *testing.T) {
			resp, body := server.post(t, fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			clientText := assertNativeSuccess(t, fixture, resp, body)
			if !strings.Contains(clientText, conformanceEmail) || !strings.Contains(clientText, conformanceIPv4) {
				t.Fatalf("buffered strict replay omitted restored content: %s", clientText)
			}
			receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt"))
			want := privacy.Receipt{
				Version: 1, Profile: privacy.ProfileStrict, Scope: conformanceScope,
				Coverage: "full", Result: "pass", Transformed: 3, Restored: 2,
			}
			if receipt != want {
				t.Fatalf("stream receipt=%+v, want exact %+v", receipt, want)
			}
			if referenceReceipt == nil {
				referenceReceipt = &receipt
			} else if receipt != *referenceReceipt {
				t.Fatalf("stream route receipt=%+v, want exact cross-route receipt %+v", receipt, *referenceReceipt)
			}
		})
	}
}

func TestConformanceInputBlockNeverDispatches(t *testing.T) {
	const marker = "task17-residual-protected"
	for _, fixture := range conformanceRouteFixturesFor(false, marker, "input-block") {
		t.Run(fixture.name, func(t *testing.T) {
			classifier := &secondPassClassifier{marker: marker}
			server := newConformanceServerWith(t, func(config *privacy.Config) {
				config.Classifier = classifier
				config.SecretClassifier = nil
			}, nil)
			resp, body := server.post(t, fixture)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d, want 422; body=%s", resp.StatusCode, body)
			}
			assertNativePrivacyError(t, fixture, resp, body, privacy.CodeInputBlocked)
			if got := server.worker.dispatchCount(); got != 0 {
				t.Fatalf("worker dispatches=%d, want 0 after strict input block", got)
			}
			receipt := decodeReceiptFixture(t, resp.Header.Get("X-GW-Privacy-Receipt"))
			if receipt.Coverage != "input" || receipt.Result != "block" || receipt.Blocked != 1 {
				t.Fatalf("input-block receipt=%+v", receipt)
			}
		})
	}
}

func TestConformanceProfileScopeAndNativeRequestErrors(t *testing.T) {
	t.Run("configured_strict_cannot_be_downgraded", func(t *testing.T) {
		server := newConformanceServerWith(t, func(config *privacy.Config) {
			config.DefaultProfile = privacy.ProfileStrict
		}, nil)
		want := privacy.Receipt{
			Version: 1, Profile: privacy.ProfileStrict, Scope: "no-downgrade",
			Coverage: "full", Result: "pass",
		}
		for _, fixture := range conformanceRouteFixturesFor(false, "safe", "no-downgrade") {
			fixture.headers.Set("X-GW-Privacy-Profile", "standard")
			resp, body := server.post(t, fixture)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status=%d body=%s", fixture.name, resp.StatusCode, body)
			}
			assertNativeSuccess(t, fixture, resp, body)
			if receipt := requireStrictReceipt(t, resp.Header.Get("X-GW-Privacy-Receipt")); receipt != want {
				t.Fatalf("no-downgrade receipt=%+v, want exact %+v", receipt, want)
			}
		}
	})

	requestErrors := []struct {
		name   string
		code   string
		mutate func(*routeFixture)
	}{
		{name: "unknown_profile", code: privacy.CodeProfileUnavailable, mutate: func(f *routeFixture) {
			f.headers.Set("X-GW-Privacy-Profile", "unknown")
		}},
		{name: "invalid_scope", code: privacy.CodeRequestInvalid, mutate: func(f *routeFixture) {
			f.headers.Set("X-GW-Privacy-Scope", "invalid scope with spaces")
		}},
	}
	for _, failure := range requestErrors {
		for _, fixture := range conformanceRouteFixturesFor(false, "safe", "request-errors") {
			t.Run(failure.name+"_"+fixture.name, func(t *testing.T) {
				failure.mutate(&fixture)
				server := newConformanceServer(t)
				resp, body := server.post(t, fixture)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", resp.StatusCode, body)
				}
				assertNativePrivacyError(t, fixture, resp, body, failure.code)
				if server.worker.dispatchCount() != 0 {
					t.Fatal("request error dispatched to worker")
				}
				if len(resp.Header.Get("X-GW-Privacy-Receipt")) > 512 {
					t.Fatal("failure receipt exceeded bound")
				}
			})
		}
	}
}

type secondPassClassifier struct {
	marker string
	calls  atomic.Int64
}

func (c *secondPassClassifier) Classify(_ string, value string) []privacy.Finding {
	start := strings.Index(value, c.marker)
	if start < 0 {
		return nil
	}
	call := c.calls.Add(1)
	if call != 2 {
		return nil
	}
	return []privacy.Finding{{
		Entity: "PERSON", Category: privacy.CategoryPersonal, Kind: privacy.MatchNER,
		Start: start, End: start + len(c.marker),
	}}
}

func strictHeaders(scope string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("X-GW-Privacy-Profile", "strict")
	headers.Set("X-GW-Privacy-Scope", scope)
	return headers
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func decodeReceiptFixture(t *testing.T, header string) privacy.Receipt {
	t.Helper()
	if header == "" {
		t.Fatal("privacy receipt missing")
	}
	payload, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("decode receipt base64: %v", err)
	}
	var receipt privacy.Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode receipt JSON: %v", err)
	}
	return receipt
}

func assertNativePrivacyError(t *testing.T, fixture routeFixture, resp conformanceResponse, body, code string) {
	t.Helper()
	if contentType := resp.Header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("privacy error Content-Type=%q, want application/json", contentType)
	}
	object := decodeJSONObject(t, body)
	wantType := "invalid_request_error"
	if resp.StatusCode >= http.StatusInternalServerError {
		wantType = "api_error"
	}
	switch {
	case strings.HasPrefix(fixture.path, "/api/"):
		if len(object) != 1 || requireStringField(t, object, "error") != code {
			t.Fatalf("Ollama error=%s, want exact native error code %q", body, code)
		}
	case fixture.path == "/v1/messages":
		if len(object) != 2 || requireStringField(t, object, "type") != "error" {
			t.Fatalf("Anthropic outer error is not exact/native: %s", body)
		}
		inner := requireObjectField(t, object, "error")
		if len(inner) != 2 || requireStringField(t, inner, "type") != wantType || requireStringField(t, inner, "message") != code {
			t.Fatalf("Anthropic inner error is not exact/native: %s", body)
		}
	default:
		if len(object) != 1 {
			t.Fatalf("OpenAI outer error is not exact/native: %s", body)
		}
		inner := requireObjectField(t, object, "error")
		if len(inner) != 4 || requireStringField(t, inner, "type") != wantType ||
			requireStringField(t, inner, "message") != code || requireStringField(t, inner, "code") != code || inner["param"] != nil {
			t.Fatalf("OpenAI inner error is not exact/native: %s", body)
		}
	}
}

func encodeReceiptFixture(t *testing.T, receipt privacy.Receipt) string {
	t.Helper()
	payload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal receipt: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func mutateReceipt(receipt privacy.Receipt, mutate func(*privacy.Receipt)) privacy.Receipt {
	mutate(&receipt)
	return receipt
}

func validateStrictReceipt(header string) (privacy.Receipt, error) {
	if header == "" {
		return privacy.Receipt{}, errors.New("strict privacy receipt is missing")
	}
	if len(header) > 512 {
		return privacy.Receipt{}, errors.New("strict privacy receipt exceeds bound")
	}
	payload, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return privacy.Receipt{}, fmt.Errorf("strict privacy receipt base64: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var receipt privacy.Receipt
	if err := decoder.Decode(&receipt); err != nil {
		return privacy.Receipt{}, fmt.Errorf("strict privacy receipt JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return privacy.Receipt{}, errors.New("strict privacy receipt has trailing JSON")
	}
	if receipt.Version != 1 || receipt.Profile != privacy.ProfileStrict ||
		receipt.Coverage != "full" || receipt.Result != "pass" {
		return privacy.Receipt{}, errors.New("strict privacy receipt does not prove full pass")
	}
	if receipt.Transformed < 0 || receipt.Restored < 0 || receipt.Blocked != 0 {
		return privacy.Receipt{}, errors.New("strict privacy receipt has invalid counts")
	}
	return receipt, nil
}

func requireStrictReceipt(t *testing.T, header string) privacy.Receipt {
	t.Helper()
	receipt, err := validateStrictReceipt(header)
	if err != nil {
		t.Fatalf("validate strict privacy receipt: %v", err)
	}
	return receipt
}
