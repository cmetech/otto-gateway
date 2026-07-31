package privacy

import (
	"errors"
	"sort"

	"otto-gateway/internal/canonical"
)

const maxStringTraversalDepth = 64

var errStringTraversalTooDeep = errors.New("privacy string traversal exceeds maximum depth")

// TransformStrings returns a copy of supported containers with every string
// value transformed. Map keys are context only and are never changed.
func TransformStrings(v any, fn func(key, value string) (string, error)) (any, error) {
	return transformStrings(v, "", 0, fn)
}

func transformStrings(v any, key string, depth int, fn func(key, value string) (string, error)) (any, error) {
	if depth > maxStringTraversalDepth {
		return nil, errStringTraversalTooDeep
	}

	switch value := v.(type) {
	case string:
		transformed, err := fn(key, value)
		if err != nil {
			return nil, err
		}
		return transformed, nil
	case map[string]any:
		keys := sortedMapKeys(value)
		transformed := make(map[string]any, len(value))
		for _, childKey := range keys {
			child, err := transformStrings(value[childKey], childKey, depth+1, fn)
			if err != nil {
				return nil, err
			}
			transformed[childKey] = child
		}
		return transformed, nil
	case []any:
		transformed := make([]any, len(value))
		for i, childValue := range value {
			child, err := transformStrings(childValue, "", depth+1, fn)
			if err != nil {
				return nil, err
			}
			transformed[i] = child
		}
		return transformed, nil
	default:
		return v, nil
	}
}

// TransformRequestStrings transforms the explicitly approved request content
// surfaces in place.
func TransformRequestStrings(req *canonical.ChatRequest, fn func(key, value string) (string, error)) error {
	if req == nil {
		return nil
	}

	system, err := fn("system", req.System)
	if err != nil {
		return err
	}
	req.System = system

	for i := range req.Messages {
		if err := transformMessageStrings(&req.Messages[i], fn); err != nil {
			return err
		}
	}
	return nil
}

// TransformResponseStrings transforms the explicitly approved response
// content surfaces in place.
func TransformResponseStrings(resp *canonical.ChatResponse, fn func(key, value string) (string, error)) error {
	if resp == nil {
		return nil
	}
	return transformMessageStrings(&resp.Message, fn)
}

func transformMessageStrings(message *canonical.Message, fn func(key, value string) (string, error)) error {
	for i := range message.Content {
		part := &message.Content[i]
		switch part.Kind {
		case canonical.ContentKindText:
			text, err := fn("text", part.Text)
			if err != nil {
				return err
			}
			part.Text = text
		case canonical.ContentKindToolUse:
			if part.ToolUse == nil {
				continue
			}
			input, err := TransformStrings(part.ToolUse.Input, fn)
			if err != nil {
				return err
			}
			part.ToolUse.Input = input.(map[string]any)
		case canonical.ContentKindToolResult:
			if part.ToolResult == nil {
				continue
			}
			content, err := fn("content", part.ToolResult.Content)
			if err != nil {
				return err
			}
			part.ToolResult.Content = content
		}
	}

	for i := range message.ToolCalls {
		arguments, err := TransformStrings(message.ToolCalls[i].Arguments, fn)
		if err != nil {
			return err
		}
		message.ToolCalls[i].Arguments = arguments.(map[string]any)
	}
	return nil
}

// VisitRequestStrings visits the approved request content surfaces without
// mutating the request.
func VisitRequestStrings(req *canonical.ChatRequest, fn func(key, value string) error) error {
	if req == nil {
		return nil
	}
	if err := fn("system", req.System); err != nil {
		return err
	}
	for i := range req.Messages {
		if err := visitMessageStrings(&req.Messages[i], fn); err != nil {
			return err
		}
	}
	return nil
}

// VisitResponseStrings visits the approved response content surfaces without
// mutating the response.
func VisitResponseStrings(resp *canonical.ChatResponse, fn func(key, value string) error) error {
	if resp == nil {
		return nil
	}
	return visitMessageStrings(&resp.Message, fn)
}

func visitMessageStrings(message *canonical.Message, fn func(key, value string) error) error {
	for i := range message.Content {
		part := &message.Content[i]
		switch part.Kind {
		case canonical.ContentKindText:
			if err := fn("text", part.Text); err != nil {
				return err
			}
		case canonical.ContentKindToolUse:
			if part.ToolUse != nil {
				if err := visitStrings(part.ToolUse.Input, "", 0, fn); err != nil {
					return err
				}
			}
		case canonical.ContentKindToolResult:
			if part.ToolResult != nil {
				if err := fn("content", part.ToolResult.Content); err != nil {
					return err
				}
			}
		}
	}

	for i := range message.ToolCalls {
		if err := visitStrings(message.ToolCalls[i].Arguments, "", 0, fn); err != nil {
			return err
		}
	}
	return nil
}

func visitStrings(v any, key string, depth int, fn func(key, value string) error) error {
	if depth > maxStringTraversalDepth {
		return errStringTraversalTooDeep
	}

	switch value := v.(type) {
	case string:
		return fn(key, value)
	case map[string]any:
		for _, childKey := range sortedMapKeys(value) {
			if err := visitStrings(value[childKey], childKey, depth+1, fn); err != nil {
				return err
			}
		}
	case []any:
		for _, childValue := range value {
			if err := visitStrings(childValue, "", depth+1, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedMapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
