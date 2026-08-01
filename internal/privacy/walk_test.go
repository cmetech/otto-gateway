package privacy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestTransformStringsCopiesContainersAndPreservesMapKeys(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"password": "alpha",
		"nested": []any{
			"plain",
			map[string]any{"access_token": "bravo"},
		},
		"number": 42,
	}
	original := map[string]any{
		"password": "alpha",
		"nested": []any{
			"plain",
			map[string]any{"access_token": "bravo"},
		},
		"number": 42,
	}

	got, err := TransformStrings(input, func(key, value string) (string, error) {
		return key + "=" + value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"password": "password=alpha",
		"nested": []any{
			"nested=plain",
			map[string]any{"access_token": "access_token=bravo"},
		},
		"number": 42,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TransformStrings()=%#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("TransformStrings mutated input: got %#v, want %#v", input, original)
	}
	if _, ok := got.(map[string]any)["password"]; !ok {
		t.Fatal("TransformStrings changed a map key")
	}
}

func TestTransformStringsVisitsMapValuesInStableKeyOrder(t *testing.T) {
	t.Parallel()

	var visited []string
	_, err := TransformStrings(map[string]any{
		"zulu":  "last",
		"alpha": "first",
		"mike":  "middle",
	}, func(key, value string) (string, error) {
		visited = append(visited, key)
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "mike", "zulu"}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visit order=%v, want %v", visited, want)
	}
}

func TestTransformStringsPropagatesCallbackErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("stop")
	got, err := TransformStrings([]any{"first", "second"}, func(_ string, value string) (string, error) {
		if value == "second" {
			return "", wantErr
		}
		return strings.ToUpper(value), nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if got != nil {
		t.Fatalf("result=%#v, want nil on callback failure", got)
	}
}

func TestTransformStringsFailsClosedBeyondDepth64(t *testing.T) {
	t.Parallel()

	allowed := nestedMaps(64)
	if _, err := TransformStrings(allowed, func(_ string, value string) (string, error) {
		return value, nil
	}); err != nil {
		t.Fatalf("depth 64 returned error: %v", err)
	}

	tooDeep := nestedMaps(65)
	got, err := TransformStrings(tooDeep, func(_ string, value string) (string, error) {
		return value, nil
	})
	if err == nil {
		t.Fatal("depth 65 returned nil error")
	}
	if got != nil {
		t.Fatalf("depth 65 returned unvisited subtree: %#v", got)
	}
}

func TestTransformRequestStringsCoversOnlyCanonicalContentSurfaces(t *testing.T) {
	t.Parallel()

	req := canonical.ChatRequest{
		Model:              "model-unchanged",
		System:             "system-secret",
		StopSequences:      []string{"stop-unchanged"},
		WorkingDirOverride: "cwd-unchanged",
		Metadata:           map[string]any{"metadata": "unchanged"},
		Format:             &canonical.Format{Type: "type-unchanged", Schema: map[string]any{"schema": "unchanged"}},
		Tools: []canonical.ToolSpec{{
			Name:        "tool-name-unchanged",
			Description: "description-unchanged",
			Parameters:  map[string]any{"parameter": "unchanged"},
		}},
		ResourceLinks: []canonical.ResourceLinkBlock{{URI: "file:///unchanged"}},
		Messages: []canonical.Message{{
			ToolCallID: "call-id-unchanged",
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "message-secret"},
				{Kind: canonical.ContentKindImage, Text: "invalid-text-unchanged", Image: &canonical.ImagePart{MIME: "mime-unchanged", DataBase64: "image-unchanged"}},
				{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{ID: "use-id-unchanged", Name: "use-name-unchanged", Input: map[string]any{"password": "input-secret", "nested": []any{"nested-secret"}}}},
				{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "result-id-unchanged", Content: "result-secret"}},
			},
			ToolCalls: []canonical.ToolCall{{ID: "call-id-unchanged", Name: "call-name-unchanged", Arguments: map[string]any{"token": "argument-secret"}}},
		}},
	}

	if err := TransformRequestStrings(&req, taggedTransform); err != nil {
		t.Fatal(err)
	}

	if req.System != "[system]system-secret" {
		t.Errorf("System=%q", req.System)
	}
	if req.Messages[0].Content[0].Text != "[text]message-secret" {
		t.Errorf("message text=%q", req.Messages[0].Content[0].Text)
	}
	if got := req.Messages[0].Content[2].ToolUse.Input["password"]; got != "[password]input-secret" {
		t.Errorf("tool input=%q", got)
	}
	nested := req.Messages[0].Content[2].ToolUse.Input["nested"].([]any)
	if nested[0] != "[nested]nested-secret" {
		t.Errorf("nested tool input=%q", nested[0])
	}
	if got := req.Messages[0].Content[3].ToolResult.Content; got != "[content]result-secret" {
		t.Errorf("tool result=%q", got)
	}
	if got := req.Messages[0].ToolCalls[0].Arguments["token"]; got != "[token]argument-secret" {
		t.Errorf("tool-call argument=%q", got)
	}

	if req.Model != "model-unchanged" || req.WorkingDirOverride != "cwd-unchanged" ||
		req.StopSequences[0] != "stop-unchanged" || req.Metadata["metadata"] != "unchanged" ||
		req.Format.Schema["schema"] != "unchanged" || req.Tools[0].Description != "description-unchanged" ||
		req.ResourceLinks[0].URI != "file:///unchanged" || req.Messages[0].ToolCallID != "call-id-unchanged" ||
		req.Messages[0].Content[1].Text != "invalid-text-unchanged" || req.Messages[0].Content[1].Image.DataBase64 != "image-unchanged" ||
		req.Messages[0].Content[2].ToolUse.Name != "use-name-unchanged" || req.Messages[0].ToolCalls[0].Name != "call-name-unchanged" {
		t.Fatalf("TransformRequestStrings changed an unlisted canonical field: %#v", req)
	}
}

