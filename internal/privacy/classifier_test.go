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
