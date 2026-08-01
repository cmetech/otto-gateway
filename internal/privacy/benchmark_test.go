package privacy_test

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/privacy"
)

const benchmarkMaximumAcceptedPayload = 4 << 20

type privacyBenchmarkSize struct {
	name  string
	bytes int
}

func BenchmarkPrivacyStandardInbound(b *testing.B) {
	runPrivacyPayloadBenchmark(b, privacy.ProfileStandard, "inbound")
}

func BenchmarkPrivacyStrictInbound(b *testing.B) {
	runPrivacyPayloadBenchmark(b, privacy.ProfileStrict, "inbound")
}

func BenchmarkPrivacyStandardOutbound(b *testing.B) {
	runPrivacyPayloadBenchmark(b, privacy.ProfileStandard, "outbound")
}

func BenchmarkPrivacyStrictOutbound(b *testing.B) {
	runPrivacyPayloadBenchmark(b, privacy.ProfileStrict, "outbound")
}

func runPrivacyPayloadBenchmark(b *testing.B, profile privacy.Profile, direction string) {
	b.Helper()
	for _, size := range []privacyBenchmarkSize{
		{name: "1KiB", bytes: 1 << 10},
		{name: "64KiB", bytes: 64 << 10},
		{name: "4MiBMaximum", bytes: benchmarkMaximumAcceptedPayload},
	} {
		b.Run(size.name, func(b *testing.B) {
			ceiling := privacyBenchmarkCeiling(profile, direction, size.bytes)
			service := newPrivacyBenchmarkService(b, profile, nil)
			payload := benchmarkPayload(size.bytes, "benchmark.task17@example.com 10.20.30.40")
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			b.ReportMetric(float64(ceiling.Nanoseconds()), "ceiling_ns/op")
			b.ResetTimer()

			var measured time.Duration
			for iteration := range b.N {
				scope := fmt.Sprintf("bench-%s-%s-%d", profile, direction, iteration%128)
				switch direction {
				case "inbound":
					state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: string(profile), ScopeID: scope})
					ctx := privacy.WithRequestState(context.Background(), state)
					req := &canonical.ChatRequest{System: payload}
					started := time.Now()
					_, err := service.Before(ctx, req)
					if err == nil && profile == privacy.ProfileStrict {
						// Strict inbound owns a scope lease until After. Include the
						// required zero-body terminal cleanup in this ceiling so the
						// benchmark cannot leak leases to improve its number.
						err = service.After(ctx, req, &canonical.ChatResponse{})
					}
					measured += time.Since(started)
					if err != nil {
						b.Fatalf("iteration %d: %v", iteration, err)
					}
				case "outbound":
					b.StopTimer()
					state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: string(profile), ScopeID: scope})
					ctx := privacy.WithRequestState(context.Background(), state)
					req := &canonical.ChatRequest{System: "benchmark.task17@example.com 10.20.30.40"}
					if _, err := service.Before(ctx, req); err != nil {
						b.Fatalf("setup Before: %v", err)
					}
					responseText := benchmarkPayload(size.bytes, req.System)
					resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
						Kind: canonical.ContentKindText, Text: responseText,
					}}}}
					b.StartTimer()
					started := time.Now()
					err := service.After(ctx, req, resp)
					measured += time.Since(started)
					if err != nil {
						b.Fatalf("iteration %d: %v", iteration, err)
					}
				default:
					b.Fatalf("unknown direction %q", direction)
				}
			}
			average := measured / time.Duration(max(b.N, 1))
			b.ReportMetric(float64(average.Nanoseconds()), "privacy_ns/op")
			if average > ceiling {
				b.Fatalf("privacy %s %s %s average=%s exceeds regression ceiling=%s",
					profile, direction, size.name, average, ceiling)
			}
		})
	}
}

