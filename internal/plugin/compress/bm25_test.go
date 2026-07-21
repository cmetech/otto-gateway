// internal/plugin/compress/bm25_test.go
package compress

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"Hello, World!", []string{"hello", "world"}},
		{"snake_case_ident stays whole", []string{"snake_case_ident", "stays", "whole"}},
		{"HTTP2 400 err_code=EOF", []string{"http2", "400", "err_code", "eof"}},
		{"Grüße 世界 42", []string{"grüße", "世界", "42"}}, // Unicode letters/digits kept
		{"---   \t\n---", nil}, // separators only → no tokens
	}
	for _, c := range cases {
		if got := tokenize(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// Determinism: same input, same output, every time.
	for i := 0; i < 3; i++ {
		if got := tokenize("Hello, World!"); !reflect.DeepEqual(got, []string{"hello", "world"}) {
			t.Fatal("tokenize is not deterministic")
		}
	}
}

func TestBM25Rank_SharedTermsRankHigher(t *testing.T) {
	ctx := context.Background()
	qi := newQueryIndex("database connection timeout in pool_manager")
	docs := []string{
		"the pool_manager raised a database connection timeout again", // overlaps
		"completely unrelated prose about weather and cooking",        // no overlap
	}
	scores := bm25Rank(ctx, qi, docs)
	if scores[0] <= scores[1] {
		t.Errorf("overlapping doc must outscore unrelated doc: %v", scores)
	}
	if scores[1] != 0 {
		t.Errorf("zero-overlap doc must score exactly 0, got %v", scores[1])
	}
}

func TestBM25Rank_Guards(t *testing.T) {
	ctx := context.Background()
	// Empty query, empty corpus, all-empty docs — all zeros, no NaN, no panic.
	for name, c := range map[string]struct {
		query string
		docs  []string
	}{
		"empty-query":  {"...!!!", []string{"a doc"}},
		"no-docs":      {"a", nil},
		"empty-corpus": {"a", []string{"", "  ...  "}}, // avgLen 0
	} {
		for _, s := range bm25Rank(ctx, newQueryIndex(c.query), c.docs) {
			if s != 0 {
				t.Errorf("%s: scored %v, want 0", name, s)
			}
		}
	}
}

func TestBM25Rank_DeterministicManyNearTiedTerms(t *testing.T) {
	// Revision-4 MAJOR: accumulation order must be fixed, not map-range.
	// Hundreds of terms with near-tied contributions is exactly where
	// order-dependent float addition would flip last bits between runs.
	ctx := context.Background()
	var qb, d1, d2 strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&qb, "term%03d ", i)
		if i%2 == 0 {
			fmt.Fprintf(&d1, "term%03d filler ", i)
		} else {
			fmt.Fprintf(&d2, "term%03d filler ", i)
		}
	}
	qi := newQueryIndex(qb.String())
	docs := []string{d1.String(), d2.String(), "nothing shared here"}
	first := bm25Rank(ctx, qi, docs)
	for i := 0; i < 5; i++ {
		if !reflect.DeepEqual(bm25Rank(ctx, newQueryIndex(qb.String()), docs), first) {
			t.Fatal("bm25Rank scores differ between identical runs")
		}
	}
	if first[2] != 0 {
		t.Errorf("zero-overlap doc scored %v, want exactly 0", first[2])
	}
}

func TestBM25Rank_CancelledContextReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := bm25Rank(ctx, newQueryIndex("alpha"), []string{"alpha beta"}); got != nil {
		t.Errorf("cancelled ctx: got %v, want nil (stage-4 no-op signal)", got)
	}
}

func TestNewQueryIndex_OverflowFailsClosed(t *testing.T) {
	// Revision-5 MAJOR: exceeding the cap must NOT rank on the prefix —
	// overflow marks the index unusable and bm25Rank returns all zeros.
	var qb strings.Builder
	for i := 0; i < maxQueryTerms+500; i++ {
		fmt.Fprintf(&qb, "u%05d ", i)
	}
	qi := newQueryIndex(qb.String())
	if !qi.overflow {
		t.Fatal("over-cap query did not set overflow")
	}
	scores := bm25Rank(context.Background(), qi, []string{"u00000 overlaps the prefix"})
	for _, s := range scores {
		if s != 0 {
			t.Errorf("overflowed index still ranked: %v", s)
		}
	}

	// EXACTLY at the cap: complete index, no overflow, ranking works.
	var qb2 strings.Builder
	for i := 0; i < maxQueryTerms; i++ {
		fmt.Fprintf(&qb2, "u%05d ", i)
	}
	qi2 := newQueryIndex(qb2.String())
	if qi2.overflow || qi2.n != maxQueryTerms {
		t.Fatalf("at-cap query: overflow=%v n=%d, want false/%d", qi2.overflow, qi2.n, maxQueryTerms)
	}
	if s := bm25Rank(context.Background(), qi2, []string{"u00000 u00001"}); s[0] <= 0 {
		t.Error("at-cap (complete) index must still rank")
	}
	// Repeats of already-indexed terms past the cap are NOT overflow.
	qi3 := newQueryIndex(qb2.String() + " u00000 u00001")
	if qi3.overflow {
		t.Error("repeated known terms wrongly flagged as overflow")
	}
}

// BenchmarkBM25RankAdversarial locks the sparse single-pass complexity
// (revision-4 CRITICAL): scale query vocabulary and candidate count
// independently — ns/op and allocs must grow roughly linearly in
// (queryTokens + totalDocTokens), NOT as their product. "shared" leads
// the query (revision-5 MINOR: appended after the cap it was never
// indexed, so the big rows scored all-zero and skipped the match/sort/
// scoring path entirely). In-cap rows genuinely scale the indexed
// vocabulary; the over-cap row measures the fail-closed path (must be
// near-instant: overflow detected during query indexing, no doc scans).
func BenchmarkBM25RankAdversarial(b *testing.B) {
	mkQuery := func(nTerms int) string {
		var sb strings.Builder
		sb.WriteString("shared ") // FIRST — always indexed → real matches in every in-cap row
		for i := 0; i < nTerms-1; i++ {
			fmt.Fprintf(&sb, "q%06d ", i)
		}
		return sb.String()
	}
	mkDocs := func(n int) []string {
		docs := make([]string, n)
		for i := range docs {
			docs[i] = "shared " + strings.Repeat("dfiller ", 24) // ~200 bytes, overlaps query
		}
		return docs
	}
	for _, nq := range []int{1000, 2000, 4000} { // all within maxQueryTerms
		for _, nd := range []int{500, 1000} {
			b.Run(fmt.Sprintf("qterms=%d/docs=%d", nq, nd), func(b *testing.B) {
				query, docs := mkQuery(nq), mkDocs(nd)
				// Sanity: the match path is actually exercised.
				if s := bm25Rank(context.Background(), newQueryIndex(query), docs[:1]); s[0] <= 0 {
					b.Fatal("benchmark setup broken: shared term not matching")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					qi := newQueryIndex(query)
					_ = bm25Rank(context.Background(), qi, docs)
				}
			})
		}
	}
	b.Run("overcap-failclosed/qterms=20000/docs=1000", func(b *testing.B) {
		query, docs := mkQuery(20_000), mkDocs(1000)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			qi := newQueryIndex(query) // overflow → bm25Rank returns zeros without scanning docs
			_ = bm25Rank(context.Background(), qi, docs)
		}
	})
}
