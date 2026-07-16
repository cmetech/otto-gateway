//go:build e2e

// This file is part of package e2e_test (same package as e2e_test.go). It adds
// OpenAI API contract coverage — the surface the Pi-SDK chat CLI consumes —
// mirroring the structure of the existing Ollama and Anthropic subtests.
//
// It REUSES the shared helpers declared in e2e_test.go (gateOrSkip,
// bootGateway, resolveKiro, freePort, readAll, TestMain, moduleRoot) and the
// generic ollamaRequest helper declared in ollama_e2e_test.go, and MUST NOT
// redefine any of them — doing so would be a redeclaration compile error.
//
// This is the automated counterpart to the Phase 3 HUMAN-UAT Pi-SDK round-trip
// (SC2 / SURF-06). Pi (@earendil-works/pi-ai) configures the OpenAI provider
// with baseUrl=…/v1 and drives the official `openai` npm SDK, which hard-codes
// stream:true — so the load-bearing acceptance path is the SSE stream asserted
// in ChatCompletions_Streaming below. The full real-SDK round-trip is the
// opt-in Node harness in TestE2E_OpenAI_SDK_RoundTrip (sdk/openai_roundtrip.mjs).
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestE2E_OpenAI boots ONE gateway (default ENABLED_SURFACES so OpenAI is
// mounted, AUTH_TOKEN=e2e-token, real kiro via KIRO_CMD) and runs the OpenAI
// contract cases as subtests sharing that single warmup.
func TestE2E_OpenAI(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	const auth = "Bearer e2e-token"

	// 1. Unauthorized — POST /v1/chat/completions with NO auth → 401. Auth
	// rejects before kiro is touched (no warmup dependency).
	t.Run("Unauthorized", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no-auth status: got %d, want 401", resp.StatusCode)
		}
	})

	// 2. Models — GET /v1/models (Bearer) → 200, object=="list", non-empty
	// data[] with each entry object=="model" and an entry id=="auto" (always
	// prepended). Does not require the engine.
	t.Run("Models", func(t *testing.T) {
		resp := ollamaRequest(t, http.MethodGet, baseURL+"/v1/models", nil, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		list := decodeModelList(t, resp)
		if list.Object != "list" {
			t.Errorf("object: got %q, want list", list.Object)
		}
		if len(list.Data) == 0 {
			t.Fatal("data: empty, want non-empty")
		}
		sawAuto := false
		for _, m := range list.Data {
			if m.Object != "model" {
				t.Errorf("data entry %q object: got %q, want model", m.ID, m.Object)
			}
			if m.ID == "auto" {
				sawAuto = true
			}
		}
		if !sawAuto {
			t.Errorf("data: no entry with id %q; got %+v", "auto", list.Data)
		}
	})

	// 3. ModelsMatchTags (SC3) — the model id set at /v1/models MUST equal the
	// model set at /api/tags (both sourced from the same pool ModelCatalog +
	// synthetic "auto"). OpenAI data[].id corresponds to Ollama models[].model.
	t.Run("ModelsMatchTags", func(t *testing.T) {
		modelsResp := ollamaRequest(t, http.MethodGet, baseURL+"/v1/models", nil, auth)
		defer func() { _ = modelsResp.Body.Close() }()
		if modelsResp.StatusCode != http.StatusOK {
			t.Fatalf("/v1/models status: got %d, want 200", modelsResp.StatusCode)
		}
		openaiList := decodeModelList(t, modelsResp)
		openaiIDs := make(map[string]struct{}, len(openaiList.Data))
		for _, m := range openaiList.Data {
			openaiIDs[m.ID] = struct{}{}
		}

		tagsResp := ollamaRequest(t, http.MethodGet, baseURL+"/api/tags", nil, auth)
		defer func() { _ = tagsResp.Body.Close() }()
		if tagsResp.StatusCode != http.StatusOK {
			t.Fatalf("/api/tags status: got %d, want 200", tagsResp.StatusCode)
		}
		var tags struct {
			Models []struct {
				Model string `json:"model"`
			} `json:"models"`
		}
		if err := json.NewDecoder(tagsResp.Body).Decode(&tags); err != nil {
			t.Fatalf("decode tags: %v", err)
		}
		tagIDs := make(map[string]struct{}, len(tags.Models))
		for _, m := range tags.Models {
			tagIDs[m.Model] = struct{}{}
		}

		if len(openaiIDs) != len(tagIDs) {
			t.Errorf("model set sizes differ: /v1/models=%d /api/tags=%d (openai=%v tags=%v)",
				len(openaiIDs), len(tagIDs), keys(openaiIDs), keys(tagIDs))
		}
		for id := range openaiIDs {
			if _, ok := tagIDs[id]; !ok {
				t.Errorf("/v1/models id %q not present in /api/tags set %v", id, keys(tagIDs))
			}
		}
	})

	// 4. ChatCompletions_NonStreaming (SC1) — POST /v1/chat/completions
	// (Bearer, stream:false) → 200, application/json, a SINGLE chat.completion
	// object: object=="chat.completion", choices[0].index==0,
	// choices[0].message.role=="assistant", message.content non-empty,
	// finish_reason non-empty. Real kiro — inherits bootGateway warmup-skip.
	//
	// ChatCompletions_NonStreaming ratifies STRM-05 (stream:false regression for
	// OpenAI surface). Note: OpenAI wire.Stream is bool (absent=false), so absent
	// field also routes to non-streaming — this matches the OpenAI public spec and
	// is correct behavior.
	t.Run("ChatCompletions_NonStreaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}],"stream":false}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix", ct)
		}

		dec := json.NewDecoder(resp.Body)
		var cc struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := dec.Decode(&cc); err != nil {
			t.Fatalf("decode chat.completion: %v", err)
		}
		// Single JSON object (not multi-frame): a second Decode must be io.EOF.
		var throwaway json.RawMessage
		if err := dec.Decode(&throwaway); err != io.EOF {
			t.Errorf("second decode: got %v, want io.EOF (response must be a single JSON object)", err)
		}
		if !strings.HasPrefix(cc.ID, "chatcmpl-") {
			t.Errorf("id: got %q, want chatcmpl- prefix", cc.ID)
		}
		if cc.Object != "chat.completion" {
			t.Errorf("object: got %q, want chat.completion", cc.Object)
		}
		if len(cc.Choices) == 0 {
			t.Fatal("choices: empty")
		}
		if cc.Choices[0].Index != 0 {
			t.Errorf("choices[0].index: got %d, want 0", cc.Choices[0].Index)
		}
		if cc.Choices[0].Message.Role != "assistant" {
			t.Errorf("choices[0].message.role: got %q, want assistant", cc.Choices[0].Message.Role)
		}
		if cc.Choices[0].Message.Content == "" {
			t.Error("choices[0].message.content: empty")
		}
		if cc.Choices[0].FinishReason == "" {
			t.Error("choices[0].finish_reason: empty, want non-null (e.g. stop)")
		}
	})

	// 5. ChatCompletions_Streaming (SC2 / the Pi acceptance path) — POST
	// /v1/chat/completions (Bearer, stream:true) → 200, Content-Type
	// text/event-stream, and a well-formed OpenAI chunk stream:
	// data:-only framing, role-first delta, content deltas, a finish_reason
	// frame, and a terminal `data: [DONE]`. Pi hard-codes stream:true so this
	// is the load-bearing surface for SURF-06.
	//
	// ChatCompletions_Streaming ratifies STRM-02 (OpenAI SSE) and STRM-03 (same
	// canonical channel as Ollama and Anthropic — all three surfaces call
	// engine.Run().Stream().Chunks() via the engine adapter).
	t.Run("ChatCompletions_Streaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}],"stream":true}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
		}
		assertOpenAISSE(t, resp)
	})

	// 6. Completions_NonStreaming — POST /v1/completions (Bearer, stream:false)
	// → 200, a SINGLE text_completion object: object=="text_completion",
	// choices[0].text non-empty, finish_reason non-empty, logprobs null
	// (D-03 accept-and-ignore). prompt is a plain string.
	t.Run("Completions_NonStreaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","prompt":"say hi","stream":false,"logprobs":5,"best_of":3}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		dec := json.NewDecoder(resp.Body)
		var tc struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Choices []struct {
				Index        int             `json:"index"`
				Text         string          `json:"text"`
				FinishReason string          `json:"finish_reason"`
				Logprobs     json.RawMessage `json:"logprobs"`
			} `json:"choices"`
		}
		if err := dec.Decode(&tc); err != nil {
			t.Fatalf("decode text_completion: %v", err)
		}
		var throwaway json.RawMessage
		if err := dec.Decode(&throwaway); err != io.EOF {
			t.Errorf("second decode: got %v, want io.EOF (response must be a single JSON object)", err)
		}
		if !strings.HasPrefix(tc.ID, "cmpl-") {
			t.Errorf("id: got %q, want cmpl- prefix", tc.ID)
		}
		if tc.Object != "text_completion" {
			t.Errorf("object: got %q, want text_completion", tc.Object)
		}
		if len(tc.Choices) == 0 {
			t.Fatal("choices: empty")
		}
		if tc.Choices[0].Text == "" {
			t.Error("choices[0].text: empty")
		}
		if tc.Choices[0].FinishReason == "" {
			t.Error("choices[0].finish_reason: empty, want non-null")
		}
		// Advanced params (logprobs, best_of) were sent and must be ignored,
		// not honored: logprobs must serialize as JSON null.
		if s := strings.TrimSpace(string(tc.Choices[0].Logprobs)); s != "null" && s != "" {
			t.Errorf("choices[0].logprobs: got %q, want null (D-03 accept-and-ignore)", s)
		}
	})
}

