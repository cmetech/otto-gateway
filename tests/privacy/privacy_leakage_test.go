package privacy_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/admin"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/metrics"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/privacy"
	privacyserver "otto-gateway/internal/server"
)

const (
	leakageInputCanary     = "sk-proj-Task17LeakageInputCanary1234567890ABCDE"
	leakageErrorCanary     = "sk-proj-Task17LeakageErrorCanary1234567890ABCDE"
	leakagePersonalCanary  = "reversible.task17@example.com"
	leakageTechnicalCanary = "10.77.88.99"
)

func TestLeakageAcrossOperationalSurfaces(t *testing.T) {
	newLeakageSurfaceHarness(t).assertCanaryAbsent(t)
}

type leakageSurfaceHarness struct {
	surfaces        map[string]string
	protectedValues []string
	evidence        leakageSurfaceEvidence
}

type leakageSurfaceEvidence struct {
	reversiblePersonal        bool
	reversibleTechnicalLedger bool
	realHealthPath            bool
	realAdminPath             bool
	realSupportArtifact       bool
}

func newLeakageSurfaceHarness(t *testing.T) *leakageSurfaceHarness {
	t.Helper()

	privacyMetrics := metrics.New(metrics.BuildInfo{
		GatewayID: "task17-gateway", Version: "task17", Commit: "task17",
	}, func() metrics.PoolStats { return metrics.PoolStats{} },
		func() metrics.SessionStats { return metrics.SessionStats{} },
		func() []metrics.WorkerProc { return nil })
	var gateway *conformanceServer
	privacyMetrics.RegisterPrivacy(func() metrics.PrivacyStats {
		if gateway == nil {
			return metrics.PrivacyStats{}
		}
		snapshot := gateway.service.Snapshot()
		return metrics.PrivacyStats{
			ScopesActive: snapshot.ScopesActive, RequestsInFlight: snapshot.RequestsInFlight,
			Entries: snapshot.Entries, MaxScopes: snapshot.MaxScopes,
			MaxEntriesPerScope: snapshot.MaxEntriesPerScope, MaxTotalEntries: snapshot.MaxTotalEntries,
			ScopeTTL: snapshot.ScopeTTL, OldestScopeAge: snapshot.OldestScopeAge,
			TriageEnabled: snapshot.TriageEnabled,
		}
	})
	gateway = newConformanceServerWith(t, func(config *privacy.Config) {
		config.Observers = privacyMetricObservers(privacyMetrics)
	}, nil)

	fixture := conformanceRouteFixturesFor(false, strings.Join([]string{
		"use", leakageInputCanary, "for", leakagePersonalCanary, "from", leakageTechnicalCanary,
	}, " "), "leakage-pass")[2]
	resp, responseBody := gateway.post(t, fixture)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("strict pass status=%d", resp.StatusCode)
	}
	receiptHeader := resp.Header.Get("X-GW-Privacy-Receipt")
	receipt := requireStrictReceipt(t, receiptHeader)
	personalRestored := strings.Contains(responseBody, leakagePersonalCanary)
	technicalRestored := strings.Contains(responseBody, leakageTechnicalCanary)
	if !personalRestored || !technicalRestored || receipt.Restored < 2 {
		t.Fatal("strict pass did not restore both reversible leakage canary classes")
	}

	entries, err := gateway.service.TriageCapability().InspectScope("leakage-pass")
	if err != nil {
		t.Fatalf("inspect reversible leakage ledger: %v", err)
	}
	technicalLedger := false
	protectedValues := []string{
		leakageInputCanary, leakageErrorCanary, leakagePersonalCanary, leakageTechnicalCanary,
	}
	for _, entry := range entries {
		if entry.Entity == "IPv4" && entry.Original == leakageTechnicalCanary && entry.Synthetic != "" {
			technicalLedger = true
			protectedValues = append(protectedValues, entry.Synthetic)
		}
		if entry.Original == leakagePersonalCanary {
			t.Fatal("personal canary entered the process-lifetime technical mapping ledger")
		}
	}
	if !technicalLedger {
		t.Fatal("technical leakage canary did not enter the reversible mapping ledger")
	}

	blockedWorker := &captureWorker{response: func(workerRecord) []canonical.Chunk {
		return []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: leakageErrorCanary}}}
	}}
	blockedGateway := newConformanceServerWith(t, nil, blockedWorker)
	blockedFixture := conformanceRouteFixturesFor(true, "safe", "leakage-error")[4]
	blockedResp, blockedBody := blockedGateway.post(t, blockedFixture)
	if blockedResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("strict block status=%d", blockedResp.StatusCode)
	}
	decodeReceiptFixture(t, blockedResp.Header.Get("X-GW-Privacy-Receipt"))

	metricRecorder := httptest.NewRecorder()
	privacyMetrics.Handler().ServeHTTP(metricRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if metricRecorder.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", metricRecorder.Code)
	}

	captureParams, err := json.Marshal(map[string]string{
		"authorization": "Bearer " + leakageInputCanary,
		"payload":       "client_secret=" + leakageErrorCanary,
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := &leakageCaptureSource{frames: []admin.CaptureFrame{{Seq: 1, Params: string(captureParams)}}}
	adminHandler := admin.WithSecretRedactor(admin.Handler(admin.Deps{
		Version: "task17", Commit: "task17", Start: time.Now(),
		PrivacyStatus: leakagePrivacyStatus{service: gateway.service},
		// Seed the actual secret-bearing dependency too: ordinary pages must
		// publish presence only, never this capability value.
		PrivacyTriageToken: leakageInputCanary,
		AcpCapture:         capture,
	}), privacy.NewSecretClassifier())
	operationalLogs := &strings.Builder{}
	operationalHandler := privacyserver.NewFromConfig(privacyserver.Config{
		Logger:         slog.New(slog.NewJSONHandler(operationalLogs, nil)),
		Version:        "task17",
		Commit:         "task17",
		AdminHandler:   adminHandler,
		Hooks:          leakageHooksSource{service: gateway.service},
		MetricsHandler: privacyMetrics.Handler(),
	})
	operationalServer := httptest.NewServer(operationalHandler)
	t.Cleanup(operationalServer.Close)

	surfaces := map[string]string{
		"ordinary_logs":      gateway.logs.String() + blockedGateway.logs.String(),
		"strict_chat_traces": gateway.trace.String() + blockedGateway.trace.String(),
		"metrics":            metricRecorder.Body.String(),
		"pass_receipt":       receiptHeader,
		"block_receipt":      blockedResp.Header.Get("X-GW-Privacy-Receipt"),
		"pass_response":      responseBody,
		"native_error":       blockedBody,
		"worker_boundary":    gateway.worker.recordsCopy()[0].joinedText(),
	}
	for _, path := range []string{
		"/health", "/health/hooks", "/admin/", "/admin/about", "/admin/privacy", "/admin/docs", "/admin/api/snapshot",
	} {
		recorder := httptest.NewRecorder()
		operationalHandler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("operational GET %s status=%d", path, recorder.Code)
		}
		surfaces["path_"+path] = recorder.Body.String()
	}
	captureRecorder := httptest.NewRecorder()
	operationalHandler.ServeHTTP(captureRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/api/acp-capture?support=redacted", nil))
	if captureRecorder.Code != http.StatusOK {
		t.Fatalf("support capture status=%d", captureRecorder.Code)
	}
	surfaces["capture_support_projection"] = captureRecorder.Body.String()
	for name, body := range collectPOSIXSupportArtifact(t, operationalServer.URL) {
		surfaces[name] = body
	}
	surfaces["operational_route_logs"] = operationalLogs.String()

	return &leakageSurfaceHarness{
		surfaces: surfaces, protectedValues: protectedValues,
		evidence: leakageSurfaceEvidence{
			reversiblePersonal: personalRestored, reversibleTechnicalLedger: technicalLedger,
			realHealthPath: surfaces["path_/health"] != "" && surfaces["path_/health/hooks"] != "",
			realAdminPath:  surfaces["path_/admin/api/snapshot"] != "",
			realSupportArtifact: surfaces["support/MANIFEST.txt"] != "" &&
				surfaces["support/health/health.json"] != "" && surfaces["support/health/snapshot.json"] != "",
		},
	}
}

func (h *leakageSurfaceHarness) assertCanaryAbsent(t *testing.T) {
	t.Helper()
	if h.evidence != (leakageSurfaceEvidence{
		reversiblePersonal: true, reversibleTechnicalLedger: true,
		realHealthPath: true, realAdminPath: true, realSupportArtifact: true,
	}) {
		t.Fatalf("operational leakage evidence incomplete: %+v", h.evidence)
	}
	if len(h.surfaces) < 15 {
		t.Fatalf("operational leakage surface coverage=%d, want at least 15", len(h.surfaces))
	}
	for surface, body := range h.surfaces {
		if body == "" {
			t.Errorf("%s produced an empty projection", surface)
			continue
		}
		canaries := h.protectedValues
		switch surface {
		case "pass_response":
			canaries = append(append([]string(nil), h.protectedValues[:2]...), h.protectedValues[4:]...)
		case "worker_boundary":
			canaries = h.protectedValues[:4]
		}
		for _, canary := range canaries {
			if strings.Contains(body, canary) {
				t.Errorf("%s leaked a protected value", surface)
			}
		}
	}
}

type leakageHooksSource struct {
	service *privacy.Service
}

func (s leakageHooksSource) Describe() (pre, post []privacyserver.HookDescription) {
	config := s.service.Describe()
	config["privacy"] = leakagePrivacyStatus(s).PrivacySnapshot()
	return []privacyserver.HookDescription{{
		Name: "PIIRedactionHook", Kind: "Pre,Post", Enabled: true, Config: config,
	}}, nil
}

func collectPOSIXSupportArtifact(t *testing.T, gatewayURL string) map[string]string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("real POSIX support collector requires a POSIX host")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("real POSIX support collector requires /bin/bash")
	}

	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve privacy test directory: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	wrapper := filepath.Join(repoRoot, "scripts", "gw")
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("locate POSIX support collector: %v", err)
	}

	isolationRoot := t.TempDir()
	paths := map[string]string{
		"cwd":       filepath.Join(isolationRoot, "cwd"),
		"install":   filepath.Join(isolationRoot, "install"),
		"gateway":   filepath.Join(isolationRoot, "gateway-home"),
		"state":     filepath.Join(isolationRoot, "gateway-home", "state"),
		"logs":      filepath.Join(isolationRoot, "gateway-home", "logs"),
		"kiro":      filepath.Join(isolationRoot, "kiro"),
		"coworker":  filepath.Join(isolationRoot, "co-worker"),
		"output":    filepath.Join(isolationRoot, "output"),
		"temporary": filepath.Join(isolationRoot, "temporary"),
		"extracted": filepath.Join(isolationRoot, "extracted"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create isolated support fixture: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(paths["coworker"], "logs"), 0o700); err != nil {
		t.Fatalf("create isolated co-worker fixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", wrapper,
		"support", "--out", paths["output"], "--max-mb", "5", "--log-days", "1",
		"--co-worker-home", paths["coworker"])
	cmd.Dir = paths["cwd"]
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"LANG=C", "LC_ALL=C",
		"COPYFILE_DISABLE=1", "COPY_EXTENDED_ATTRIBUTES_DISABLE=1",
		"TMPDIR=" + paths["temporary"],
		"GW_ID=task17-isolated",
		"GW_HOME=" + paths["gateway"],
		"GW_INSTALL_DIR=" + paths["install"],
		"GW_BIN=/bin/true",
		"GW_SUPPORT_REDACTOR_BIN=/bin/true",
		"GW_STATE_DIR=" + paths["state"],
		"GW_PID=" + filepath.Join(paths["state"], "gateway.pid"),
		"GW_LOG=" + filepath.Join(paths["logs"], "gateway.log"),
		"GW_LOG_BOOT=" + filepath.Join(paths["logs"], "gateway-boot.log"),
		"GW_ADDR=" + gatewayURL,
		"KIRO_CWD=" + paths["kiro"],
		"KIRO_CHAT_LOG_FILE=" + filepath.Join(paths["kiro"], "kiro-chat.log"),
		"HERMES_HOME=" + paths["coworker"],
		"CHAT_TRACE=false",
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatal("isolated POSIX support collector exceeded its time bound")
		}
		t.Fatal("isolated POSIX support collector failed")
	}

	archives, err := filepath.Glob(filepath.Join(paths["output"], "gateway-support-*.tar.gz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("isolated POSIX support artifacts=%d, want exactly one", len(archives))
	}
	surfaces := extractSupportArtifact(t, archives[0], paths["extracted"])
	userHome, err := os.UserHomeDir()
	if err == nil && userHome != "" {
		for _, body := range surfaces {
			if strings.Contains(body, userHome) {
				t.Fatal("isolated POSIX support artifact resolved a user-home path")
			}
		}
	}
	return surfaces
}

