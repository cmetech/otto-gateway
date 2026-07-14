// Whitebox unit tests for the _kiro.dev/metadata + _kiro.dev/mcp/* capture
// added for the kiro usage-metrics parity build. Package acp (not acp_test)
// so the test can drive c.handleNotification directly with crafted frames and
// observe the OnTurnMeter/OnContextPct/OnMCPInit Config hooks.
package acp

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

// newMetadataTestClient builds a bare *Client (no subprocess) wired with the
// supplied metadata hooks. The RWC is a discard pipe; the readLoop/writerLoop
// are irrelevant — the test calls handleNotification synchronously.
func newMetadataTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := NewWithConn(newMockRWC(), cfg)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestHandleNotification_MetadataContextPctOnly: a _kiro.dev/metadata frame
// carrying only contextUsagePercentage fires OnContextPct with the 0–100 value
// and does NOT fire OnTurnMeter (no metering = mid-turn frame).
func TestHandleNotification_MetadataContextPctOnly(t *testing.T) {
	var gotPct float64
	var pctFired bool
	var turnFired bool
	c := newMetadataTestClient(t, Config{
		OnContextPct: func(pct float64) { gotPct = pct; pctFired = true },
		OnTurnMeter:  func(_ float64, _ int64) { turnFired = true },
	})

	c.handleNotification(rpcFrame{
		Method: "_kiro.dev/metadata",
		Params: json.RawMessage(`{"sessionId":"s1","contextUsagePercentage":4.897}`),
	})

	if !pctFired {
		t.Fatal("OnContextPct did not fire for a contextUsagePercentage frame")
	}
	if gotPct != 4.897 {
		t.Errorf("OnContextPct pct = %v, want 4.897", gotPct)
	}
	if turnFired {
		t.Error("OnTurnMeter fired on a metering-less frame — should only fire on turn completion")
	}
}

// TestHandleNotification_MetadataTurnComplete: a turn-completion frame carrying
// meteringUsage + turnDurationMs fires OnTurnMeter with credits summed over
// unit==credit entries and the turn duration, plus OnContextPct for the ctx.
func TestHandleNotification_MetadataTurnComplete(t *testing.T) {
	var gotCredits float64
	var gotTurnMs int64
	var turnFired bool
	var gotPct float64
	c := newMetadataTestClient(t, Config{
		OnContextPct: func(pct float64) { gotPct = pct },
		OnTurnMeter:  func(credits float64, turnMs int64) { gotCredits = credits; gotTurnMs = turnMs; turnFired = true },
	})

	c.handleNotification(rpcFrame{
		Method: "_kiro.dev/metadata",
		Params: json.RawMessage(`{"sessionId":"s1","contextUsagePercentage":1.567,` +
			`"meteringUsage":[{"value":0.0287,"unit":"credit","unitPlural":"credits"},` +
			`{"value":9.0,"unit":"token","unitPlural":"tokens"}],"turnDurationMs":2012}`),
	})

	if !turnFired {
		t.Fatal("OnTurnMeter did not fire for a turn-completion frame")
	}
	// Only the credit-unit entry counts; the token entry is excluded.
	if gotCredits != 0.0287 {
		t.Errorf("OnTurnMeter credits = %v, want 0.0287 (credit units only)", gotCredits)
	}
	if gotTurnMs != 2012 {
		t.Errorf("OnTurnMeter turnMs = %d, want 2012", gotTurnMs)
	}
	if gotPct != 1.567 {
		t.Errorf("OnContextPct pct = %v, want 1.567 on the turn-completion frame", gotPct)
	}
}

// TestHandleNotification_MCPInit: the two MCP methods fire OnMCPInit with the
// serverName and ok/fail result.
func TestHandleNotification_MCPInit(t *testing.T) {
	type call struct {
		server string
		ok     bool
	}
	var calls []call
	c := newMetadataTestClient(t, Config{
		OnMCPInit: func(server string, ok bool) { calls = append(calls, call{server, ok}) },
	})

	c.handleNotification(rpcFrame{
		Method: "_kiro.dev/mcp/server_initialized",
		Params: json.RawMessage(`{"serverName":"filesystem"}`),
	})
	c.handleNotification(rpcFrame{
		Method: "_kiro.dev/mcp/server_init_failure",
		Params: json.RawMessage(`{"serverName":"broken"}`),
	})

	if len(calls) != 2 {
		t.Fatalf("OnMCPInit fired %d times, want 2", len(calls))
	}
	if calls[0] != (call{"filesystem", true}) {
		t.Errorf("first MCP init = %+v, want {filesystem true}", calls[0])
	}
	if calls[1] != (call{"broken", false}) {
		t.Errorf("second MCP init = %+v, want {broken false}", calls[1])
	}
}

// TestHandleNotification_MetadataMalformedDropped: a malformed metadata frame
// is dropped (no hook fires, no panic).
func TestHandleNotification_MetadataMalformedDropped(t *testing.T) {
	fired := false
	c := newMetadataTestClient(t, Config{
		OnContextPct: func(_ float64) { fired = true },
		OnTurnMeter:  func(_ float64, _ int64) { fired = true },
	})

	c.handleNotification(rpcFrame{
		Method: "_kiro.dev/metadata",
		Params: json.RawMessage(`{"contextUsagePercentage":"not-a-number"}`),
	})

	if fired {
		t.Error("a malformed metadata frame must not fire any hook")
	}
}
