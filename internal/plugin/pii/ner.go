// Placeholder for the NER engine wired in Task 9. Defined here so the
// PIIRedactionHook.NER field type exists before its real implementation
// lands. Task 9 replaces this file entirely.

package pii

type nerEngine struct{}

func (n *nerEngine) Detect(text string) []span { return nil }
