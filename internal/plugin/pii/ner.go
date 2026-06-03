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
	"strings"
	"sync"

	"github.com/jdkato/prose/v2"
)

// nerEngine wraps prose under a sync.Once. The Document is NOT cached
// across calls — prose Documents are constructed per-text — but the
// sync.Once gates whatever one-time global state prose may lazy-init.
type nerEngine struct {
	once sync.Once
}

// NewNEREngine constructs an engine. The prose tagger/tokenizer global
// state is NOT touched until the first Detect call.
func NewNEREngine() *nerEngine {
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
		var name string
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
