package admin

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
)

const (
	captureRedactionMarker     = "[REDACTED]"
	captureRedactionMaxDepth   = 8
	captureRedactionMaxJSONLen = 256 * 1024
)

var (
	captureAuthorizationPrefixPattern = regexp.MustCompile(`(?i)\bauthorization\b\s*["']?\s*[:=]\s*`)
	captureCredentialPattern          = regexp.MustCompile(`(?i)\b(?:bearer|basic|api[ _-]?key)\s+[a-z0-9._~+/=-]+`)
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
	"bytes":        {},
	"credential":   {},
	"credentials":  {},
	"confirm":      {},
	"confirmation": {},
	"data":         {},
	"digest":       {},
	"hash":         {},
	"header":       {},
	"raw":          {},
	"string":       {},
	"value":        {},
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
	value, ok := decodeCaptureJSON(raw)
	if !ok {
		// A ring snapshot can start or end in the middle of a JSON string that
		// itself contains encoded JSON. In that state the key delimiters are
		// still written as \" even though the outer document no longer decodes.
		// The ordinary malformed-text scanner intentionally does not interpret
		// arbitrary backslashes as syntax, so fail the whole fragment closed
		// when an escaped credential key/value boundary is still recognizable.
		if hasEscapedCaptureSecretAssignment(raw) {
			return captureRedactionMarker
		}
		return redactCaptureString(raw)
	}
	value = redactCaptureValue("", value, 0)
	body, err := json.Marshal(value)
	if err != nil {
		return "[REDACTED: invalid captured JSON]"
	}
	return string(body)
}

func hasEscapedCaptureSecretAssignment(value string) bool {
	const escapedQuote = `\"`
	for searchFrom := 0; searchFrom < len(value); {
		keyStartOffset := strings.Index(value[searchFrom:], escapedQuote)
		if keyStartOffset < 0 {
			return false
		}
		keyStart := searchFrom + keyStartOffset + len(escapedQuote)
		keyEndOffset := strings.Index(value[keyStart:], escapedQuote)
		if keyEndOffset < 0 {
			return false
		}
		keyEnd := keyStart + keyEndOffset
		separator := keyEnd + len(escapedQuote)
		for separator < len(value) && isCaptureSpace(value[separator]) {
			separator++
		}
		if isCaptureSecretName(value[keyStart:keyEnd]) && separator < len(value) &&
			(value[separator] == ':' || value[separator] == '=') {
			return true
		}
		searchFrom = keyEnd + len(escapedQuote)
	}
	return false
}

func decodeCaptureJSON(raw string) (any, bool) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

func redactCaptureValue(key string, value any, depth int) any {
	if key != "" && isCaptureSecretName(key) {
		return captureRedactionMarker
	}
	if depth >= captureRedactionMaxDepth {
		switch value.(type) {
		case map[string]any, []any, string:
			return captureRedactionMarker
		default:
			return value
		}
	}

	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for childKey, childValue := range value {
			redacted[childKey] = redactCaptureValue(childKey, childValue, depth+1)
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for i, childValue := range value {
			redacted[i] = redactCaptureValue("", childValue, depth+1)
		}
		return redacted
	case string:
		return redactCaptureStringValue(value, depth+1)
	default:
		return value
	}
}

func redactCaptureStringValue(value string, depth int) string {
	if len(value) > captureRedactionMaxJSONLen {
		if looksLikeCaptureJSON(value) {
			return captureRedactionMarker
		}
		return redactCaptureString(value)
	}
	nested, ok := decodeCaptureJSON(value)
	if !ok {
		return redactCaptureString(value)
	}
	if depth >= captureRedactionMaxDepth {
		return captureRedactionMarker
	}
	nested = redactCaptureValue("", nested, depth)
	body, err := json.Marshal(nested)
	if err != nil {
		return captureRedactionMarker
	}
	return string(body)
}

func looksLikeCaptureJSON(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '{', '[', '"':
		return true
	default:
		return false
	}
}

func redactCaptureString(value string) string {
	value = redactCaptureAuthorizationValues(value)
	value = captureCredentialPattern.ReplaceAllString(value, captureRedactionMarker)
	return redactCaptureNamedAssignments(value)
}

func redactCaptureNamedAssignments(value string) string {
	var redacted strings.Builder
	written := 0
	for index := 0; index < len(value); {
		if !isCaptureNameStart(value[index]) || (index > 0 && isCaptureNamePart(value[index-1])) {
			index++
			continue
		}

		nameStart := index
		index++
		for index < len(value) && isCaptureNamePart(value[index]) {
			index++
		}
		nameEnd := index
		if !isCaptureSecretName(value[nameStart:nameEnd]) {
			continue
		}

		separator := index
		for separator < len(value) && isCaptureSpace(value[separator]) {
			separator++
		}
		if separator < len(value) && (value[separator] == '"' || value[separator] == '\'') {
			separator++
			for separator < len(value) && isCaptureSpace(value[separator]) {
				separator++
			}
		}
		if separator >= len(value) || (value[separator] != ':' && value[separator] != '=') {
			continue
		}
		separator++
		for separator < len(value) && isCaptureSpace(value[separator]) {
			separator++
		}

		valueEnd, replacement := captureAssignmentValue(value, separator)
		redacted.WriteString(value[written:separator])
		redacted.WriteString(replacement)
		written = valueEnd
		index = valueEnd
	}
	redacted.WriteString(value[written:])
	return redacted.String()
}

func captureAssignmentValue(value string, start int) (int, string) {
	if start >= len(value) {
		return start, captureRedactionMarker
	}
	switch value[start] {
	case '"', '\'':
		quote := value[start]
		for index := start + 1; index < len(value); index++ {
			if value[index] == '\\' {
				index++
				continue
			}
			if value[index] == quote {
				return index + 1, string(quote) + captureRedactionMarker + string(quote)
			}
		}
		return captureLineEnd(value, start), string(quote) + captureRedactionMarker
	case '{', '[':
		return captureStructuredAssignmentValue(value, start)
	default:
		end := start
		for end < len(value) && !isCaptureAssignmentDelimiter(value[end]) {
			end++
		}
		return end, captureRedactionMarker
	}
}

func captureStructuredAssignmentValue(value string, start int) (int, string) {
	stack := []byte{value[start]}
	var quote byte
	for index := start + 1; index < len(value); index++ {
		char := value[index]
		if quote != 0 {
			if char == '\\' {
				index++
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '"', '\'':
			quote = char
		case '{', '[':
			stack = append(stack, char)
		case '}', ']':
			want := byte('{')
			if char == ']' {
				want = '['
			}
			if stack[len(stack)-1] != want {
				return captureLineEnd(value, start), captureRedactionMarker
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return index + 1, captureRedactionMarker
			}
		}
	}
	return captureLineEnd(value, start), captureRedactionMarker
}

func captureLineEnd(value string, start int) int {
	if offset := strings.IndexAny(value[start:], "\r\n"); offset >= 0 {
		return start + offset
	}
	return len(value)
}

func isCaptureNameStart(char byte) bool {
	return char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

func isCaptureNamePart(char byte) bool {
	return isCaptureNameStart(char) || char >= '0' && char <= '9' || char == '_' || char == '-'
}

func isCaptureSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n'
}

func isCaptureAssignmentDelimiter(char byte) bool {
	return isCaptureSpace(char) || char == '&' || char == ',' || char == ';' || char == '}' || char == ']'
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