func BenchmarkPrivacyParallel100Scopes(b *testing.B) {
	const scopes = 100
	const ceiling = 250 * time.Millisecond
	service := newPrivacyBenchmarkService(b, privacy.ProfileStrict, nil)
	payloads := make([]string, scopes)
	for index := range scopes {
		payloads[index] = benchmarkPayload(1<<10, fmt.Sprintf("10.0.%d.7", index))
	}
	b.ReportAllocs()
	b.ReportMetric(scopes, "requests/op")
	b.ReportMetric(float64(ceiling.Nanoseconds()), "ceiling_ns/op")
	b.ResetTimer()

	var measured time.Duration
	for iteration := range b.N {
		started := time.Now()
		errs := make(chan error, scopes)
		var workers sync.WaitGroup
		workers.Add(scopes)
		for index := range scopes {
			go func() {
				defer workers.Done()
				state := privacy.NewRequestState(privacy.RequestMetadata{
					RequestedProfile: "strict", ScopeID: fmt.Sprintf("parallel-bench-%03d", index),
				})
				ctx := privacy.WithRequestState(context.Background(), state)
				req := &canonical.ChatRequest{System: payloads[index]}
				if _, err := service.Before(ctx, req); err != nil {
					errs <- err
					return
				}
				errs <- service.After(ctx, req, &canonical.ChatResponse{})
			}()
		}
		workers.Wait()
		measured += time.Since(started)
		close(errs)
		for err := range errs {
			if err != nil {
				b.Fatalf("parallel iteration %d: %v", iteration, err)
			}
		}
	}
	average := measured / time.Duration(max(b.N, 1))
	b.ReportMetric(float64(average.Nanoseconds()), "privacy_ns/op")
	if average > ceiling {
		b.Fatalf("parallel 100-scope average=%s exceeds regression ceiling=%s", average, ceiling)
	}
}

// privacyBenchmarkCeiling records the Task 17 clean three-run baseline taken
// on darwin/arm64 (Apple M3 Ultra) and applies deliberately wide regression
// headroom. Median privacy_ns/op baselines, in 1KiB/64KiB/4MiB order:
//
//	standard inbound:  0.330ms / 22.805ms / 1.393s
//	strict inbound:    0.926ms / 62.073ms / 3.770s
//	standard outbound: 0.037ms /  2.145ms / 0.133s
//	strict outbound:   1.320ms / 91.020ms / 5.479s
//
// These are smoke-test ceilings, not user-facing latency SLOs. They are set
// far enough above the medians to tolerate slower shared CI hosts while still
// catching order-of-magnitude classifier or traversal regressions.
func privacyBenchmarkCeiling(profile privacy.Profile, direction string, size int) time.Duration {
	if profile == privacy.ProfileStandard && direction == "inbound" {
		switch size {
		case 1 << 10:
			return 5 * time.Millisecond
		case 64 << 10:
			return 150 * time.Millisecond
		default:
			return 8 * time.Second
		}
	}
	if profile == privacy.ProfileStrict && direction == "inbound" {
		switch size {
		case 1 << 10:
			return 10 * time.Millisecond
		case 64 << 10:
			return 500 * time.Millisecond
		default:
			return 25 * time.Second
		}
	}
	if profile == privacy.ProfileStandard {
		switch size {
		case 1 << 10:
			return 2 * time.Millisecond
		case 64 << 10:
			return 25 * time.Millisecond
		default:
			return time.Second
		}
	}
	switch size {
	case 1 << 10:
		return 10 * time.Millisecond
	case 64 << 10:
		return 750 * time.Millisecond
	default:
		return 35 * time.Second
	}
}

// TestPrivacyParallelClassificationNoGlobalLock turns the block-profile claim
// into an executable invariant. All 100 requests must enter the configured
// classifier before any is released; a Gateway-wide classification mutex
// would allow only one entry and time out here. Per-scope/store-shard locking
// remains permitted and is exercised by the surrounding lifecycle tests.
func TestPrivacyParallelClassificationNoGlobalLock(t *testing.T) {
	const requests = 100
	const barrierFrame = "privacy_test.(*classificationBarrier).Classify"
	runtime.SetBlockProfileRate(1)
	t.Cleanup(func() { runtime.SetBlockProfileRate(0) })
	baselineProfile, _ := runtimeBlockProfile(t)
	baselineBarrierSamples := blockProfileSamples(baselineProfile, barrierFrame)
	entered := make(chan struct{}, requests*2)
	release := make(chan struct{})
	classifier := &classificationBarrier{entered: entered, release: release}
	service := newPrivacyBenchmarkService(t, privacy.ProfileStrict, classifier)

	errs := make(chan error, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for index := range requests {
		go func() {
			defer workers.Done()
			state := privacy.NewRequestState(privacy.RequestMetadata{
				RequestedProfile: "strict", ScopeID: fmt.Sprintf("lock-proof-%03d", index),
			})
			ctx := privacy.WithRequestState(context.Background(), state)
			req := &canonical.ChatRequest{System: "safe"}
			if _, err := service.Before(ctx, req); err != nil {
				errs <- err
				return
			}
			errs <- service.After(ctx, req, &canonical.ChatResponse{})
		}()
	}

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for range requests {
		select {
		case <-entered:
		case <-deadline.C:
			close(release)
			workers.Wait()
			t.Fatalf("only %d/%d classifiers entered concurrently; possible global lock", classifier.maximum.Load(), requests)
		}
	}
	close(release)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel request: %v", err)
		}
	}
	if maximum := classifier.maximum.Load(); maximum != requests {
		t.Fatalf("maximum concurrent classifiers=%d, want %d", maximum, requests)
	}
	evidence, records := runtimeBlockProfile(t)
	if records == 0 {
		t.Fatalf("runtime block profile contains no samples:\n%s", evidence)
	}
	barrierSamples := blockProfileSamples(evidence, barrierFrame) - baselineBarrierSamples
	if barrierSamples <= 0 {
		t.Fatalf("block profile has no new classifier-barrier samples:\n%s", evidence)
	}
	if mutexRecord := gatewayClassifierMutexBlock(evidence); mutexRecord != "" {
		t.Fatalf("block profile contains a Gateway-wide classifier mutex:\n%s", mutexRecord)
	}
}