func extractSupportArtifact(t *testing.T, archivePath, destination string) map[string]string {
	t.Helper()
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open POSIX support artifact: %v", err)
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatalf("open compressed POSIX support artifact: %v", err)
	}
	defer compressed.Close()

	reader := tar.NewReader(compressed)
	bundleRoot := ""
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("read POSIX support artifact: %v", nextErr)
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			t.Fatal("POSIX support artifact contained an unsafe path")
		}
		parts := strings.Split(filepath.ToSlash(clean), "/")
		if bundleRoot == "" {
			bundleRoot = parts[0]
		}
		if parts[0] != bundleRoot {
			t.Fatal("POSIX support artifact contained multiple roots")
		}
		target := filepath.Join(destination, clean)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatalf("extract POSIX support directory: %v", err)
			}
		case tar.TypeReg:
			if header.Size > 5<<20 {
				t.Fatal("POSIX support artifact member exceeded the test bound")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatalf("extract POSIX support parent: %v", err)
			}
			body, err := io.ReadAll(io.LimitReader(reader, (5<<20)+1))
			if err != nil || int64(len(body)) != header.Size {
				t.Fatal("read bounded POSIX support artifact member")
			}
			if err := os.WriteFile(target, body, 0o600); err != nil {
				t.Fatalf("extract POSIX support member: %v", err)
			}
		default:
			t.Fatal("POSIX support artifact contained a non-regular member")
		}
	}
	if bundleRoot == "" {
		t.Fatal("POSIX support artifact was empty")
	}

	surfaces := make(map[string]string)
	extractedRoot := filepath.Join(destination, bundleRoot)
	err = filepath.WalkDir(extractedRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(extractedRoot, path)
		if relErr != nil {
			return relErr
		}
		surfaces["support/"+filepath.ToSlash(relative)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("scan extracted POSIX support tree: %v", err)
	}
	for _, required := range []string{
		"support/MANIFEST.txt", "support/env/effective.env", "support/health/health.json",
		"support/health/snapshot.json", "support/system/system.txt", "support/tray/pidfile.txt",
	} {
		if _, ok := surfaces[required]; !ok {
			t.Fatalf("POSIX support artifact omitted required member %s", required)
		}
	}
	return surfaces
}

