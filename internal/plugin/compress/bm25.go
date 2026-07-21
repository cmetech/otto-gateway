// internal/plugin/compress/bm25.go
// Stage 4's relevance scorer: Okapi BM25 over a deterministic,
// stdlib-only tokenization, implemented as a SPARSE SINGLE PASS. Local
// by design — the deployed topology is otto-gateway + kiro-cli only, so
// relevance scoring must not require a network endpoint, model weights,
// or CGO. BM25 measures exact lexical overlap (identifiers, error
// strings, names) — not synonyms or paraphrases; note that no-space CJK
// text tokenizes into sentence-sized runs, making stage 4 largely inert
// for it (documented operator limitation).
//
// Cost discipline (revision-4 CRITICAL): Before runs synchronously on
// the request path and its inputs are client-controlled up to the 4 MiB
// body cap, so this file must never do O(uniqueQueryTerms × candidates)
// work. Everything here is O(queryTokens + totalDocTokens +
// matches·log(matches)), with per-token allocations avoided (tokens are
// substrings of a single ToLower'd copy) and ctx cancellation honored
// between documents.

package compress

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// maxQueryTerms bounds how many UNIQUE query terms stage 4 will
	// index (df/idf arrays, per-doc match lists) against megabyte-scale
	// adversarial questions. Exceeding it FAILS CLOSED: queryIndex sets
	// overflow and stage 4 no-ops (revision-5 MAJOR — ranking on the
	// first-4096-terms PREFIX would let an attacker-chosen preamble
	// authorize pruning while the real question past the cap is
	// ignored). 4096 unique terms is far beyond any real user question.
	maxQueryTerms = 4096
	// cancelCheckEvery: token interval for ctx checks inside a single
	// large document scan (a lone near-4-MiB candidate must not run to
	// completion after the client disconnects).
	//
	// ACCEPTED RESIDUAL (final review sign-off): checks are per-token,
	// so the query projection and a pathological multi-megabyte single
	// token can still finish after disconnect. That is one linear pass
	// bounded by the 4-MiB body cap (~ms of CPU); a byte-level check
	// would put a branch in the zero-allocation hot loop and tax every
	// LIVE request to save milliseconds on dead ones. Do not "fix" this.
	cancelCheckEvery = 4096
)

// forEachToken lowercases s once and streams its tokens to fn — a token
// is a maximal run of Unicode letters, Unicode digits, or '_'; all
// other runes separate; empty tokens are discarded. Each token is a
// substring of the single lowered copy, so the scan allocates nothing
// per token. fn returning false stops the scan early. No stemming,
// stop-words, synonyms, or language detection: deterministic by
// construction.
func forEachToken(s string, fn func(tok string) bool) {
	s = strings.ToLower(s)
	start := -1
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if !fn(s[start:i]) {
				return
			}
			start = -1
		}
	}
	if start >= 0 {
		fn(s[start:])
	}
}

// tokenize materializes forEachToken's stream — used for tests and the
// (small) query; documents are streamed, never materialized.
func tokenize(s string) []string {
	var toks []string
	forEachToken(s, func(tok string) bool {
		toks = append(toks, tok)
		return true
	})
	return toks
}

// queryIndex assigns stable integer IDs to the query's unique terms in
// FIRST-SEEN order. Stable IDs are what make downstream accumulation
// order-deterministic. overflow means the query had MORE than
// maxQueryTerms unique terms — the index is incomplete and MUST NOT be
// used for ranking (fail closed; revision-5 MAJOR).
type queryIndex struct {
	ids      map[string]int
	n        int
	overflow bool
}

// newQueryIndex tokenizes the query text and indexes its unique terms.
// n == 0 or overflow == true mean stage 4 must no-op.
func newQueryIndex(query string) *queryIndex {
	qi := &queryIndex{ids: make(map[string]int)}
	forEachToken(query, func(tok string) bool {
		if _, ok := qi.ids[tok]; ok {
			return true
		}
		if qi.n >= maxQueryTerms {
			qi.overflow = true // incomplete index — caller must no-op
			return false
		}
		qi.ids[tok] = qi.n
		qi.n++
		return true
	})
	return qi
}

// bm25Rank scores every doc against the indexed query with Okapi BM25:
//
//	idf(t)     = ln(1 + (N - df(t) + 0.5) / (df(t) + 0.5))
//	score(doc) = Σ over matched query terms of
//	             idf(t) · tf(t,doc)·(k1+1) / (tf(t,doc) + k1·(1 − b + b·|doc|/avg))
//
// Single pass per document: one streaming token scan accumulates the
// doc length and sparse per-query-term tf counts; df updates once per
// matched term per doc. Scoring iterates each doc's matched IDs in
// ASCENDING order — floating-point accumulation order is therefore
// identical run-to-run (map-range accumulation could flip near-tied
// scores). A doc sharing no query term scores exactly 0 — the caller's
// zero-evidence stop depends on that exactness.
//
// Guards: nil/empty/overflowed query index, no docs, or an all-empty
// corpus return all zeros (overflow means the index is an incomplete
// prefix — never rank on it); no NaN or division by zero is possible.
// ctx is checked between documents AND every cancelCheckEvery tokens
// inside a document (one candidate can approach the 4-MiB body cap);
// on cancellation bm25Rank returns nil and the caller must treat
// stage 4 as a no-op.
func bm25Rank(ctx context.Context, qi *queryIndex, docs []string) []float64 {
	scores := make([]float64, len(docs))
	if qi == nil || qi.n == 0 || qi.overflow || len(docs) == 0 {
		return scores
	}
	type match struct{ qid, tf int }
	df := make([]int, qi.n)
	docLens := make([]int, len(docs))
	matches := make([][]match, len(docs))
	tfBuf := make([]int, qi.n)
	touched := make([]int, 0, 16)
	totalLen := 0

	for d, text := range docs {
		if ctx.Err() != nil {
			return nil // cancelled — caller treats stage 4 as a no-op
		}
		docLen := 0
		cancelled := false
		forEachToken(text, func(tok string) bool {
			docLen++
			if docLen%cancelCheckEvery == 0 && ctx.Err() != nil {
				cancelled = true
				return false
			}
			if qid, ok := qi.ids[tok]; ok {
				if tfBuf[qid] == 0 {
					touched = append(touched, qid)
				}
				tfBuf[qid]++
			}
			return true
		})
		if cancelled {
			return nil
		}
		docLens[d] = docLen
		totalLen += docLen
		if len(touched) > 0 {
			sort.Ints(touched) // ascending qid — deterministic accumulation order
			ms := make([]match, len(touched))
			for k, qid := range touched {
				ms[k] = match{qid: qid, tf: tfBuf[qid]}
				df[qid]++
				tfBuf[qid] = 0
			}
			matches[d] = ms
			touched = touched[:0]
		}
	}
	if totalLen == 0 {
		return scores // all-empty corpus — no evidence, all zeros
	}
	avgLen := float64(totalLen) / float64(len(docs))

	n := float64(len(docs))
	idf := make([]float64, qi.n)
	for qid := range idf {
		if df[qid] > 0 {
			idf[qid] = math.Log(1 + (n-float64(df[qid])+0.5)/(float64(df[qid])+0.5))
		}
	}
	for d := range docs {
		docLen := float64(docLens[d])
		for _, m := range matches[d] {
			f := float64(m.tf)
			scores[d] += idf[m.qid] * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*docLen/avgLen))
		}
	}
	return scores
}