func TestTransformResponseStringsCoversResponseTextAndReturnedToolArguments(t *testing.T) {
	t.Parallel()

	resp := canonical.ChatResponse{
		ID:    "response-id-unchanged",
		Model: "model-unchanged",
		Message: canonical.Message{
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "response-secret"}},
			ToolCalls: []canonical.ToolCall{{
				ID:        "tool-id-unchanged",
				Name:      "tool-name-unchanged",
				Arguments: map[string]any{"api_key": "returned-secret"},
			}},
		},
	}

	if err := TransformResponseStrings(&resp, taggedTransform); err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content[0].Text != "[text]response-secret" {
		t.Errorf("response text=%q", resp.Message.Content[0].Text)
	}
	if got := resp.Message.ToolCalls[0].Arguments["api_key"]; got != "[api_key]returned-secret" {
		t.Errorf("returned argument=%q", got)
	}
	if resp.ID != "response-id-unchanged" || resp.Model != "model-unchanged" || resp.Message.ToolCalls[0].Name != "tool-name-unchanged" {
		t.Fatalf("TransformResponseStrings changed an unlisted field: %#v", resp)
	}
}

func TestVisitRequestStringsIsReadOnlyAndStable(t *testing.T) {
	t.Parallel()

	req := canonical.ChatRequest{
		System: "system",
		Messages: []canonical.Message{
			{
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "message"},
					{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{Input: map[string]any{"z": "last", "a": "first"}}},
					{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{Content: "result"}},
				},
				ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{"argument": "value"}}},
			},
		},
	}
	before := canonicalSnapshot(t, req)
	var visited []string
	if err := VisitRequestStrings(&req, func(key, value string) error {
		visited = append(visited, fmt.Sprintf("%s=%s", key, value))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"system=system", "text=message", "a=first", "z=last", "content=result", "argument=value"}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited=%v, want %v", visited, want)
	}
	if after := canonicalSnapshot(t, req); !bytes.Equal(after, before) {
		t.Fatalf("VisitRequestStrings mutated nested request state: before=%s after=%s", before, after)
	}
}

func TestVisitResponseStringsIsReadOnly(t *testing.T) {
	t.Parallel()

	resp := canonical.ChatResponse{Message: canonical.Message{
		Content:   []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "response"}},
		ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{"token": "value"}}},
	}}
	before := canonicalSnapshot(t, resp)

	var visited []string
	if err := VisitResponseStrings(&resp, func(key, value string) error {
		visited = append(visited, key+"="+value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"text=response", "token=value"}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited=%v, want %v", visited, want)
	}
	if after := canonicalSnapshot(t, resp); !bytes.Equal(after, before) {
		t.Fatalf("VisitResponseStrings mutated nested response state: before=%s after=%s", before, after)
	}
}