func privacyMetricObservers(recorder *metrics.Metrics) privacy.Observers {
	return privacy.Observers{
		Request: func(profile privacy.Profile, surface, workload, result string) {
			recorder.RecordPrivacyRequest(string(profile), surface, workload, result)
		},
		Transformation: func(profile privacy.Profile, entity string, action privacy.Action) {
			recorder.RecordPrivacyTransformation(string(profile), entity, string(action))
		},
		Restoration: func(profile privacy.Profile, entity, result string) {
			recorder.RecordPrivacyRestoration(string(profile), entity, result)
		},
		Block: func(profile privacy.Profile, stage, reason string) {
			recorder.RecordPrivacyBlock(string(profile), stage, reason)
		},
		Residual: func(profile privacy.Profile, stage, entity string) {
			recorder.RecordPrivacyResidual(string(profile), stage, entity)
		},
		Receipt: func(profile privacy.Profile, result string) {
			recorder.RecordPrivacyReceipt(string(profile), result)
		},
		Duration: func(profile privacy.Profile, stage string, elapsed time.Duration) {
			recorder.ObservePrivacyDuration(string(profile), stage, elapsed)
		},
		ScopeEvent:        recorder.RecordPrivacyScopeEvent,
		CapacityRejection: recorder.RecordPrivacyCapacityRejection,
		MappingOperation:  recorder.RecordPrivacyMappingOperation,
		InternalError:     recorder.RecordPrivacyError,
		Triage:            recorder.RecordPrivacyTriage,
	}
}

