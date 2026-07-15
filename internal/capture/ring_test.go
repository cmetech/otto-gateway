package capture

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestRing_RecordSnapshotOrder(t *testing.T) {
	r := NewRing(4, 1024)
	r.Record("a", json.RawMessage(`{"n":1}`))
	r.Record("b", json.RawMessage(`{"n":2}`))

	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Method != "a" || got[1].Method != "b" {
		t.Errorf("order = %q,%q; want a,b", got[0].Method, got[1].Method)
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("seq = %d,%d; want 1,2", got[0].Seq, got[1].Seq)
	}
	if got[0].Params != `{"n":1}` {
		t.Errorf("params = %q; want {\"n\":1}", got[0].Params)
	}
}

func TestRing_OverflowOverwritesOldest(t *testing.T) {
	r := NewRing(2, 1024)
	r.Record("a", json.RawMessage(`1`))
	r.Record("b", json.RawMessage(`2`))
	r.Record("c", json.RawMessage(`3`)) // evicts a

	got := r.Snapshot()
	if len(got) != 2 || got[0].Method != "b" || got[1].Method != "c" {
		t.Fatalf("snapshot = %+v; want [b,c]", got)
	}
	if got[1].Seq != 3 {
		t.Errorf("newest seq = %d; want 3", got[1].Seq)
	}
}

func TestRing_TruncatesOnRuneBoundary(t *testing.T) {
	// A multi-byte rune (é = 2 bytes) straddling the cap must not be split.
	big := `"` + strings.Repeat("é", 20) + `"` // 42 bytes
	r := NewRing(1, 10)
	r.Record("m", json.RawMessage(big))

	f := r.Snapshot()[0]
	if len(f.Params) > 10 {
		t.Errorf("params len = %d, want <= 10 (capBytes)", len(f.Params))
	}
	if !isValidUTF8(f.Params) {
		t.Errorf("truncation split a rune: %q", f.Params)
	}
	if f.Bytes != len(big) {
		t.Errorf("Bytes = %d, want %d (pre-truncation length)", f.Bytes, len(big))
	}
}

func TestRing_ConcurrentRecord(t *testing.T) {
	r := NewRing(1024, 64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Record("m", json.RawMessage(`{}`))
			}
		}()
	}
	wg.Wait()
	if n := len(r.Snapshot()); n != 800 {
		t.Errorf("recorded %d frames, want 800", n)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