func runtimeBlockProfile(tb testing.TB) (string, int) {
	tb.Helper()
	profile := pprof.Lookup("block")
	if profile == nil {
		tb.Fatal("runtime block profile is unavailable")
	}
	var evidence bytes.Buffer
	if err := profile.WriteTo(&evidence, 1); err != nil {
		tb.Fatalf("write runtime block profile: %v", err)
	}
	return evidence.String(), profile.Count()
}

func blockProfileSamples(profile, frame string) int64 {
	var total int64
	for _, record := range strings.Split(profile, "\n\n") {
		if !strings.Contains(record, frame) {
			continue
		}
		for _, line := range strings.Split(record, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[2] != "@" {
				continue
			}
			samples, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				total += samples
			}
			break
		}
	}
	return total
}

func gatewayClassifierMutexBlock(profile string) string {
	for _, record := range strings.Split(profile, "\n\n") {
		if !strings.Contains(record, "sync.(*Mutex).Lock") {
			continue
		}
		if !strings.Contains(record, "internal/privacy.(*Service).transformInbound.func1") &&
			!strings.Contains(record, "internal/privacy.strictClassifierCandidates") {
			continue
		}
		// Scope-store coordination is deliberately allowed, and the stateless
		// secret classifier may contend inside regexp's process-wide sync.Pool.
		if strings.Contains(record, "internal/privacy.(*ScopeStore).") ||
			strings.Contains(record, "internal/privacy.(*ScopeLease).") ||
			strings.Contains(record, "internal/privacy.(*scopeState).") ||
			strings.Contains(record, "internal/privacy.(*SecretClassifier).Classify") {
			continue
		}
		return record
	}
	return ""
}

type classificationBarrier struct {
	entered chan<- struct{}
	release <-chan struct{}
	current atomic.Int64
	maximum atomic.Int64
}

func (c *classificationBarrier) Classify(_, _ string) []privacy.Finding {
	current := c.current.Add(1)
	for {
		maximum := c.maximum.Load()
		if current <= maximum || c.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	c.entered <- struct{}{}
	<-c.release
	c.current.Add(-1)
	return nil
}

func newPrivacyBenchmarkService(tb testing.TB, profile privacy.Profile, classifier privacy.Classifier) *privacy.Service {
	tb.Helper()
	if classifier == nil {
		classifier = pii.NewPIIClassifier(pii.Recognizers, pii.SourceAuditNames(), false)
	}
	profiles := []privacy.Profile{privacy.ProfileStandard}
	if profile == privacy.ProfileStrict {
		profiles = append(profiles, privacy.ProfileStrict)
	}
	service, err := privacy.NewService(privacy.Config{
		DefaultProfile: profile, RequestProfiles: profiles,
		AliasKey: []byte("task17-benchmark-alias-key-32byte"), SecretAction: privacy.ActionReplace,
		TechnicalAction: privacy.ActionPseudonymize, ScopeTTL: time.Hour,
		MaxScopes: 256, MaxEntriesPerScope: 16, MaxTotalEntries: 4096,
		PIIEnabled: true, PIIMode: privacy.ActionEncrypt,
		PIIEncryptKey: []byte("0123456789abcdef0123456789abcdef"),
		Recognizers:   pii.SourceAuditNames(), Classifier: classifier,
		SecretClassifier: privacy.NewSecretClassifier(),
	})
	if err != nil {
		tb.Fatalf("privacy.NewService: %v", err)
	}
	tb.Cleanup(service.Close)
	return service
}

func benchmarkPayload(size int, suffix string) string {
	if len(suffix) >= size {
		return suffix[:size]
	}
	remaining := size - len(suffix)
	filler := strings.Repeat("safe ", remaining/len("safe ")) + strings.Repeat("x", remaining%len("safe "))
	return filler + suffix
}