type leakagePrivacyStatus struct {
	service *privacy.Service
}

func (s leakagePrivacyStatus) PrivacySnapshot() admin.PrivacySnapshot {
	snapshot := s.service.Snapshot()
	profiles := make([]string, len(snapshot.RequestProfiles))
	for index, profile := range snapshot.RequestProfiles {
		profiles[index] = string(profile)
	}
	actions := make(map[string]string, len(snapshot.EntityActions))
	for entity, action := range snapshot.EntityActions {
		actions[entity] = string(action)
	}
	return admin.PrivacySnapshot{
		DefaultProfile: string(snapshot.DefaultProfile), RequestProfiles: profiles,
		StrictAvailable: snapshot.StrictAvailable, TriageEnabled: snapshot.TriageEnabled,
		AliasKeyPresent: snapshot.AliasKeyPresent, TriageTokenPresent: true,
		PIIEnabled: snapshot.PIIEnabled, NEREnabled: snapshot.NEREnabled,
		SecretAction: string(snapshot.SecretAction), TechnicalAction: string(snapshot.TechnicalAction),
		PIIMode: string(snapshot.PIIMode), Recognizers: append([]string(nil), snapshot.Recognizers...),
		EntityActions: actions, StrictFullBuffering: true, ReceiptVersion: 1,
		ScopesActive: snapshot.ScopesActive, RequestsInFlight: snapshot.RequestsInFlight,
		Entries: snapshot.Entries, MaxScopes: snapshot.MaxScopes,
		MaxEntriesPerScope: snapshot.MaxEntriesPerScope, MaxTotalEntries: snapshot.MaxTotalEntries,
		ScopeTTLSeconds: snapshot.ScopeTTL.Seconds(), OldestScopeAgeSeconds: snapshot.OldestScopeAge.Seconds(),
		RequestsProtected: snapshot.RequestsProtected, RequestsBlocked: snapshot.RequestsBlocked,
		LastErrorCode: snapshot.LastErrorCode,
	}
}

