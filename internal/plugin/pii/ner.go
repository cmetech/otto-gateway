// PII-NER engine — wraps jdkato/prose/v2 under sync.Once so the prose
// tagger/tokenizer global state is loaded exactly once per process.
// When PII_NER_ENABLED is false at boot, NewNEREngine is never called
// and no prose state is allocated.
//
// Why prose:
//   - Pure Go: no CGo, no shared libs, single static binary preserved.
//   - Bundled model: the averaged-perceptron NER weights ship inside
//     the Go module — no model download, no first-run bootstrap, no
//     network at install time. Curl|sh install stays one command.
//   - English-only: known limitation, documented in the design doc
//     accuracy-ceiling section.
//
// Accuracy ceiling (v1):
//   - Decent on common Western names and major place names.
//   - Weaker on Asian / multilingual names, unusual locations.
//   - Roughly: spaCy small ≤ prose < spaCy large < BERT.
//   - v2 will add an opt-in transformer-backed engine (first-run ONNX
//     model download); explicitly out of scope here.
//
// Byte-offset reconstruction:
//
//	prose returns Entity.Text but not byte offsets. We reconstruct them
//	by scanning the original text for each entity's text in order,
//	advancing a cursor so duplicates resolve to distinct matches. This
//	is robust enough for the round-trip property (each detected name
//	maps to ONE span; duplicates handled by sequential scan).
//	Pathological cases (overlapping substrings, weird tokenizer
//	normalizations) fall back to skipping the entity — preferred over
//	emitting a wrong span.

package pii

import (
	"sort"
	"strings"
	"sync"

	"github.com/jdkato/prose/v2"

	"otto-gateway/internal/privacy"
)

// nerEntityNames lists the entity names Detect's label-mapping switch
// below can emit (PERSON for prose's "PERSON" label; LOCATION for its
// "GPE"/"LOC"/"LOCATION" labels). Defined once, adjacent to that switch,
// so TokenEntityNames (recognizers.go) — which must cover every name
// pii.ApplyMode can be called with, including these NER names — cannot
// silently drift from it. If the switch below ever emits a new name,
// add it here too.
var nerEntityNames = []string{"PERSON", "LOCATION"}

// nerEngine wraps prose under a sync.Once. The Document is NOT cached
// across calls — prose Documents are constructed per-text — but the
// sync.Once gates whatever one-time global state prose may lazy-init.
type nerEngine struct {
	once sync.Once
}

// NewNEREngine constructs an engine. The prose tagger/tokenizer global
// state is NOT touched until the first Detect call.
func NewNEREngine() *nerEngine { //nolint:revive // nerEngine deliberately package-private; consumed only via PIIRedactionHook.NER field, no callers outside internal/plugin/pii/
	return &nerEngine{}
}

// Detect returns PERSON and LOCATION spans in text, with byte offsets
// reconstructed by sequential scan. Returns nil for empty text.
func (n *nerEngine) Detect(text string) []span {
	if text == "" {
		return nil
	}
	// sync.Once gives a one-time warmup hook. Prose itself has no
	// explicit init function, so the body is empty for now. Kept so
	// any future warmup (e.g., a dummy doc parse to pre-populate
	// caches) has a clean home.
	n.once.Do(func() {})

	doc, err := prose.NewDocument(
		text,
		prose.WithExtraction(true), // enables NER
		prose.WithSegmentation(false),
		prose.WithTokenization(true),
	)
	if err != nil {
		return nil
	}

	entities := doc.Entities()
	if len(entities) == 0 {
		return nil
	}

	out := make([]span, 0, len(entities))
	cursor := 0
	for _, e := range entities {
		// Audit pii-ner-empty-entity-text-zero-length-span: prose's
		// tokenizer can normalize an entity to empty after stripping
		// punctuation. strings.Index(text[cursor:], "") returns 0, so a
		// zero-length span at position cursor would land in the output,
		// poison the Summary counter, and (for replace mode) emit a
		// spurious "[PERSON_N]" sentinel mid-text. Skip empty.
		if e.Text == "" {
			continue
		}
		var name string
		// Emitted names must match nerEntityNames above (kept adjacent
		// on purpose) and TokenEntityNames' authoritative vocabulary.
		switch e.Label {
		case "PERSON":
			name = "PERSON"
		case "GPE", "LOC", "LOCATION":
			name = "LOCATION"
		default:
			continue
		}
		// Find e.Text starting at cursor. If not found from cursor,
		// fall back to a full scan. If still not found, skip — better
		// to drop one entity than emit a wrong span.
		idx := strings.Index(text[cursor:], e.Text)
		if idx >= 0 {
			start := cursor + idx
			out = append(out, span{
				Name:  name,
				Value: e.Text,
				Start: start,
				End:   start + len(e.Text),
			})
			cursor = start + len(e.Text)
			continue
		}
		idx = strings.Index(text, e.Text)
		if idx < 0 {
			continue
		}
		out = append(out, span{
			Name:  name,
			Value: e.Text,
			Start: idx,
			End:   idx + len(e.Text),
		})
		// Don't move cursor backward — accept the early span without
		// adjusting cursor (next entity's sequential scan still works).
	}
	return out
}

