package admin

import (
	"encoding/json"
	"regexp"
)

const captureRedactionMarker = "[REDACTED]"

var (
	// Match credential-bearing field names without treating safe words such as
	// "keyboard" as credentials; the final alternative covers camelCase keys.
	captureSecretKeyPattern     = regexp.MustCompile(`(?i:authorization|api[ _-]?key|token|secret|password|passphrase|bearer)|(?i:(?:^|[^a-z0-9])key(?:$|[^a-z0-9]))|Key(?:$|[A-Z])`)
	captureAuthorizationPattern = regexp.MustCompile(`(?i)(\bauthorization\b\s*["']?\s*[:=]\s*["']?)(?:(?:bearer|basic)\s+)?[^\s,;"'\}\]]+`)
	captureCredentialPattern    = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`)
	captureNamedValuePattern    = regexp.MustCompile(`(?i)((?:\bx[-_ ]?api[-_ ]?key\b|\bapi[-_ ]?key\b|\btoken\b|\bkey\b|\bsecret\b|\bpassword\b|\bpassphrase\b)\s*["']?\s*[:=]\s*["']?)[^\s,;"'\}\]]+`)
)

func redactCaptureFrames(in []CaptureFrame) []CaptureFrame {
	out := make([]CaptureFrame, len(in))
	copy(out, in)
	for i := range out {
		out[i].Params = redactCapturedParams(out[i].Params)
	}
	return out
}

func redactCapturedParams(raw string) string {
	// Decode before walking so redaction never runs across the serialized JSON
	// syntax and cannot damage its quoting or structural delimiters.
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return redactCaptureString(raw)
	}
	value = redactCaptureValue("", value)
	body, err := json.Marshal(value)
	if err != nil {
		return "[REDACTED: invalid captured JSON]"
	}
	return string(body)
}

func redactCaptureValue(key string, value any) any {
	if key != "" && captureSecretKeyPattern.MatchString(key) {
		return captureRedactionMarker
	}

	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for childKey, childValue := range value {
			redacted[childKey] = redactCaptureValue(childKey, childValue)
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for i, childValue := range value {
			redacted[i] = redactCaptureValue("", childValue)
		}
		return redacted
	case string:
		return redactCaptureString(value)
	default:
		return value
	}
}

func redactCaptureString(value string) string {
	value = captureAuthorizationPattern.ReplaceAllString(value, `${1}`+captureRedactionMarker)
	value = captureCredentialPattern.ReplaceAllString(value, captureRedactionMarker)
	return captureNamedValuePattern.ReplaceAllString(value, `${1}`+captureRedactionMarker)
}