func TestCanonicalTraversalClassifiesAcronymKeys(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	redact := func(key, value string) (string, error) {
		return classifier.Redact(key, value), nil
	}
	req := canonical.ChatRequest{Messages: []canonical.Message{{
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{Input: map[string]any{
				"APIKey":             "request-api-value",
				"AWSAccessKeyID":     "request-aws-id",
				"AWSSecretAccessKey": "request-aws-secret",
			}},
		}},
	}}}
	resp := canonical.ChatResponse{Message: canonical.Message{ToolCalls: []canonical.ToolCall{{
		Arguments: map[string]any{
			"GitHubToken": "response-github-value",
			"OAuthToken":  "response-oauth-value",
		},
	}}}}

	if err := TransformRequestStrings(&req, redact); err != nil {
		t.Fatal(err)
	}
	if err := TransformResponseStrings(&resp, redact); err != nil {
		t.Fatal(err)
	}

	input := req.Messages[0].Content[0].ToolUse.Input
	for _, key := range []string{"APIKey", "AWSAccessKeyID", "AWSSecretAccessKey"} {
		if input[key] != "[REDACTED]" {
			t.Errorf("request %s=%q, want redacted", key, input[key])
		}
	}
	arguments := resp.Message.ToolCalls[0].Arguments
	for _, key := range []string{"GitHubToken", "OAuthToken"} {
		if arguments[key] != "[REDACTED]" {
			t.Errorf("response %s=%q, want redacted", key, arguments[key])
		}
	}
}

func TestTransformAndVisitStringsPreserveArrayKeyContext(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"password": []any{
			"password-one",
			[]any{"password-two"},
			map[string]any{
				"label":         "safe-label",
				"client_secret": []any{"client-three"},
			},
		},
		"client_secret": []any{"client-one"},
	}
	wantVisits := []string{
		"client_secret=client-one",
		"password=password-one",
		"password=password-two",
		"client_secret=client-three",
		"label=safe-label",
	}

	var transformVisits []string
	got, err := TransformStrings(input, func(key, value string) (string, error) {
		transformVisits = append(transformVisits, key+"="+value)
		return "[" + key + "]" + value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var visitVisits []string
	if err := visitStrings(input, "", 0, func(key, value string) error {
		visitVisits = append(visitVisits, key+"="+value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transformVisits, wantVisits) {
		t.Fatalf("transform visits=%v, want %v", transformVisits, wantVisits)
	}
	if !reflect.DeepEqual(visitVisits, transformVisits) {
		t.Fatalf("visit sequence=%v, want transform sequence %v", visitVisits, transformVisits)
	}

	transformed := got.(map[string]any)
	passwords := transformed["password"].([]any)
	if passwords[0] != "[password]password-one" || passwords[1].([]any)[0] != "[password]password-two" {
		t.Fatalf("password arrays lost key context: %#v", passwords)
	}
	nested := passwords[2].(map[string]any)
	if nested["client_secret"].([]any)[0] != "[client_secret]client-three" || nested["label"] != "[label]safe-label" {
		t.Fatalf("nested map did not override array context: %#v", nested)
	}
}

func TestCanonicalTransformAndVisitPreserveArrayKeyContext(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	transformReq, transformResp := canonicalArrayFixtures()
	visitReq, visitResp := canonicalArrayFixtures()
	var transformVisits []string
	transform := func(key, value string) (string, error) {
		transformVisits = append(transformVisits, key+"="+value)
		return classifier.Redact(key, value), nil
	}
	if err := TransformRequestStrings(&transformReq, transform); err != nil {
		t.Fatal(err)
	}
	if err := TransformResponseStrings(&transformResp, transform); err != nil {
		t.Fatal(err)
	}

	var visitVisits []string
	visit := func(key, value string) error {
		visitVisits = append(visitVisits, key+"="+value)
		return nil
	}
	if err := VisitRequestStrings(&visitReq, visit); err != nil {
		t.Fatal(err)
	}
	if err := VisitResponseStrings(&visitResp, visit); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(visitVisits, transformVisits) {
		t.Fatalf("visit sequence=%v, want transform sequence %v", visitVisits, transformVisits)
	}

	requestInput := transformReq.Messages[0].Content[0].ToolUse.Input["password"].([]any)
	if requestInput[0] != "[REDACTED]" || requestInput[1].(map[string]any)["client_secret"].([]any)[0] != "[REDACTED]" {
		t.Fatalf("request tool input arrays were not redacted: %#v", requestInput)
	}
	if got := transformReq.Messages[0].ToolCalls[0].Arguments["client_secret"].([]any)[0]; got != "[REDACTED]" {
		t.Fatalf("request tool-call argument=%q, want redacted", got)
	}
	if got := transformResp.Message.Content[0].ToolUse.Input["password"].([]any)[0]; got != "[REDACTED]" {
		t.Fatalf("response tool input=%q, want redacted", got)
	}
	if got := transformResp.Message.ToolCalls[0].Arguments["client_secret"].([]any)[0]; got != "[REDACTED]" {
		t.Fatalf("response tool-call argument=%q, want redacted", got)
	}
}

func TestTransformRequestStringsPreservesExactOneWayMarkersUnderCredentialKeys(t *testing.T) {
	t.Parallel()

	const (
		apiMarker      = "[SECRET:API_KEY_0123456789AB]"
		passwordMarker = "[SECRET:PASSWORD_0123456789AB]"
	)
	classifier := NewSecretClassifier()
	req := canonical.ChatRequest{Messages: []canonical.Message{{
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{Input: map[string]any{
				"api_key": apiMarker,
				"password": []any{
					passwordMarker,
					[]any{passwordMarker},
				},
			}},
		}},
		ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{
			"password": []any{passwordMarker},
		}}},
	}}}

	if err := TransformRequestStrings(&req, func(key, value string) (string, error) {
		return classifier.Redact(key, value), nil
	}); err != nil {
		t.Fatal(err)
	}
	input := req.Messages[0].Content[0].ToolUse.Input
	if got := input["api_key"]; got != apiMarker {
		t.Fatalf("tool input api_key=%q, want exact marker", got)
	}
	passwords := input["password"].([]any)
	if passwords[0] != passwordMarker || passwords[1].([]any)[0] != passwordMarker {
		t.Fatalf("nested tool input markers changed: %#v", passwords)
	}
	if got := req.Messages[0].ToolCalls[0].Arguments["password"].([]any)[0]; got != passwordMarker {
		t.Fatalf("tool-call argument marker=%q, want exact marker", got)
	}
}