type piiClassifier struct {
	recognizers []Recognizer
	ner         *nerEngine
	allowedNER  map[string]struct{}
}

// NewPIIClassifier adapts the established PII recognizer inventory to the
// privacy classifier seam while preserving standard first-recognizer overlap
// behavior.
func NewPIIClassifier(recognizers []Recognizer, enabled []string, nerEnabled bool) privacy.Classifier {
	var ner *nerEngine
	if nerEnabled {
		ner = NewNEREngine()
	}
	return newPIIClassifierWithNER(recognizers, enabled, ner)
}

func newPIIClassifierWithNER(recognizers []Recognizer, enabled []string, ner *nerEngine) privacy.Classifier {
	allow := make(map[string]struct{}, len(enabled))
	for _, entity := range enabled {
		allow[entity] = struct{}{}
	}
	active := make([]Recognizer, 0, len(recognizers))
	for _, recognizer := range recognizers {
		if len(allow) != 0 {
			if _, ok := allow[recognizer.Name]; !ok {
				continue
			}
		}
		active = append(active, recognizer)
	}
	var allowedNER map[string]struct{}
	if len(allow) != 0 {
		allowedNER = make(map[string]struct{}, len(nerEntityNames))
		for _, entity := range nerEntityNames {
			if _, ok := allow[entity]; ok {
				allowedNER[entity] = struct{}{}
			}
		}
		if len(allowedNER) == 0 {
			ner = nil
		}
	}
	return &piiClassifier{recognizers: active, ner: ner, allowedNER: allowedNER}
}

func (c *piiClassifier) Classify(_ string, value string) []privacy.Finding {
	if value == "" {
		return nil
	}
	findings := make([]privacy.Finding, 0, 4)
	for order, recognizer := range c.recognizers {
		for _, offsets := range recognizer.Pattern.FindAllStringIndex(value, -1) {
			start, end := offsets[0], offsets[1]
			matched := value[start:end]
			if recognizer.Validate != nil && !recognizer.Validate(matched) {
				continue
			}
			if len(recognizer.ContextKeywords) != 0 &&
				!hasContextWithin(value, start, end, recognizer.ContextKeywords) {
				continue
			}
			candidate := privacy.Finding{
				Entity:        recognizer.Name,
				Category:      categoryForEntity(recognizer.Name),
				Kind:          privacy.MatchValidatedRegex,
				Start:         start,
				End:           end,
				RegistryOrder: order,
			}
			if findingOverlaps(findings, candidate) {
				continue
			}
			findings = append(findings, candidate)
		}
	}

	if c.ner != nil {
		for index, candidate := range c.ner.Detect(value) {
			if c.allowedNER != nil {
				if _, ok := c.allowedNER[candidate.Name]; !ok {
					continue
				}
			}
			finding := privacy.Finding{
				Entity:        candidate.Name,
				Category:      privacy.CategoryPersonal,
				Kind:          privacy.MatchNER,
				Start:         candidate.Start,
				End:           candidate.End,
				RegistryOrder: len(c.recognizers) + index,
			}
			if findingOverlaps(findings, finding) {
				continue
			}
			findings = append(findings, finding)
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].Start < findings[j].Start
	})
	return findings
}

func findingOverlaps(findings []privacy.Finding, candidate privacy.Finding) bool {
	for _, existing := range findings {
		if candidate.Start < existing.End && existing.Start < candidate.End {
			return true
		}
	}
	return false
}

func categoryForEntity(entity string) privacy.Category {
	switch entity {
	case "IPv4", "IPv6", "USPhone", "SIP_URI", "IMEI", "IMSI", "MSISDN", "MAC_ADDRESS", "COORDINATES", "SITE":
		return privacy.CategoryTechnical
	default:
		return privacy.CategoryPersonal
	}
}
