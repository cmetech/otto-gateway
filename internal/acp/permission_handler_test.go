// Whitebox tests for the session/request_permission handler (Track 3a).
// D-18: whitebox package gives access to unexported handleNotification,
// activeStream, streamMu, and the Stream deny-flag helpers.
package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// permissionResponseWire is the typed shape of the JSON-RPC response the
// permission handler writes directly via c.framer.writeFrame.
type permissionResponseWire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  struct {
		OptionID string `json:"optionId"`
		Granted  bool   `json:"granted"`
	} `json:"result"`
}

// cancelNotificationWire is the typed shape of the session/cancel
// notification Client.Cancel sends.
type cancelNotificationWire struct {
	Method string `json:"method"`
	Params struct {
		SessionID string `json:"sessionId"`
	} `json:"params"`
}

// readPermissionResponse reads and decodes one line off mock.serverRead as a
// permission response. Fails the test on timeout or decode error.
func readPermissionResponse(t *testing.T, mock *mockRWC) permissionResponseWire {
	t.Helper()
	line, err := readLineFromPipeWithTimeout(mock.serverRead, 2*time.Second)
	if err != nil {
		t.Fatalf("read permission response: %v", err)
	}
	var resp permissionResponseWire
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal permission response: %v (line=%s)", err, line)
	}
	return resp
}

// newDenyStream builds a Stream with denyBuiltinTools=true and publishes it
// as c's activeStream, mirroring what Client.Prompt does before a turn with
// caller-supplied tools.
func newDenyStream(c *Client, sessionID string) {
	st := newStream(context.Background(), sessionID)
	st.setDenyBuiltinTools(true)
	c.streamMu.Lock()
	c.activeStream = st
	c.streamMu.Unlock()
}

// TestPermission_GrantsWhenNoDenyFlag is a regression guard: with no active
// stream, or an active stream that does not request denial, the handler must
// keep auto-granting {"optionId":"allow_always","granted":true} exactly as
// before Track 3a.
func TestPermission_GrantsWhenNoDenyFlag(t *testing.T) {
	t.Run("no_active_stream", func(t *testing.T) {
		mock := newMockRWC()
		cfg := newTestConfig(t)
		c := NewWithConn(mock, cfg)
		defer func() {
			mock.serverClose()
			_ = c.Close()
			goleak.VerifyNone(t)
		}()

		// handleNotification's permission-response write is synchronous and
		// blocks on the io.Pipe until read — run it on its own goroutine
		// (mirroring readLoop) so the test goroutine can read the response.
		go c.handleNotification(rpcFrame{
			Method: "session/request_permission",
			ID:     json.RawMessage("1"),
			Params: json.RawMessage(`{"requestId":"req-1"}`),
		})

		resp := readPermissionResponse(t, mock)
		if resp.Result.OptionID != "allow_always" {
			t.Errorf("optionId = %q, want allow_always", resp.Result.OptionID)
		}
		if !resp.Result.Granted {
			t.Errorf("granted = false, want true")
		}
		if string(resp.ID) != "1" {
			t.Errorf("id = %s, want 1 (verbatim echo)", resp.ID)
		}
	})

	t.Run("deny_flag_false", func(t *testing.T) {
		mock := newMockRWC()
		cfg := newTestConfig(t)
		c := NewWithConn(mock, cfg)
		defer func() {
			mock.serverClose()
			_ = c.Close()
			goleak.VerifyNone(t)
		}()

		st := newStream(context.Background(), "sid-grant")
		st.setDenyBuiltinTools(false)
		c.streamMu.Lock()
		c.activeStream = st
		c.streamMu.Unlock()

		go c.handleNotification(rpcFrame{
			Method: "session/request_permission",
			ID:     json.RawMessage(`"str-id-2"`),
			Params: json.RawMessage(`{"requestId":"req-2"}`),
		})

		resp := readPermissionResponse(t, mock)
		if resp.Result.OptionID != "allow_always" {
			t.Errorf("optionId = %q, want allow_always", resp.Result.OptionID)
		}
		if !resp.Result.Granted {
			t.Errorf("granted = false, want true")
		}
		if string(resp.ID) != `"str-id-2"` {
			t.Errorf("id = %s, want \"str-id-2\" (verbatim echo)", resp.ID)
		}
	})
}