// TestE2E_SurfaceGating_OpenAINotMounted boots with only the Ollama surface and
// proves the OpenAI routes are NOT mounted (404) while /api/chat IS mounted —
// the deploy-time ENABLED_SURFACES gate (SURF-02) excludes openai cleanly.
func TestE2E_SurfaceGating_OpenAINotMounted(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, map[string]string{"ENABLED_SURFACES": "ollama"})
	defer cleanup()

	const auth = "Bearer e2e-token"

	t.Run("ChatCompletionsNotMounted", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("/v1/chat/completions status with openai disabled: got %d, want 404", resp.StatusCode)
		}
	})

	t.Run("ModelsNotMounted", func(t *testing.T) {
		resp := ollamaRequest(t, http.MethodGet, baseURL+"/v1/models", nil, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("/v1/models status with openai disabled: got %d, want 404", resp.StatusCode)
		}
	})
}

// TestE2E_OpenAI_SDK_RoundTrip is the opt-in Node `openai` SDK harness — the
// automated form of the Pi-SDK HUMAN-UAT (Pi drives the official openai npm SDK
// under the hood). It skips cleanly when node is absent OR the harness is not
// installed (no tests/e2e/sdk/node_modules and GW_E2E_SDK unset). When ready
// it boots the gateway, points OPENAI_BASE_URL at it, and runs the .mjs
// round-trip (non-stream + stream), asserting exit code 0.
func TestE2E_OpenAI_SDK_RoundTrip(t *testing.T) {
	gateOrSkip(t)

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed — run: make e2e-sdk-setup")
	}
	// CWD is tests/e2e/, so the relative node_modules path is "sdk/node_modules".
	if _, statErr := os.Stat("sdk/node_modules"); statErr != nil && os.Getenv("GW_E2E_SDK") != "1" {
		t.Skip("SDK harness not installed — run: make e2e-sdk-setup (or set GW_E2E_SDK=1)")
	}

	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// The .mjs is referenced from the module root, so run it from "../..".
	cmd := exec.CommandContext(ctx, "node", "tests/e2e/sdk/openai_roundtrip.mjs")
	cmd.Dir = moduleRoot
	cmd.Env = append(
		os.Environ(),
		// The official openai SDK appends /chat/completions etc. to baseURL, so
		// it must include the /v1 prefix (mirrors Pi's baseUrl=…/v1).
		"OPENAI_BASE_URL="+baseURL+"/v1",
		"OPENAI_API_KEY=e2e-token",
	)
	combined, runErr := cmd.CombinedOutput()
	t.Logf("openai sdk harness output:\n%s", string(combined))
	if runErr != nil {
		t.Fatalf("node openai_roundtrip.mjs failed: %v", runErr)
	}
}

