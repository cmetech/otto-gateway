package admin

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
)

const captureRedactionMarker = "[REDACTED]"

var (
	captureAuthorizationPrefixPattern = regexp.MustCompile(`(?i)\bauthorization\b\s*["']?\s*[:=]\s*`)
	captureCredentialPattern          = regexp.MustCompile(`(?i)\b(?:bearer|basic|api[ _-]?key)\s+[a-z0-9._~+/=-]+`)
	captureNamedAssignmentPattern     = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_-]*)(\s*["']?\s*[:=]\s*["']?)(\[REDACTED\]|[^\s&,;"'\}\]]+)`)
)

var captureSecretNameWords = map[string]struct{}{
	"apikey":        {},
	"authorization": {},
	"bearer":        {},
	"key":           {},
	"passphrase":    {},
	"password":      {},
	"secret":        {},
	"token":         {},
}

var captureCredentialValueSuffixWords = map[string]struct{}{
	"bytes":       {},
	"credential":  {},
	"credentials": {},
	"data":        {},
	"digest":      {},
	"hash":        {},
	"header":      {},
	"raw":         {},
	"string":      {},
	"value":       {},
}

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
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return redactCaptureString(raw)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
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
	if key != "" && isCaptureSecretName(key) {
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
	value = redactCaptureAuthorizationValues(value)
	value = captureCredentialPattern.ReplaceAllString(value, captureRedactionMarker)
	return captureNamedAssignmentPattern.ReplaceAllStringFunc(value, func(assignment string) string {
		parts := captureNamedAssignmentPattern.FindStringSubmatch(assignment)
		if len(parts) != 4 || !isCaptureSecretName(parts[1]) {
			return assignment
		}
		return parts[1] + parts[2] + captureRedactionMarker
	})
}

func redactCaptureAuthorizationValues(value string) string {
	var redacted strings.Builder
	remaining := value
	for {
		match := captureAuthorizationPrefixPattern.FindStringIndex(remaining)
		if match == nil {
			redacted.WriteString(remaining)
			return redacted.String()
		}

		redacted.WriteString(remaining[:match[1]])
		credential := remaining[match[1]:]
		end := captureAuthorizationValueEnd(credential)
		if len(credential) > 0 && end > 1 &&
			(credential[0] == '"' || credential[0] == '\'') && credential[end-1] == credential[0] {
			redacted.WriteByte(credential[0])
			redacted.WriteString(captureRedactionMarker)
			redacted.WriteByte(credential[0])
		} else {
			redacted.WriteString(captureRedactionMarker)
		}
		remaining = credential[end:]
	}
}

func captureAuthorizationValueEnd(value string) int {
	if value == "" {
		return 0
	}
	if value[0] == '"' || value[0] == '\'' {
		quote := value[0]
		for i := 1; i < len(value); i++ {
			if value[i] == '\\' {
				i++
				continue
			}
			if value[i] == quote {
				return i + 1
			}
		}
	}

	lineEnd := strings.IndexAny(value, "\r\n")
	if lineEnd < 0 {
		lineEnd = len(value)
	}
	fields := strings.Fields(value[:lineEnd])
	if len(fields) >= 2 {
		switch strings.ToLower(fields[0]) {
		case "bearer", "basic", "apikey", "api-key", "api_key":
			credentialEnd := strings.Index(value, fields[1]) + len(fields[1])
			return credentialEnd
		}
	}
	return lineEnd
}

func isCaptureSecretName(name string) bool {
	words := captureNameWords(name)
	if len(words) == 0 {
		return false
	}
	for i, word := range words {
		if _, secret := captureSecretNameWords[strings.ToLower(word)]; !secret {
			continue
		}
		credentialValue := true
		for _, suffix := range words[i+1:] {
			if _, ok := captureCredentialValueSuffixWords[strings.ToLower(suffix)]; !ok {
				credentialValue = false
				break
			}
		}
		if credentialValue {
			return true
		}
	}
	return false
}

func captureNameWords(name string) []string {
	segments := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var words []string
	for _, segment := range segments {
		runes := []rune(segment)
		start := 0
		for i := 1; i < len(runes); i++ {
			lowerToUpper := unicode.IsLower(runes[i-1]) && unicode.IsUpper(runes[i])
			acronymToWord := unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i]) &&
				i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if lowerToUpper || acronymToWord {
				words = append(words, string(runes[start:i]))
				start = i
			}
		}
		if start < len(runes) {
			words = append(words, string(runes[start:]))
		}
	}
	return words
}