// TestPermission_DeniesWhenDenyFlag verifies the deny branch: an active
// stream with denyBuiltinTools=true must reject kiro's built-in-tool
// permission request using pickRejectOption's selection over the options
// kiro offered, and denialCount must increment turn-over-turn.
func TestPermission_DeniesWhenDenyFlag(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)
	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	newDenyStream(c, "sid-deny")

	params := json.RawMessage(`{
		"requestId": "req-3",
		"options": [
			{"optionId": "reject_always", "kind": "reject_always"},
			{"optionId": "allow_once", "kind": "allow"}
		],
		"toolCall": {"title": "fs_write"}
	}`)

	go c.handleNotification(rpcFrame{
		Method: "session/request_permission",
		ID:     json.RawMessage("3"),
		Params: params,
	})

	resp := readPermissionResponse(t, mock)
	if resp.Result.OptionID != "reject_always" {
		t.Errorf("optionId = %q, want reject_always", resp.Result.OptionID)
	}
	if resp.Result.Granted {
		t.Errorf("granted = true, want false")
	}
	if string(resp.ID) != "3" {
		t.Errorf("id = %s, want 3 (verbatim echo)", resp.ID)
	}

	// Denial #2: a second built-in-tool request in the same turn must also
	// be denied (denialCount is internal state on the Stream — this proves
	// it kept incrementing rather than resetting or wedging after the first
	// denial).
	go c.handleNotification(rpcFrame{
		Method: "session/request_permission",
		ID:     json.RawMessage("4"),
		Params: params,
	})
	resp2 := readPermissionResponse(t, mock)
	if resp2.Result.OptionID != "reject_always" {
		t.Errorf("2nd optionId = %q, want reject_always", resp2.Result.OptionID)
	}
	if resp2.Result.Granted {
		t.Errorf("2nd granted = true, want false")
	}
}

// TestPermission_CircuitBreakerCancels verifies the MAX_TOOL_DENIALS circuit
// breaker: once the per-turn denial count reaches cfg.MaxToolDenials, the
// handler must cancel the turn via a session/cancel notification AFTER the
// deny response for the triggering request has been written (response-first-
// then-cancel).
func TestPermission_CircuitBreakerCancels(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)
	cfg.MaxToolDenials = 2
	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	newDenyStream(c, "sid-breaker")

	params := json.RawMessage(`{
		"requestId": "req-b",
		"options": [{"optionId": "reject_always", "kind": "reject_always"}],
		"toolCall": {"title": "shell_exec"}
	}`)

	// First denial: must NOT trip the breaker yet.
	go c.handleNotification(rpcFrame{
		Method: "session/request_permission",
		ID:     json.RawMessage("10"),
		Params: params,
	})
	resp1 := readPermissionResponse(t, mock)
	if resp1.Result.Granted {
		t.Fatalf("1st response granted = true, want false")
	}

	// Second denial: reaches MaxToolDenials=2 — response is written first...
	go c.handleNotification(rpcFrame{
		Method: "session/request_permission",
		ID:     json.RawMessage("11"),
		Params: params,
	})
	resp2 := readPermissionResponse(t, mock)
	if resp2.Result.Granted {
		t.Fatalf("2nd response granted = true, want false")
	}

	// ...then a session/cancel notification for the same session follows.
	line, err := readLineFromPipeWithTimeout(mock.serverRead, 2*time.Second)
	if err != nil {
		t.Fatalf("read session/cancel notification: %v", err)
	}
	var notif cancelNotificationWire
	if err := json.Unmarshal(line, &notif); err != nil {
		t.Fatalf("unmarshal session/cancel: %v (line=%s)", err, line)
	}
	if notif.Method != "session/cancel" {
		t.Fatalf("method = %q, want session/cancel", notif.Method)
	}
	if notif.Params.SessionID != "sid-breaker" {
		t.Fatalf("sessionId = %q, want sid-breaker", notif.Params.SessionID)
	}
}

// TestPermission_CircuitBreakerDefaultsWhenUnset verifies the defensive
// default: a zero-valued MaxToolDenials (Config field unset) must NOT
// disable the breaker — it must fall back to 4, not "never fire".
func TestPermission_CircuitBreakerDefaultsWhenUnset(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t) // MaxToolDenials left at zero value
	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	newDenyStream(c, "sid-default")

	params := json.RawMessage(`{
		"requestId": "req-d",
		"options": [{"optionId": "reject_always", "kind": "reject_always"}],
		"toolCall": {"title": "shell_exec"}
	}`)

	for i := 0; i < 4; i++ {
		go c.handleNotification(rpcFrame{
			Method: "session/request_permission",
			ID:     json.RawMessage(`5`),
			Params: params,
		})
		resp := readPermissionResponse(t, mock)
		if resp.Result.Granted {
			t.Fatalf("denial #%d granted = true, want false", i+1)
		}
	}

	// The 4th denial must trip the default-4 breaker.
	line, err := readLineFromPipeWithTimeout(mock.serverRead, 2*time.Second)
	if err != nil {
		t.Fatalf("read session/cancel notification: %v", err)
	}
	var notif cancelNotificationWire
	if err := json.Unmarshal(line, &notif); err != nil {
		t.Fatalf("unmarshal session/cancel: %v (line=%s)", err, line)
	}
	if notif.Method != "session/cancel" {
		t.Fatalf("method = %q, want session/cancel", notif.Method)
	}
}

// TestPermission_NoIDDropped confirms the D-20 no-id guard is unchanged: a
// permission "request" with no id cannot be responded to, so the handler
// must log and return without writing anything to the wire.
func TestPermission_NoIDDropped(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)
	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	// No ID present -> the early-return guard fires; no framer write occurs,
	// so this call cannot block and is safe to run synchronously.
	c.handleNotification(rpcFrame{
		Method: "session/request_permission",
		Params: json.RawMessage(`{"requestId":"req-noid"}`),
	})

	if _, err := readLineFromPipeWithTimeout(mock.serverRead, 200*time.Millisecond); err == nil {
		t.Fatalf("expected no response written for a no-id permission request")
	}
}