// openaiModelList mirrors the GET /v1/models response shape for decoding.
type openaiModelList struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// decodeModelList decodes a GET /v1/models response body.
func decodeModelList(t *testing.T, resp *http.Response) openaiModelList {
	t.Helper()
	var list openaiModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode model list: %v", err)
	}
	return list
}

// keys returns the keys of a set (for diagnostic messages).
func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// assertOpenAISSE walks an OpenAI chat-completions SSE body and asserts the
// flat OpenAI framing contract (distinct from Anthropic's event-named frames):
//
//   - Every non-blank line is a `data: ` line (NO `event:` lines).
//   - Each non-[DONE] payload is a valid chat.completion.chunk JSON object.
//   - The first chunk's delta carries role=="assistant".
//   - At least one chunk carries a non-null finish_reason.
//   - The stream terminates with a literal `data: [DONE]` and NOTHING follows it.
//   - Accumulated delta content is non-empty (the round-trip actually produced text).
func assertOpenAISSE(t *testing.T, resp *http.Response) {
	t.Helper()

	var (
		frames     int
		sawRole    bool
		sawFinish  bool
		sawDone    bool
		content    strings.Builder
		firstChunk = true
	)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	lineIdx := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineIdx++
		if line == "" {
			continue // blank separator between `data:` frames
		}
		if sawDone {
			t.Fatalf("framing violation at line %d: data line %q appeared AFTER data: [DONE]", lineIdx, line)
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("framing violation at line %d: got %q, want a `data: ` line (OpenAI uses data-only framing, no event: lines)", lineIdx, line)
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}

		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("framing violation at line %d: chunk payload not valid JSON: %v; payload=%s", lineIdx, err, payload)
		}
		frames++
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("chunk object at line %d: got %q, want chat.completion.chunk", lineIdx, chunk.Object)
		}
		if len(chunk.Choices) == 0 {
			t.Fatalf("chunk at line %d has empty choices[]", lineIdx)
		}
		ch := chunk.Choices[0]
		if firstChunk {
			if ch.Delta.Role != "assistant" {
				t.Errorf("first chunk delta.role: got %q, want assistant (role-first contract)", ch.Delta.Role)
			}
			firstChunk = false
		}
		if ch.Delta.Role == "assistant" {
			sawRole = true
		}
		content.WriteString(ch.Delta.Content)
		if ch.FinishReason != nil {
			sawFinish = true
			if *ch.FinishReason == "" {
				t.Error("a chunk carried an empty (\"\") finish_reason; want a non-empty value like stop")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	if frames == 0 {
		t.Fatal("no chat.completion.chunk frames received")
	}
	if !sawRole {
		t.Error("no chunk carried delta.role=assistant")
	}
	if !sawFinish {
		t.Error("no chunk carried a non-null finish_reason")
	}
	if !sawDone {
		t.Error("stream did not terminate with data: [DONE]")
	}
	if content.Len() == 0 {
		t.Error("accumulated streamed content was empty (round-trip produced no text)")
	}
	t.Logf("streaming: %d chunk frames, content=%q", frames, truncate(content.String(), 60))
}

// truncate shortens s to at most n runes for log output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestE2E_OpenAI_ModelCapabilities boots a gateway backed by the fake kiro with
// a scripted catalog: a registered model (claude-sonnet-4.5), an unknown model
// (unknown-model-zzz, absent from the registry). It asserts the
// /v1/model-capabilities contract. (spec §11.4)
func TestE2E_OpenAI_ModelCapabilities(t *testing.T) {
	gateOrSkip(t)
	cmd, env := FakeKiro(t, Script{})
	baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{
		"KIRO_CMD":            cmd,
		"GW_FAKE_KIRO_MODELS": "auto:Auto,claude-sonnet-4.5:Claude Sonnet 4.5,unknown-model-zzz:Unknown Model",
	}))
	defer cleanup()

	resp := ollamaRequest(t, http.MethodGet, baseURL+"/v1/model-capabilities", nil, "Bearer e2e-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
	}

	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID            string                       `json:"id"`
			Name          string                       `json:"name"`
			Available     bool                         `json:"available"`
			SelectionMode string                       `json:"selection_mode"`
			Capabilities  map[string]string            `json:"capabilities"`
			Evidence      map[string]map[string]string `json:"evidence"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Object != "list" || len(list.Data) == 0 || list.Data[0].ID != "auto" {
		t.Fatalf("auto not first / bad envelope: %+v", list)
	}

	byID := map[string]map[string]string{}
	evByID := map[string]map[string]map[string]string{}
	ids := make([]string, 0, len(list.Data))
	for _, e := range list.Data {
		byID[e.ID] = e.Capabilities
		evByID[e.ID] = e.Evidence
		ids = append(ids, e.ID)
	}

	// 1+2. Registered model present with ALL four seeded states AND their
	// evidence — guards against silently dropping/corrupting a verified model's
	// tools/vision/reasoning or its evidence (claude-sonnet-4.5 is seeded
	// completion+tools+vision+reasoning=supported).
	reg, ok := byID["claude-sonnet-4.5"]
	if !ok {
		t.Fatalf("registered model claude-sonnet-4.5 not returned; got ids %v", ids)
	}
	for _, k := range []string{"completion", "tools", "vision", "reasoning"} {
		if reg[k] != "supported" {
			t.Errorf("claude-sonnet-4.5 %q: got %q, want supported", k, reg[k])
		}
		if _, hasEv := evByID["claude-sonnet-4.5"][k]; !hasEv {
			t.Errorf("claude-sonnet-4.5 %q: missing evidence object", k)
		}
	}

	// 3. Unknown model present but all-unknown.
	unk, ok := byID["unknown-model-zzz"]
	if !ok {
		t.Fatalf("unknown model not returned; got ids %v", ids)
	}
	for _, k := range []string{"completion", "tools", "vision", "reasoning"} {
		if unk[k] != "unknown" {
			t.Errorf("unknown-model-zzz %q: got %q, want unknown", k, unk[k])
		}
	}

	// 4. A registry model absent from the live catalog is NOT returned.
	if _, present := byID["claude-haiku-4.5"]; present {
		t.Errorf("stale registry model claude-haiku-4.5 leaked into response")
	}

	// 5. Auth parity with /v1/models: both are read-only catalog endpoints gated
	// by IP-allowlist only (no bearer — accepted T-8 posture). With NO
	// Authorization header, both must still return 200, proving they share the
	// same no-bearer posture by route placement.
	for _, path := range []string{"/v1/models", "/v1/model-capabilities"} {
		r := ollamaRequest(t, http.MethodGet, baseURL+path, nil, "")
		if r.StatusCode != http.StatusOK {
			t.Errorf("no-auth GET %s: got %d, want 200 (IP-allowlist-only, no bearer)", path, r.StatusCode)
		}
		_ = r.Body.Close()
	}
}
