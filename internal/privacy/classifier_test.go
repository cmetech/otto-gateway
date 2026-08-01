package privacy

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"
)

type secretCase struct {
	Name     string   `json:"name"`
	Key      string   `json:"key"`
	Value    string   `json:"value"`
	Entities []string `json:"entities"`
}

func TestSecretClassifierSharedCorpus(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/secret-cases.json")
	if err != nil {
		t.Fatal(err)
	}

	var cases []secretCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 35 {
		t.Fatalf("credential corpus has %d rows, want at least 35", len(cases))
	}

	classifier := NewSecretClassifier()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			findings := classifier.Classify(tc.Key, tc.Value)
			entities := make([]string, 0, len(findings))
			for _, finding := range findings {
				if finding.Start < 0 || finding.End <= finding.Start || finding.End > len(tc.Value) {
					t.Fatalf("invalid finding offsets: %+v", finding)
				}
				if finding.Kind != MatchHighConfidenceSecret {
					t.Fatalf("finding kind=%v, want MatchHighConfidenceSecret", finding.Kind)
				}
				if finding.Category != CategorySecret {
					t.Fatalf("finding category=%q, want %q", finding.Category, CategorySecret)
				}
				entities = append(entities, finding.Entity)
			}
			sort.Strings(entities)
			want := append([]string(nil), tc.Entities...)
			sort.Strings(want)
			if !slices.Equal(entities, want) {
				t.Fatalf("entities=%v, want %v", entities, want)
			}
		})
	}
}

func TestSecretClassifierNormalizesCredentialCompoundKeys(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	for _, key := range []string{
		"clientSecret",
		"client_secret",
		"client-secret",
		"client.secret",
		"ProxyAuthorization",
		"proxy_authorization",
	} {
		if !classifier.IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q)=false, want true", key)
		}
	}
}

