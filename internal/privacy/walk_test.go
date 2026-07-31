package privacy

import (
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
			"=plain",
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
	if nested[0] != "[]nested-secret" {
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
	original := cloneRequestForTest(req)
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
	if !reflect.DeepEqual(req, original) {
		t.Fatalf("VisitRequestStrings mutated request: got %#v, want %#v", req, original)
	}
}

func TestVisitResponseStringsIsReadOnly(t *testing.T) {
	t.Parallel()

	resp := canonical.ChatResponse{Message: canonical.Message{
		Content:   []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "response"}},
		ToolCalls: []canonical.ToolCall{{Arguments: map[string]any{"token": "value"}}},
	}}
	original := resp
	original.Message.Content = append([]canonical.ContentPart(nil), resp.Message.Content...)
	original.Message.ToolCalls = append([]canonical.ToolCall(nil), resp.Message.ToolCalls...)

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
	if !reflect.DeepEqual(resp, original) {
		t.Fatalf("VisitResponseStrings mutated response: got %#v, want %#v", resp, original)
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

func cloneRequestForTest(req canonical.ChatRequest) canonical.ChatRequest {
	copyReq := req
	copyReq.Messages = append([]canonical.Message(nil), req.Messages...)
	for i := range copyReq.Messages {
		copyReq.Messages[i].Content = append([]canonical.ContentPart(nil), req.Messages[i].Content...)
		copyReq.Messages[i].ToolCalls = append([]canonical.ToolCall(nil), req.Messages[i].ToolCalls...)
	}
	return copyReq
}
