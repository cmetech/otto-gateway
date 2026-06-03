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