func TestSecretClassifierRedactsManagedCredentialAssignments(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "hash key field", key: "PII_HASH_KEY", value: "hash-secret-7788", want: "[REDACTED]"},
		{name: "hash key dotenv", value: "PII_HASH_KEY=hash-secret-7788", want: "PII_HASH_KEY=[REDACTED]"},
		{name: "encrypt key yaml", value: "PII_ENCRYPT_KEY: encrypt-secret-8899", want: "PII_ENCRYPT_KEY: [REDACTED]"},
		{
			name:  "remote write token JSON",
			value: `{"GW_METRICS_REMOTE_WRITE_TOKEN":"metrics-secret-9900","keep":"yes"}`,
			want:  `{"GW_METRICS_REMOTE_WRITE_TOKEN":"[REDACTED]","keep":"yes"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifier.Redact(tc.key, tc.value); got != tc.want {
				t.Fatalf("Redact()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecretClassifierRedactsTask14PrivacyCredentials(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	tests := []struct {
		value string
		want  string
	}{
		{value: "PRIVACY_ALIAS_KEY=alias-secret-1122", want: "PRIVACY_ALIAS_KEY=[REDACTED]"},
		{value: "PRIVACY_TRIAGE_TOKEN=triage-secret-3344", want: "PRIVACY_TRIAGE_TOKEN=[REDACTED]"},
	}
	for _, tc := range tests {
		if got := classifier.Redact("", tc.value); got != tc.want {
			t.Errorf("Redact(%q)=%q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestSecretClassifierRejectsGenericKeyNames(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	for _, key := range []string{
		"key",
		"keyboard",
		"monkey",
		"key_count",
		"token_count",
		"secretary",
		"public_key",
		"checksum",
	} {
		if classifier.IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q)=true, want false", key)
		}
	}
}

func TestSecretClassifierRedactsOnlyMatchedSpans(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	got := classifier.Redact("", "prefix Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig suffix")
	want := "prefix [REDACTED] suffix"
	if got != want {
		t.Fatalf("Redact()=%q, want %q", got, want)
	}

	safe := "Press any key to continue."
	if got := classifier.Redact("", safe); got != safe {
		t.Fatalf("safe Redact()=%q, want unchanged input", got)
	}
}

func TestSecretClassifierRedactsBareBearerValue(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	const value = "prefix Bearer bearer-secret-7788 suffix"
	findings := classifier.Classify("", value)
	if len(findings) != 1 {
		t.Fatalf("finding count=%d, want 1", len(findings))
	}
	if got, want := findings[0].Entity, "BEARER_TOKEN"; got != want {
		t.Fatalf("entity=%q, want %q", got, want)
	}
	if got, want := classifier.Redact("", value), "prefix Bearer [REDACTED] suffix"; got != want {
		t.Fatalf("Redact()=%q, want %q", got, want)
	}
}

func TestSecretClassifierPreservesRedactedAssignments(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "direct field", key: "PII_HASH_KEY", value: "[REDACTED]"},
		{name: "dotenv", value: "AUTH_TOKEN=[REDACTED]"},
		{name: "single quoted", value: "PII_ENCRYPT_KEY='[REDACTED]'"},
		{name: "JSON", value: `{"GW_METRICS_REMOTE_WRITE_TOKEN":"[REDACTED]"}`},
		{name: "authorization header", value: "Authorization: [REDACTED]"},
		{name: "bearer authorization header", value: "Authorization: Bearer [REDACTED]"},
		{name: "bare bearer", value: "Bearer [REDACTED]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if findings := classifier.Classify(tc.key, tc.value); len(findings) != 0 {
				t.Fatalf("Classify()=%+v, want no finding for an existing redaction", findings)
			}
			if got := classifier.Redact(tc.key, tc.value); got != tc.value {
				t.Fatalf("Redact()=%q, want unchanged %q", got, tc.value)
			}
		})
	}
}

func TestSecretClassifierPreservesOneWayCredentialAssignmentMarkers(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	tests := []struct {
		name  string
		value string
	}{
		{name: "JSON password", value: `{"password":"[SECRET:PASSWORD_0123456789AB]"}`},
		{name: "YAML client secret", value: "client_secret: [SECRET:CLIENT_SECRET_0123456789AB]"},
		{name: "dotenv refresh token", value: "REFRESH_TOKEN=[SECRET:REFRESH_TOKEN_0123456789AB]"},
		{name: "CLI access token", value: "--access-token=[SECRET:ACCESS_TOKEN_0123456789AB]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if findings := classifier.Classify("", tc.value); len(findings) != 0 {
				t.Fatalf("Classify()=%+v, want no finding for a one-way credential marker", findings)
			}
			if got := classifier.Redact("", tc.value); got != tc.value {
				t.Fatalf("Redact()=%q, want unchanged %q", got, tc.value)
			}
		})
	}
}

func TestSecretClassifierCredentialKeysPreserveOnlyExactOneWayMarkers(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	tests := []struct {
		name, key, value string
		wantFindings     int
		wantRedacted     string
	}{
		{
			name: "API key exact marker", key: "api_key", value: "[SECRET:API_KEY_0123456789AB]",
			wantRedacted: "[SECRET:API_KEY_0123456789AB]",
		},
		{
			name: "password exact marker", key: "password", value: "[SECRET:PASSWORD_0123456789AB]",
			wantRedacted: "[SECRET:PASSWORD_0123456789AB]",
		},
		{
			name: "malformed marker", key: "api_key", value: "[SECRET:API_KEY_NOT-HEX]",
			wantFindings: 1, wantRedacted: redactedSecret,
		},
		{
			name: "marker with suffix", key: "password", value: "[SECRET:PASSWORD_0123456789AB] trailing",
			wantFindings: 1, wantRedacted: redactedSecret,
		},
		{
			name: "raw credential", key: "api_key", value: "raw-credential-value",
			wantFindings: 1, wantRedacted: redactedSecret,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifier.Classify(tc.key, tc.value)
			if len(findings) != tc.wantFindings {
				t.Fatalf("Classify(%q, %q)=%+v, want %d findings", tc.key, tc.value, findings, tc.wantFindings)
			}
			if got := classifier.Redact(tc.key, tc.value); got != tc.wantRedacted {
				t.Fatalf("Redact(%q, %q)=%q, want %q", tc.key, tc.value, got, tc.wantRedacted)
			}
		})
	}
}

func TestSecretClassifierFindingDoesNotRetainSourceValue(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(Finding{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == "Value" || field.Name == "Source" || field.Name == "Text" {
			t.Fatalf("Finding retains source in field %q", field.Name)
		}
	}
}

func TestSecretClassifierQuotedAssignmentsUseExactValueSpans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		wantStart int
		wantEnd   int
		want      string
	}{
		{
			name:      "JSON double quoted password",
			value:     `{"password":"correct horse battery staple","keep":"yes"}`,
			wantStart: 13,
			wantEnd:   41,
			want:      `{"password":"[REDACTED]","keep":"yes"}`,
		},
		{
			name:      "dotenv single quoted password",
			value:     `PASSWORD='correct horse battery staple'`,
			wantStart: 10,
			wantEnd:   38,
			want:      `PASSWORD='[REDACTED]'`,
		},
		{
			name:      "YAML single quoted passphrase",
			value:     `private_key_passphrase: 'orange lantern signal'`,
			wantStart: 25,
			wantEnd:   46,
			want:      `private_key_passphrase: '[REDACTED]'`,
		},
	}

	classifier := NewSecretClassifier()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifier.Classify("", tc.value)
			if len(findings) != 1 {
				t.Fatalf("finding count=%d, want 1", len(findings))
			}
			if findings[0].Start != tc.wantStart || findings[0].End != tc.wantEnd {
				t.Fatalf("span=(%d,%d), want (%d,%d)", findings[0].Start, findings[0].End, tc.wantStart, tc.wantEnd)
			}
			if got := classifier.Redact("", tc.value); got != tc.want {
				t.Fatalf("Redact()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecretClassifierCredentialURLsUseExactSpans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		wantStart int
		wantEnd   int
		want      string
	}{
		{
			name:      "compact JSON adjacent field",
			value:     `{"url":"postgres://u:p@host/db","action":"keep"}`,
			wantStart: 8,
			wantEnd:   30,
			want:      `{"url":"[REDACTED]","action":"keep"}`,
		},
		{
			name:      "quoted dotenv IPv4 and percent encoding",
			value:     `DATABASE_URL="postgres://user:p%40ss@10.20.30.40:5432/app%2Fone"`,
			wantStart: 14,
			wantEnd:   63,
			want:      `DATABASE_URL="[REDACTED]"`,
		},
		{
			name:      "quoted YAML service URL",
			value:     `database_url: 'https://deploy:p%40ss@example.internal/api%2Fv1'`,
			wantStart: 15,
			wantEnd:   62,
			want:      `database_url: '[REDACTED]'`,
		},
		{
			name:      "trailing prose punctuation",
			value:     `Connect using postgres://u:p@192.168.1.10:5432/db, then retry.`,
			wantStart: 14,
			wantEnd:   49,
			want:      `Connect using [REDACTED], then retry.`,
		},
	}

	classifier := NewSecretClassifier()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifier.Classify("", tc.value)
			if len(findings) != 1 {
				t.Fatalf("finding count=%d, want 1", len(findings))
			}
			finding := findings[0]
			if finding.Entity != "CREDENTIAL_URL" {
				t.Fatalf("entity=%q, want CREDENTIAL_URL", finding.Entity)
			}
			if finding.Start != tc.wantStart || finding.End != tc.wantEnd {
				t.Fatalf("span=(%d,%d), want (%d,%d)", finding.Start, finding.End, tc.wantStart, tc.wantEnd)
			}
			if got := classifier.Redact("", tc.value); got != tc.want {
				t.Fatalf("Redact()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecretClassifierLeavesNonCredentialedURLsUnchanged(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	for _, value := range []string{
		`{"url":"https://example.com/api","action":"keep"}`,
		`See https://192.168.1.10/status, then retry.`,
		`DATABASE_URL="postgres://database.internal/app"`,
	} {
		if findings := classifier.Classify("", value); len(findings) != 0 {
			t.Errorf("Classify returned %+v for non-credentialed URL", findings)
		}
		if got := classifier.Redact("", value); got != value {
			t.Errorf("Redact changed non-credentialed URL")
		}
	}
}

func TestSecretClassifierAcronymKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key    string
		entity string
	}{
		{key: "APIKey", entity: "API_KEY"},
		{key: "AWSAccessKeyID", entity: "AWS_ACCESS_KEY_ID"},
		{key: "AWSSecretAccessKey", entity: "AWS_SECRET_ACCESS_KEY"},
		{key: "GitHubToken", entity: "GITHUB_TOKEN"},
		{key: "OAuthToken", entity: "OAUTH_TOKEN"},
	}

	classifier := NewSecretClassifier()
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if !classifier.IsSecretKey(tc.key) {
				t.Fatalf("IsSecretKey(%q)=false, want true", tc.key)
			}
			findings := classifier.Classify(tc.key, "opaque-credential-value")
			if len(findings) != 1 {
				t.Fatalf("finding count=%d, want 1", len(findings))
			}
			if findings[0].Entity != tc.entity || findings[0].Start != 0 || findings[0].End != 23 {
				t.Fatalf("finding=%+v, want entity %q and span (0,23)", findings[0], tc.entity)
			}
		})
	}
}

func TestSecretClassifierRejectsAcronymLookalikes(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	for _, key := range []string{
		"APIKeyboard",
		"AWSAccessKeyCount",
		"GitHubTokenCount",
		"OAuthTokenCount",
	} {
		if classifier.IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q)=true, want false", key)
		}
	}
}

func TestSecretClassifierStructuredCredentialURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		wantStart int
		wantEnd   int
		want      string
	}{
		{
			name:      "bracketed IPv6 port and path",
			value:     `Connect postgres://u:p@[2001:db8::1]:5432/db now`,
			wantStart: 8,
			wantEnd:   44,
			want:      `Connect [REDACTED] now`,
		},
		{
			name:      "encoded IPv6 zone in compact JSON",
			value:     `{"url":"postgres://u:p@[fe80::1%25eth0]:5432/db"}`,
			wantStart: 8,
			wantEnd:   47,
			want:      `{"url":"[REDACTED]"}`,
		},
		{
			name:      "userinfo comma and sub-delimiter",
			value:     `Use postgres://u:p,ass;word@host/db, then retry.`,
			wantStart: 4,
			wantEnd:   35,
			want:      `Use [REDACTED], then retry.`,
		},
		{
			name:      "IPv6 compact JSON adjacent field",
			value:     `{"url":"postgres://u:p@[2001:db8::2]/db","next":"keep"}`,
			wantStart: 8,
			wantEnd:   39,
			want:      `{"url":"[REDACTED]","next":"keep"}`,
		},
		{
			name:      "IPv6 trailing prose punctuation",
			value:     `Try (postgres://u:p@[2001:db8::3]/db), then continue.`,
			wantStart: 5,
			wantEnd:   36,
			want:      `Try ([REDACTED]), then continue.`,
		},
	}

	classifier := NewSecretClassifier()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifier.Classify("", tc.value)
			if len(findings) != 1 {
				t.Fatalf("finding count=%d, want 1", len(findings))
			}
			finding := findings[0]
			if finding.Entity != "CREDENTIAL_URL" {
				t.Fatalf("entity=%q, want CREDENTIAL_URL", finding.Entity)
			}
			if finding.Start != tc.wantStart || finding.End != tc.wantEnd {
				t.Fatalf("span=(%d,%d), want (%d,%d)", finding.Start, finding.End, tc.wantStart, tc.wantEnd)
			}
			if got := classifier.Redact("", tc.value); got != tc.want {
				t.Fatalf("Redact()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecretClassifierLeavesNonCredentialedIPv6URLsUnchanged(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	for _, value := range []string{
		`{"url":"postgres://[2001:db8::1]:5432/db","next":"keep"}`,
		`See https://[fe80::1%25eth0]/status, then retry.`,
	} {
		if findings := classifier.Classify("", value); len(findings) != 0 {
			t.Errorf("Classify returned %+v for non-credentialed IPv6 URL", findings)
		}
		if got := classifier.Redact("", value); got != value {
			t.Errorf("Redact changed non-credentialed IPv6 URL")
		}
	}
}

func TestSecretClassifierUnquotedCredentialURLBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		wantStart int
		wantEnd   int
		want      string
	}{
		{
			name:      "array followed by safe URL",
			value:     `[postgres://u:p@host/db,https://safe.example/status]`,
			wantStart: 1,
			wantEnd:   23,
			want:      `[[REDACTED],https://safe.example/status]`,
		},
		{
			name:      "array followed by safe value",
			value:     `[postgres://u:p@host/db,keep]`,
			wantStart: 1,
			wantEnd:   23,
			want:      `[[REDACTED],keep]`,
		},
		{
			name:      "object-like adjacent field",
			value:     `{url:postgres://u:p@host/db,next:keep}`,
			wantStart: 5,
			wantEnd:   27,
			want:      `{url:[REDACTED],next:keep}`,
		},
	}

	classifier := NewSecretClassifier()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifier.Classify("", tc.value)
			if len(findings) != 1 {
				t.Fatalf("finding count=%d, want 1", len(findings))
			}
			finding := findings[0]
			if finding.Entity != "CREDENTIAL_URL" {
				t.Fatalf("entity=%q, want CREDENTIAL_URL", finding.Entity)
			}
			if finding.Start != tc.wantStart || finding.End != tc.wantEnd {
				t.Fatalf("span=(%d,%d), want (%d,%d)", finding.Start, finding.End, tc.wantStart, tc.wantEnd)
			}
			if got := classifier.Redact("", tc.value); got != tc.want {
				t.Fatalf("Redact()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecretClassifierMalformedUnquotedIPv6DoesNotCaptureAdjacentData(t *testing.T) {
	t.Parallel()

	classifier := NewSecretClassifier()
	for _, value := range []string{
		`[postgres://u:p@[2001:db8::1:5432/db,keep]`,
		`{url:postgres://u:p@[fe80::1%25eth0:5432/db,next:keep}`,
	} {
		if findings := classifier.Classify("", value); len(findings) != 0 {
			t.Errorf("Classify returned %+v for malformed bracketed IPv6 URL", findings)
		}
		if got := classifier.Redact("", value); got != value {
			t.Errorf("Redact changed malformed bracketed IPv6 input")
		}
	}
}
