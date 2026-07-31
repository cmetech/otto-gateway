package privacy

import (
	"sort"
	"strings"
	"unicode"
)

const redactedSecret = "[REDACTED]"

// SecretClassifier detects only high-confidence credentials.
type SecretClassifier struct{}

// NewSecretClassifier constructs the shared stateless credential classifier.
func NewSecretClassifier() *SecretClassifier {
	return &SecretClassifier{}
}

// Classify returns high-confidence credential spans in value.
func (c *SecretClassifier) Classify(key, value string) []Finding {
	if value == "" {
		return nil
	}

	if c.IsSecretKey(key) {
		return []Finding{secretFinding(entityForKey(key), 0, len(value), 0)}
	}

	return detectSecretValues(value)
}

// IsSecretKey reports whether key unambiguously names a credential field.
func (c *SecretClassifier) IsSecretKey(key string) bool {
	words := normalizeKeyWords(key)
	return containsCredentialCompound(words) || containsAuthorizationName(words)
}

// Redact replaces detected credential spans without retaining the source.
func (c *SecretClassifier) Redact(key, value string) string {
	findings := c.Classify(key, value)
	if len(findings) == 0 {
		return value
	}

	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].Start < findings[j].Start
	})

	var out strings.Builder
	start := 0
	for _, finding := range findings {
		if finding.Start < start || finding.End > len(value) {
			continue
		}
		out.WriteString(value[start:finding.Start])
		out.WriteString(redactedSecret)
		start = finding.End
	}
	out.WriteString(value[start:])
	return out.String()
}

func normalizeKeyWords(key string) []string {
	var normalized strings.Builder
	runes := []rune(strings.TrimSpace(key))
	for i, r := range runes {
		if r == '_' || r == '-' || r == '.' || unicode.IsSpace(r) {
			normalized.WriteByte(' ')
			continue
		}
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])) {
			normalized.WriteByte(' ')
		}
		normalized.WriteRune(unicode.ToLower(r))
	}
	return strings.Fields(normalized.String())
}

func containsCredentialCompound(words []string) bool {
	if len(words) == 0 {
		return false
	}
	if hasWord(words, "public") && hasWord(words, "key") {
		return false
	}
	if hasWord(words, "password") || hasWord(words, "passphrase") {
		return true
	}
	if hasWord(words, "secret") {
		return true
	}
	if hasAdjacent(words, "api", "key") || hasAdjacent(words, "access", "key") ||
		hasAdjacent(words, "private", "key") || hasAdjacent(words, "signing", "key") {
		return true
	}
	return hasAdjacent(words, "access", "token") || hasAdjacent(words, "refresh", "token") ||
		hasAdjacent(words, "oauth", "token") || hasAdjacent(words, "auth", "token") ||
		hasAdjacent(words, "github", "token") || hasAdjacent(words, "gitlab", "token")
}

func containsAuthorizationName(words []string) bool {
	return len(words) == 1 && words[0] == "authorization" ||
		len(words) == 2 && words[0] == "proxy" && words[1] == "authorization"
}

func hasWord(words []string, want string) bool {
	for _, word := range words {
		if word == want {
			return true
		}
	}
	return false
}

func hasAdjacent(words []string, first, second string) bool {
	for i := 0; i+1 < len(words); i++ {
		if words[i] == first && words[i+1] == second {
			return true
		}
	}
	return false
}

func entityForKey(key string) string {
	words := normalizeKeyWords(key)
	switch {
	case len(words) == 2 && words[0] == "proxy" && words[1] == "authorization":
		return "PROXY_AUTHORIZATION"
	case containsAuthorizationName(words):
		return "AUTHORIZATION"
	case hasWord(words, "passphrase"):
		return "PASSPHRASE"
	case hasWord(words, "password"):
		return "PASSWORD"
	case hasAdjacent(words, "private", "key"):
		return "PRIVATE_KEY"
	case hasWord(words, "aws") && hasAdjacent(words, "secret", "access"):
		return "AWS_SECRET_ACCESS_KEY"
	case hasWord(words, "aws") && hasAdjacent(words, "access", "key"):
		return "AWS_ACCESS_KEY_ID"
	case hasAdjacent(words, "client", "secret"):
		return "CLIENT_SECRET"
	case hasAdjacent(words, "refresh", "token"):
		return "REFRESH_TOKEN"
	case hasAdjacent(words, "oauth", "token"):
		return "OAUTH_TOKEN"
	case hasAdjacent(words, "access", "token"):
		return "ACCESS_TOKEN"
	case hasWord(words, "github") && hasWord(words, "token"):
		return "GITHUB_TOKEN"
	case hasWord(words, "gitlab") && hasWord(words, "token"):
		return "GITLAB_TOKEN"
	case hasAdjacent(words, "api", "key"):
		return "API_KEY"
	default:
		return "SECRET"
	}
}