type leakageCaptureSource struct {
	frames  []admin.CaptureFrame
	enabled bool
}

func (s *leakageCaptureSource) Snapshot() []admin.CaptureFrame {
	return append([]admin.CaptureFrame(nil), s.frames...)
}
func (s *leakageCaptureSource) Enabled() bool            { return true }
func (s *leakageCaptureSource) AllowRuntimeToggle() bool { return false }
func (s *leakageCaptureSource) Count() int               { return len(s.frames) }
func (s *leakageCaptureSource) Size() int                { return len(s.frames) }
func (s *leakageCaptureSource) Enable()                  { s.enabled = true }
func (s *leakageCaptureSource) Disable()                 { s.enabled = false }
func (s *leakageCaptureSource) Clear()                   { s.frames = nil }

func TestParallelLifecycleStress(t *testing.T) {
	assertConcurrentLifecycleEvidence(t, runConcurrentLifecycleStress(t))

	service := newLifecycleStressService(t, time.Hour, 100, 2, 200)
	const scopeCount = 100
	const requestsPerScope = 5

	type scopeResult struct {
		index int
		alias string
		err   error
	}
	results := make(chan scopeResult, scopeCount)
	var workers sync.WaitGroup
	workers.Add(scopeCount)
	for index := range scopeCount {
		go func() {
			defer workers.Done()
			scope := fmt.Sprintf("parallel-%03d", index)
			original := fmt.Sprintf("10.0.%d.7", index)
			stableAlias := ""
			for requestIndex := range requestsPerScope {
				state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
				ctx := privacy.WithRequestState(context.Background(), state)
				req := &canonical.ChatRequest{System: original}
				if _, err := service.Before(ctx, req); err != nil {
					results <- scopeResult{index: index, err: fmt.Errorf("request %d Before: %w", requestIndex, err)}
					return
				}
				alias := firstBenchmarkIPv4(req.System)
				if alias == "" || stableAlias != "" && alias != stableAlias {
					_ = service.After(ctx, req, nil)
					results <- scopeResult{index: index, err: fmt.Errorf("request %d alias=%q stable=%q", requestIndex, alias, stableAlias)}
					return
				}
				stableAlias = alias
				resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText, Text: alias,
				}}}}
				if err := service.After(ctx, req, resp); err != nil {
					results <- scopeResult{index: index, err: fmt.Errorf("request %d After: %w", requestIndex, err)}
					return
				}
				if got := resp.Message.Content[0].Text; got != original {
					results <- scopeResult{index: index, err: fmt.Errorf("request %d restored=%q want=%q", requestIndex, got, original)}
					return
				}
			}
			results <- scopeResult{index: index, alias: stableAlias}
		}()
	}
	workers.Wait()
	close(results)

	aliases := make([]string, scopeCount)
	for result := range results {
		if result.err != nil {
			t.Errorf("scope %03d: %v", result.index, result.err)
			continue
		}
		aliases[result.index] = result.alias
	}
	if t.Failed() {
		return
	}

	snapshot := service.Snapshot()
	if snapshot.ScopesActive != scopeCount || snapshot.Entries != 2*scopeCount || snapshot.RequestsInFlight != 0 {
		t.Fatalf("parallel exact-cap snapshot=%+v, want scopes=100 entries=200 in_flight=0", snapshot)
	}
	for index := range scopeCount {
		entries, err := service.TriageCapability().InspectScope(fmt.Sprintf("parallel-%03d", index))
		if err != nil || len(entries) != 1 || entries[0].Synthetic != aliases[index] {
			t.Fatalf("scope %03d entries=%+v err=%v alias=%q", index, entries, err, aliases[index])
		}
	}

	// Hold an active lease at capacity. A rejected 101st scope must not evict
	// that request or any retained mapping.
	activeState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "parallel-000"})
	activeCtx := privacy.WithRequestState(context.Background(), activeState)
	activeReq := &canonical.ChatRequest{System: "10.0.0.7"}
	if _, err := service.Before(activeCtx, activeReq); err != nil {
		t.Fatalf("active Before: %v", err)
	}
	rejectedState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "parallel-rejected"})
	_, rejectedErr := service.Before(privacy.WithRequestState(context.Background(), rejectedState), &canonical.ChatRequest{System: "10.200.1.1"})
	assertPrivacyCode(t, rejectedErr, privacy.CodeCapacityExceeded)
	if got := service.Snapshot().RequestsInFlight; got != 1 {
		t.Fatalf("capacity pressure evicted active lease; in_flight=%d", got)
	}
	if err := service.After(activeCtx, activeReq, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("active After: %v", err)
	}

	// A valid alias from another scope is not an authorization capability.
	crossState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "parallel-001"})
	crossCtx := privacy.WithRequestState(context.Background(), crossState)
	crossReq := &canonical.ChatRequest{System: "safe"}
	if _, err := service.Before(crossCtx, crossReq); err != nil {
		t.Fatalf("cross-scope Before: %v", err)
	}
	crossResp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText, Text: aliases[0],
	}}}}
	assertPrivacyCode(t, service.After(crossCtx, crossReq, crossResp), privacy.CodeOutputBlocked)

	if result := service.TriageCapability().ClearAllScopes(); result != privacy.ClearCompleted {
		t.Fatalf("ClearAllScopes=%q, want completed", result)
	}
	cleared := service.Snapshot()
	if cleared.ScopesActive != 0 || cleared.Entries != 0 || cleared.RequestsInFlight != 0 {
		t.Fatalf("post-clear snapshot=%+v, want eventual zero", cleared)
	}

	t.Run("expiry_keeps_active_scope_then_reaches_zero", func(t *testing.T) {
		expiring := newLifecycleStressService(t, 20*time.Millisecond, 4, 2, 8)
		state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "expiry-active"})
		ctx := privacy.WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{System: "10.99.1.7"}
		if _, err := expiring.Before(ctx, req); err != nil {
			t.Fatalf("Before: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
		if snapshot := expiring.Snapshot(); snapshot.ScopesActive != 1 || snapshot.RequestsInFlight != 1 {
			t.Fatalf("active scope expired or evicted: %+v", snapshot)
		}
		if err := expiring.After(ctx, req, &canonical.ChatResponse{}); err != nil {
			t.Fatalf("After: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			snapshot := expiring.Snapshot()
			if snapshot.ScopesActive == 0 && snapshot.Entries == 0 && snapshot.RequestsInFlight == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("expiry did not reach zero: %+v", snapshot)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("per_scope_cap_is_exact", func(t *testing.T) {
		capped := newLifecycleStressService(t, time.Hour, 2, 2, 4)
		firstState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "per-scope-cap"})
		firstCtx := privacy.WithRequestState(context.Background(), firstState)
		firstReq := &canonical.ChatRequest{System: "10.88.1.7"}
		if _, err := capped.Before(firstCtx, firstReq); err != nil {
			t.Fatalf("first Before: %v", err)
		}
		if err := capped.After(firstCtx, firstReq, &canonical.ChatResponse{}); err != nil {
			t.Fatalf("first After: %v", err)
		}
		secondState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "per-scope-cap"})
		_, err := capped.Before(
			privacy.WithRequestState(context.Background(), secondState),
			&canonical.ChatRequest{System: "10.88.2.7"},
		)
		assertPrivacyCode(t, err, privacy.CodeCapacityExceeded)
		if snapshot := capped.Snapshot(); snapshot.ScopesActive != 1 || snapshot.Entries != 2 || snapshot.RequestsInFlight != 0 {
			t.Fatalf("per-scope rejected reservation changed exact cap: %+v", snapshot)
		}
	})
}

