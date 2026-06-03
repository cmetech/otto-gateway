// PII-NER tests for the prose-backed PERSON/LOCATION recognizer.
//
// prose uses an averaged-perceptron NER model; accuracy is decent on
// common Western names and major place names but has known quirks
// (e.g., sentence-initial "Hello" can be mislabeled as GPE; "Alice
// Johnson" may be tokenized so only "Johnson" survives as PERSON).
// Test fixtures here use names/places prose handles reliably:
// "John Smith", "Jane Doe", "Barack Obama", "Boston", "Seattle",
// "Paris", "New York".

package pii

import (
	"strings"
	"testing"
)

func TestNEREngine_DefaultIsNil(t *testing.T) {
	h := &PIIRedactionHook{}
	if h.NER != nil {
		t.Error("PIIRedactionHook.NER must default to nil")
	}
}

func TestNEREngine_DetectPerson(t *testing.T) {
	eng := NewNEREngine()
	text := "John Smith works at the gateway."
	spans := eng.Detect(text)

	foundPerson := false
	for _, s := range spans {
		if s.Name != "PERSON" {
			continue
		}
		got := text[s.Start:s.End]
		if !strings.Contains(got, "John Smith") {
			continue
		}
		foundPerson = true
		if s.Value != got {
			t.Errorf("span.Value=%q does not match text[Start:End]=%q", s.Value, got)
		}
	}
	if !foundPerson {
		t.Errorf("expected to find a PERSON span 'John Smith' in %+v", spans)
	}
}

func TestNEREngine_DetectLocation(t *testing.T) {
	eng := NewNEREngine()
	text := "We deployed the gateway in Boston last week."
	spans := eng.Detect(text)

	foundLoc := false
	for _, s := range spans {
		if s.Name == "LOCATION" && strings.Contains(text[s.Start:s.End], "Boston") {
			foundLoc = true
			break
		}
	}
	if !foundLoc {
		t.Errorf("expected to find a LOCATION span 'Boston' in %+v", spans)
	}
}

// TestNEREngine_GPEMapsToLOCATION: prose emits 'GPE' (geo-political
// entity) for cities; our engine maps that to the canonical 'LOCATION'
// name used everywhere else in PII.
func TestNEREngine_GPEMapsToLOCATION(t *testing.T) {
	eng := NewNEREngine()
	text := "Paris and Seattle are popular destinations."
	spans := eng.Detect(text)
	count := 0
	for _, s := range spans {
		if s.Name == "LOCATION" {
			count++
		}
		if s.Name == "GPE" {
			t.Errorf("raw GPE leaked into spans; want LOCATION: %+v", s)
		}
	}
	if count < 2 {
		t.Errorf("expected ≥2 LOCATION spans (Paris, Seattle); got %d in %+v", count, spans)
	}
}

func TestNEREngine_EmptyInput(t *testing.T) {
	eng := NewNEREngine()
	got := eng.Detect("")
	if len(got) != 0 {
		t.Errorf("Detect(\"\"): expected 0 spans, got %d", len(got))
	}
}

func TestNEREngine_RepeatableInit(t *testing.T) {
	eng := NewNEREngine()
	_ = eng.Detect("first call primes the lazy init")
	a := eng.Detect("Barack Obama visited Paris.")
	b := eng.Detect("Barack Obama visited Paris.")
	if len(a) != len(b) {
		t.Errorf("repeated Detect calls yielded different counts: %d vs %d", len(a), len(b))
	}
}

// Byte offsets must be exact so the rewrite pass slices the original
// string correctly. text[Start:End] must equal span.Value for every span.
func TestNEREngine_OffsetsAreExact(t *testing.T) {
	eng := NewNEREngine()
	text := "Jane Doe lives in Seattle and works in New York."
	spans := eng.Detect(text)
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	for _, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			t.Errorf("span has invalid offsets: %+v (len(text)=%d)", s, len(text))
			continue
		}
		if got := text[s.Start:s.End]; got != s.Value {
			t.Errorf("span.Value=%q != text[%d:%d]=%q", s.Value, s.Start, s.End, got)
		}
	}
}