func canonicalArrayFixtures() (canonical.ChatRequest, canonical.ChatResponse) {
	req := canonical.ChatRequest{Messages: []canonical.Message{{
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{Input: map[string]any{
				"password": []any{
					"request-password",
					map[string]any{"client_secret": []any{"request-client"}},
				},
			}},
		}},
		ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{
			"client_secret": []any{"request-argument"},
		}}},
	}}}
	resp := canonical.ChatResponse{Message: canonical.Message{
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{Input: map[string]any{
				"password": []any{"response-password"},
			}},
		}},
		ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{
			"client_secret": []any{"response-client"},
		}}},
	}}
	return req, resp
}

func TestVisitStringsDepthLimit(t *testing.T) {
	t.Parallel()

	requestAtDepth := func(depth int) canonical.ChatRequest {
		return canonical.ChatRequest{Messages: []canonical.Message{{
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindToolUse,
				ToolUse: &canonical.ToolUsePart{
					Input: nestedMaps(depth).(map[string]any),
				},
			}},
		}}}
	}
	allowed := requestAtDepth(64)
	if err := VisitRequestStrings(&allowed, func(_, _ string) error { return nil }); err != nil {
		t.Fatalf("VisitRequestStrings depth 64 returned error: %v", err)
	}

	tooDeep := requestAtDepth(65)
	before := canonicalSnapshot(t, tooDeep)
	err := VisitRequestStrings(&tooDeep, func(_, _ string) error { return nil })
	if !errors.Is(err, errStringTraversalTooDeep) {
		t.Fatalf("VisitRequestStrings depth 65 error=%v, want %v", err, errStringTraversalTooDeep)
	}
	if after := canonicalSnapshot(t, tooDeep); !bytes.Equal(after, before) {
		t.Fatalf("depth error mutated request: before=%s after=%s", before, after)
	}
}

func TestVisitStringsPropagatesCallbackErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("visit stopped")
	req := canonical.ChatRequest{System: "system", Messages: []canonical.Message{{
		Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "request-text"}},
	}}}
	err := VisitRequestStrings(&req, func(key, _ string) error {
		if key == "text" {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("VisitRequestStrings error=%v, want %v", err, wantErr)
	}

	resp := canonical.ChatResponse{Message: canonical.Message{ToolCalls: []canonical.ToolCall{{
		Arguments: map[string]any{"token": "response-value"},
	}}}}
	err = VisitResponseStrings(&resp, func(key, _ string) error {
		if key == "token" {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("VisitResponseStrings error=%v, want %v", err, wantErr)
	}
}

func nestedMaps(depth int) any {
	var value any = "leaf"
	for range depth {
		value = map[string]any{"next": value}
	}
	return value
}

func taggedTransform(key, value string) (string, error) {
	if key == "" {
		return "[]" + value, nil
	}
	return "[" + key + "]" + value, nil
}

func canonicalSnapshot(t *testing.T, value any) []byte {
	t.Helper()
	snapshot, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