type concurrentLifecycleEvidence struct {
	requestCount       int
	scopeCount         int
	inspectWhileActive bool
	clearReturnedClose bool
	reapWhileActive    bool
	activeNotEvicted   bool
	eventualZero       bool
}

func assertConcurrentLifecycleEvidence(t *testing.T, evidence concurrentLifecycleEvidence) {
	t.Helper()
	if evidence.requestCount < 500 || evidence.scopeCount < 100 ||
		!evidence.inspectWhileActive || !evidence.clearReturnedClose ||
		!evidence.reapWhileActive || !evidence.activeNotEvicted || !evidence.eventualZero {
		t.Fatalf("concurrent lifecycle evidence incomplete: %+v", evidence)
	}
}

type controlledLifecycleClock struct {
	mu     sync.Mutex
	now    time.Time
	ticker *controlledLifecycleTicker
}

type controlledLifecycleTicker struct {
	ch chan time.Time
}

func newControlledLifecycleClock() *controlledLifecycleClock {
	return &controlledLifecycleClock{now: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *controlledLifecycleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *controlledLifecycleClock) NewTicker(time.Duration) privacy.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ticker = &controlledLifecycleTicker{ch: make(chan time.Time, 1)}
	return c.ticker
}

func (c *controlledLifecycleClock) advanceWithoutTick(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func (c *controlledLifecycleClock) tick() {
	c.mu.Lock()
	now := c.now
	ticker := c.ticker
	c.mu.Unlock()
	if ticker != nil {
		ticker.ch <- now
	}
}

func (t *controlledLifecycleTicker) C() <-chan time.Time { return t.ch }
func (t *controlledLifecycleTicker) Stop()               {}

type concurrentLiveRequest struct {
	ctx   context.Context
	req   *canonical.ChatRequest
	alias string
	err   error
}

func runConcurrentLifecycleStress(t *testing.T) concurrentLifecycleEvidence {
	t.Helper()
	const (
		scopeCount       = 100
		requestsPerScope = 5
		requestCount     = scopeCount * requestsPerScope
	)

	clock := newControlledLifecycleClock()
	expired := make(chan struct{}, 1)
	service := newLifecycleStressServiceWith(t, time.Minute, 101, 2, 202, clock, privacy.Observers{
		ScopeEvent: func(event string) {
			if event == "expired" {
				select {
				case expired <- struct{}{}:
				default:
				}
			}
		},
	})

	victimState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "concurrent-expiry-victim"})
	victimCtx := privacy.WithRequestState(context.Background(), victimState)
	victimReq := &canonical.ChatRequest{System: "10.250.1.7"}
	if _, err := service.Before(victimCtx, victimReq); err != nil {
		t.Fatalf("expiry victim Before: %v", err)
	}
	if err := service.After(victimCtx, victimReq, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("expiry victim After: %v", err)
	}
	clock.advanceWithoutTick(2 * time.Minute)

	releaseAfter := make(chan struct{})
	ready := make(chan concurrentLiveRequest, requestCount)
	finished := make(chan error, requestCount)
	for scopeIndex := range scopeCount {
		for range requestsPerScope {
			go func() {
				scope := fmt.Sprintf("concurrent-%03d", scopeIndex)
				original := fmt.Sprintf("10.1.%d.7", scopeIndex)
				state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
				ctx := privacy.WithRequestState(context.Background(), state)
				req := &canonical.ChatRequest{System: original}
				_, beforeErr := service.Before(ctx, req)
				live := concurrentLiveRequest{ctx: ctx, req: req, alias: firstBenchmarkIPv4(req.System), err: beforeErr}
				ready <- live
				if beforeErr != nil {
					finished <- beforeErr
					return
				}
				<-releaseAfter
				response := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText, Text: live.alias,
				}}}}
				afterErr := service.After(ctx, req, response)
				if afterErr == nil && response.Message.Content[0].Text != original {
					afterErr = errors.New("reversible response mismatch")
				}
				finished <- afterErr
			}()
		}
	}

	setupFailed := false
	for range requestCount {
		live := <-ready
		if live.err != nil || live.alias == "" {
			setupFailed = true
		}
	}
	if setupFailed {
		close(releaseAfter)
		for range requestCount {
			<-finished
		}
		t.Fatal("concurrent lifecycle request setup failed")
	}
	full := service.Snapshot()
	if full.ScopesActive != 101 || full.RequestsInFlight != requestCount || full.Entries != 202 ||
		full.MaxScopes != 101 || full.MaxEntriesPerScope != 2 || full.MaxTotalEntries != 202 {
		close(releaseAfter)
		for range requestCount {
			<-finished
		}
		t.Fatalf("concurrent exact-cap snapshot has scopes=%d in_flight=%d entries=%d caps=(%d,%d,%d)",
			full.ScopesActive, full.RequestsInFlight, full.Entries,
			full.MaxScopes, full.MaxEntriesPerScope, full.MaxTotalEntries)
	}

	startOperations := make(chan struct{})
	inspectResult := make(chan bool, 1)
	clearResult := make(chan privacy.ClearResult, 1)
	reapTriggered := make(chan struct{}, 1)
	go func() {
		<-startOperations
		entries, err := service.TriageCapability().InspectScope("concurrent-001")
		inspectResult <- err == nil && len(entries) == 1 && entries[0].Entity == "IPv4"
	}()
	go func() {
		<-startOperations
		result, err := service.TriageCapability().ClearScope("concurrent-000")
		if err != nil {
			clearResult <- ""
			return
		}
		clearResult <- result
	}()
	go func() {
		<-startOperations
		clock.tick()
		reapTriggered <- struct{}{}
	}()
	close(startOperations)

	inspected := <-inspectResult
	cleared := <-clearResult
	<-reapTriggered
	select {
	case <-expired:
	case <-time.After(2 * time.Second):
		close(releaseAfter)
		for range requestCount {
			<-finished
		}
		t.Fatal("controlled concurrent reaper did not report the idle expiry")
	}
	whileActive := service.Snapshot()
	activeNotEvicted := whileActive.ScopesActive == scopeCount &&
		whileActive.RequestsInFlight == requestCount && whileActive.Entries == 2*scopeCount

	closedState := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "concurrent-000"})
	_, closedErr := service.Before(
		privacy.WithRequestState(context.Background(), closedState),
		&canonical.ChatRequest{System: "10.1.0.7"},
	)
	closingDenied := false
	var privacyErr *privacy.Error
	if errors.As(closedErr, &privacyErr) && privacyErr.Code == privacy.CodeScopeClosed {
		closingDenied = true
	}

	close(releaseAfter)
	requestsFinished := true
	for range requestCount {
		if err := <-finished; err != nil {
			requestsFinished = false
		}
	}
	if result := service.TriageCapability().ClearAllScopes(); result != privacy.ClearCompleted {
		t.Fatalf("post-concurrency ClearAllScopes=%q, want completed", result)
	}
	zero := service.Snapshot()
	eventualZero := zero.ScopesActive == 0 && zero.RequestsInFlight == 0 && zero.Entries == 0

	return concurrentLifecycleEvidence{
		requestCount: requestCount, scopeCount: scopeCount,
		inspectWhileActive: inspected,
		clearReturnedClose: cleared == privacy.ClearClosing && closingDenied,
		reapWhileActive:    activeNotEvicted,
		activeNotEvicted:   activeNotEvicted && requestsFinished,
		eventualZero:       eventualZero,
	}
}