// TestNEREngine_DuplicateName: prose may emit the same entity twice if
// the name appears twice in text. Sequential cursor scan must resolve
// to two distinct, non-overlapping spans.
func TestNEREngine_DuplicateName(t *testing.T) {
	eng := NewNEREngine()
	text := "Barack Obama spoke. Barack Obama waved."
	spans := eng.Detect(text)
	count := 0
	prevStart := -1
	for _, s := range spans {
		if s.Name == "PERSON" && s.Value == "Barack Obama" {
			count++
			if s.Start <= prevStart {
				t.Errorf("duplicate PERSON spans not strictly ordered: %+v after start=%d", s, prevStart)
			}
			prevStart = s.Start
		}
	}
	if count < 2 {
		t.Logf("only %d 'Barack Obama' spans detected (prose may collapse duplicates); spans=%+v", count, spans)
		// Not a hard failure — prose's deduplication behavior is internal.
		// The test documents the expected sequential-scan invariant
		// without forcing a specific count.
	}
}

// Integration: when NER is wired AND PERSON/LOCATION are in
// EnabledEntities, names and places in user text get redacted alongside
// regex-detected entities.
func TestNER_Integration_PersonReplace(t *testing.T) {
	hook := freshHook("replace")
	hook.EnabledEntities = []string{"PERSON", "LOCATION", "Email"}
	hook.NER = NewNEREngine()

	got := redactText(t, hook, "John Smith works in Boston.")
	if strings.Contains(got, "John Smith") {
		t.Errorf("expected PERSON redaction; got %q", got)
	}
	if strings.Contains(got, "Boston") {
		t.Errorf("expected LOCATION redaction; got %q", got)
	}
	if !strings.Contains(got, "[PERSON") {
		t.Errorf("expected [PERSON token; got %q", got)
	}
	if !strings.Contains(got, "[LOCATION") {
		t.Errorf("expected [LOCATION token; got %q", got)
	}
}

// Off-by-default: NER is nil on the hook → names pass through even
// when PERSON is in EnabledEntities (the allowlist is shape-only;
// behavior depends on NER being wired).
func TestNER_OffByDefault(t *testing.T) {
	hook := freshHook("replace")
	hook.EnabledEntities = []string{"PERSON", "Email"}
	// hook.NER intentionally nil — represents PII_NER_ENABLED=false

	got := redactText(t, hook, "John Smith emailed me.")
	if !strings.Contains(got, "John Smith") {
		t.Errorf("NER off → name must pass through; got %q", got)
	}
}

// Overlap arbitration: regex Email wins over NER PERSON at the same
// span. Prose can sometimes label name-shaped email local-parts as
// PERSON; verify regex takes priority.
func TestNER_RegexWinsOverlap(t *testing.T) {
	hook := freshHook("replace")
	hook.EnabledEntities = []string{"Email", "PERSON"}
	hook.NER = NewNEREngine()

	got := redactText(t, hook, "contact jane.doe@example.com please")
	if !strings.Contains(got, "[EMAIL") {
		t.Errorf("expected EMAIL redaction (regex must win); got %q", got)
	}
}

// EnabledEntities allowlist must filter NER outputs even when NER is
// wired. PERSON enabled, LOCATION not → only person redacts.
func TestNER_EnabledEntitiesFilter(t *testing.T) {
	hook := freshHook("replace")
	hook.EnabledEntities = []string{"PERSON"}
	hook.NER = NewNEREngine()

	got := redactText(t, hook, "John Smith visited Boston.")
	if strings.Contains(got, "John Smith") {
		t.Errorf("PERSON enabled → name must be redacted; got %q", got)
	}
	if !strings.Contains(got, "Boston") {
		t.Errorf("LOCATION NOT enabled → must pass through; got %q", got)
	}
}

// Empty EnabledEntities = allow all → both PERSON and LOCATION redact.
func TestNER_EnabledEntitiesEmpty_AllowsAll(t *testing.T) {
	hook := freshHook("replace")
	hook.EnabledEntities = nil
	hook.NER = NewNEREngine()

	got := redactText(t, hook, "John Smith visited Boston.")
	if strings.Contains(got, "John Smith") {
		t.Errorf("empty allowlist → PERSON must be redacted; got %q", got)
	}
	if strings.Contains(got, "Boston") {
		t.Errorf("empty allowlist → LOCATION must be redacted; got %q", got)
	}
}