func newLifecycleStressService(t *testing.T, ttl time.Duration, maxScopes, maxPerScope, maxTotal int) *privacy.Service {
	t.Helper()
	return newLifecycleStressServiceWith(t, ttl, maxScopes, maxPerScope, maxTotal, nil, privacy.Observers{})
}

func newLifecycleStressServiceWith(
	t *testing.T,
	ttl time.Duration,
	maxScopes, maxPerScope, maxTotal int,
	clock privacy.Clock,
	observers privacy.Observers,
) *privacy.Service {
	t.Helper()
	service, err := privacy.NewService(privacy.Config{
		DefaultProfile: privacy.ProfileStrict, RequestProfiles: []privacy.Profile{privacy.ProfileStandard, privacy.ProfileStrict},
		AliasKey: []byte("task17-lifecycle-alias-key-32bytes"), SecretAction: privacy.ActionReplace,
		TechnicalAction: privacy.ActionPseudonymize, ScopeTTL: ttl,
		MaxScopes: maxScopes, MaxEntriesPerScope: maxPerScope, MaxTotalEntries: maxTotal,
		PIIEnabled: true, PIIMode: privacy.ActionReplace, Recognizers: []string{"IPv4"},
		Classifier:       pii.NewPIIClassifier(pii.Recognizers, []string{"IPv4"}, false),
		SecretClassifier: privacy.NewSecretClassifier(),
		Clock:            clock,
		Observers:        observers,
	})
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func assertPrivacyCode(t *testing.T, err error, code string) {
	t.Helper()
	var privacyErr *privacy.Error
	if !errors.As(err, &privacyErr) || privacyErr.Code != code {
		t.Fatalf("error=%v, want privacy code %q", err, code)
	}
}
