package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"loop24-gateway/internal/canonical"
	"loop24-gateway/internal/version"
)

// ErrClientClosed is returned to all in-flight callers when Close() is called or
// when the readLoop encounters EOF (subprocess exited). Callers use errors.Is to
// distinguish this from a request-level RPC error.
var ErrClientClosed = errors.New("acp: client closed")

// ErrSessionClosed is returned when a Prompt call finds the session already closed.
var ErrSessionClosed = errors.New("acp: session closed")

// ErrSubprocessExited is returned when the subprocess exits unexpectedly.
var ErrSubprocessExited = errors.New("acp: subprocess exited")

// Config holds all configuration for the ACP client.
// Required field: Logger. All others have documented defaults.
// D-05: Config-struct constructor, not functional options — matches stdlib pattern.
type Config struct {
	// Logger is required; all ACP client events are structured-logged here.
	Logger *slog.Logger
	// Command is the kiro-cli binary path or name (default "kiro-cli").
	Command string
	// Args are the arguments passed to kiro-cli (default ["acp"]).
	Args []string
	// Cwd is the working directory for the kiro-cli subprocess.
	Cwd string
	// Env holds additional environment variables appended to os.Environ().
	Env []string
	// PingInterval is the heartbeat interval (default 60s).
	PingInterval time.Duration
}

// applyDefaults fills in zero-value Config fields with documented defaults.
func (c *Config) applyDefaults() {
	if c.Command == "" {
		c.Command = "kiro-cli"
	}
	if len(c.Args) == 0 {
		c.Args = []string{"acp"}
	}
	if c.PingInterval == 0 {
		c.PingInterval = 60 * time.Second
	}
}

// rpcRequest is the outbound JSON-RPC 2.0 request envelope.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// rpcNotification is the outbound JSON-RPC 2.0 notification envelope (no id).
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// initializeParams is the params payload for the initialize request.
type initializeParams struct {
	ClientInfo   clientInfo   `json:"clientInfo"`
	Capabilities capabilities `json:"capabilities"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type capabilities struct{}

// sessionNewParams is the params payload for session/new.
type sessionNewParams struct {
	CWD string `json:"cwd"`
}

// sessionNewResult is the result from session/new.
type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// setModelParams is the params payload for session/set_model.
type setModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// promptParams is the params payload for session/prompt.
type promptParams struct {
	SessionID string           `json:"sessionId"`
	Blocks    []canonical.Block `json:"blocks"`
}

// cancelParams is the params payload for session/cancel notification.
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// pingParams is the params payload for the ping request.
type pingParams struct{}

// Client is the ACP JSON-RPC 2.0 client for kiro-cli.
// It manages one reader goroutine, one writer goroutine, and one ping goroutine.
// All exported methods are safe to call from multiple goroutines.
//
// D-06: dual constructors — New (spawns subprocess) and NewWithConn (accepts RWC).
// D-07: Close() is idempotent; shutdown order documented in the Close method.
type Client struct {
	cfg Config

	framer *framer
	disp   *dispatcher

	wg         sync.WaitGroup
	clientCtx  context.Context    // the client-lifetime context; cancelled by Close()
	cancel     context.CancelFunc
	closeOnce  sync.Once

	// Subprocess path (New constructor).
	stdin io.WriteCloser
	cmd   *exec.Cmd

	// NewWithConn path: rwc is stored so Close() can call rwc.Close() to unblock the scanner.
	rwc io.ReadWriteCloser

	nextID  atomic.Uint64
	writeCh chan []byte // ACP-02: all RPC sends go through this channel; writer goroutine serialises

	// activeStream holds the current in-flight Prompt stream (one at a time in Phase 1).
	// Guarded by streamMu.
	activeStream *Stream
	streamMu     sync.Mutex
}

// New spawns a kiro-cli subprocess and returns a connected Client.
// The subprocess is killed when the client context is cancelled (on Close()).
// ACP-01: uses exec.CommandContext so subprocess is killed on context cancellation.
//
//nolint:gosec // G204: kiro-cli command is env-var config; not user-controlled HTTP input
func New(cfg Config) (*Client, error) {
	cfg.applyDefaults()

	clientCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(clientCtx, cfg.Command, cfg.Args...) //nolint:gosec // G204
	cmd.Dir = cfg.Cwd
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("acp: start %q: %w", cfg.Command, err)
	}

	// For the framer, wrap stdout as reader and stdin as writer.
	type stdinWriter struct{ io.Writer }
	fr := newFramer(stdout, stdinWriter{stdin})

	c := &Client{
		cfg:       cfg,
		framer:    fr,
		clientCtx: clientCtx,
		cancel:    cancel,
		stdin:     stdin,
		cmd:       cmd,
	}
	c.disp = &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: c.handleNotification,
	}
	c.writeCh = make(chan []byte, 16) // buffered to reduce latency for back-to-back RPCs

	c.wg.Add(3)
	go c.readLoop(clientCtx)
	go c.writerLoop(clientCtx)
	go c.pingLoop(clientCtx)

	return c, nil
}

// NewWithConn accepts a pre-built io.ReadWriteCloser (e.g., io.Pipe for tests, or a pool
// connection in Phase 5). No subprocess is spawned; the caller owns subprocess lifecycle.
// D-06: dual constructors; shared internals via newClient pattern.
func NewWithConn(rwc io.ReadWriteCloser, cfg Config) *Client {
	cfg.applyDefaults()

	clientCtx, cancel := context.WithCancel(context.Background())

	c := &Client{
		cfg:       cfg,
		framer:    newFramer(rwc, rwc),
		clientCtx: clientCtx,
		cancel:    cancel,
		rwc:       rwc, // stored so Close() can call rwc.Close() to unblock the scanner
		cmd:       nil,
		stdin:     nil,
	}
	c.disp = &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: c.handleNotification,
	}
	c.writeCh = make(chan []byte, 16)

	c.wg.Add(3)
	go c.readLoop(clientCtx)
	go c.writerLoop(clientCtx)
	go c.pingLoop(clientCtx)

	return c
}

// readLoop reads JSON-RPC frames from the subprocess stdout pipe and routes them.
// On EOF (subprocess exited or stdin closed by Close()), it calls failPending so
// no in-flight caller hangs.
func (c *Client) readLoop(ctx context.Context) {
	defer c.wg.Done()
	defer func() {
		// On any readLoop exit, close the active stream if one exists so callers
		// waiting on Result() don't hang.
		c.streamMu.Lock()
		s := c.activeStream
		c.activeStream = nil
		c.streamMu.Unlock()
		if s != nil {
			s.close(nil, ErrClientClosed)
		}
	}()
	for {
		frame, err := c.framer.readFrame()
		if err != nil {
			// EOF or read error — subprocess exited or pipe closed.
			c.failPending(ErrClientClosed)
			return
		}
		var f rpcFrame
		if err := json.Unmarshal(frame, &f); err != nil {
			c.cfg.Logger.Warn("acp: malformed frame", "err", err)
			continue // log and continue — don't kill session on parse error (T-02-04)
		}
		c.disp.route(f)
		_ = ctx // ctx is passed for future cancellation use; readLoop exits on EOF
	}
}

// writerLoop is the sole goroutine that calls framer.writeFrame.
// All other goroutines send their serialised request bytes to writeCh.
// ACP-02: one reader + one writer goroutine; writeCh serialises all framer writes.
func (c *Client) writerLoop(ctx context.Context) {
	defer c.wg.Done()
	for {
		select {
		case data := <-c.writeCh:
			if err := c.framer.writeFrame(json.RawMessage(data)); err != nil {
				c.cfg.Logger.Error("acp: write error", "err", err)
				return
			}
		case <-ctx.Done():
			// Context cancelled by Close() — drain any buffered items before returning.
			// writeCh is NOT closed here (avoiding race with send()), so we drain by length.
			for len(c.writeCh) > 0 {
				data := <-c.writeCh
				if err := c.framer.writeFrame(json.RawMessage(data)); err != nil {
					c.cfg.Logger.Warn("acp: drain write error", "err", err)
				}
			}
			return
		}
	}
}

// pingLoop sends a periodic ping to detect subprocess health.
// ACP-06: 60s default interval; exits cleanly on Close().
func (c *Client) pingLoop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := c.Ping(pingCtx); err != nil {
				cancel()
				if errors.Is(err, context.Canceled) || errors.Is(err, ErrClientClosed) {
					return // expected on Close()
				}
				c.cfg.Logger.Warn("acp: ping failed", "err", err)
				return
			}
			cancel()
		case <-ctx.Done():
			return
		}
	}
}

// send marshals req and queues it for the writer goroutine.
// If ctx is cancelled before the write is accepted, the pending entry is cancelled.
// Also listens to the client lifetime context (c.clientCtx) to detect Close() — this
// prevents sending to a closed writeCh channel which would panic.
// ACP-02 (REVIEW FIX): all RPC methods use this helper; framer.writeFrame is only
// called by the writer goroutine.
func (c *Client) send(ctx context.Context, id uint64, req any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("acp: marshal request: %w", err)
	}
	select {
	case c.writeCh <- data:
		return nil
	case <-ctx.Done():
		c.disp.cancel(id)
		return fmt.Errorf("acp: send cancelled: %w", ctx.Err())
	case <-c.clientCtx.Done():
		// Client is closing — don't send to a channel that may be closed.
		c.disp.cancel(id)
		return ErrClientClosed
	}
}

// sendNotification marshals a notification and queues it for the writer goroutine.
// Notifications have no id so there is no pending entry to cancel.
// Returns silently if the client is closed or the channel is full.
func (c *Client) sendNotification(notif any) {
	data, err := json.Marshal(notif)
	if err != nil {
		c.cfg.Logger.Warn("acp: marshal notification failed", "err", err)
		return
	}
	select {
	case c.writeCh <- data:
	case <-c.clientCtx.Done():
		// Client closed — drop notification.
	default:
		// writeCh full — drop notification (best-effort for cancel/grant).
	}
}

// failPending drains the dispatcher's pending map, sending err to every waiting caller.
// Called from readLoop on EOF and from Close() so no caller hangs indefinitely.
func (c *Client) failPending(err error) {
	c.disp.drainAll(err)
}

// Initialize sends the JSON-RPC initialize request and waits for a response.
// ACP-03: required before any session/new or ping call.
func (c *Client) Initialize(ctx context.Context) error {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "initialize",
		Params: initializeParams{
			ClientInfo:   clientInfo{Name: "loop24-gateway", Version: version.Version},
			Capabilities: capabilities{},
		},
	}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("acp: initialize: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			if frame.Error.Code == -32099 {
				return ErrClientClosed
			}
			return fmt.Errorf("acp: initialize: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		return nil
	}
}

// NewSession sends session/new with the given working directory and returns the session ID.
// ACP-03.
func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/new",
		Params:  sessionNewParams{CWD: cwd},
	}); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("acp: session/new: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			if frame.Error.Code == -32099 {
				return "", ErrClientClosed
			}
			return "", fmt.Errorf("acp: session/new: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		var result sessionNewResult
		if err := json.Unmarshal(frame.Result, &result); err != nil {
			return "", fmt.Errorf("acp: session/new result: %w", err)
		}
		return result.SessionID, nil
	}
}

// Ping sends a JSON-RPC ping and waits for a response.
// ACP-03: used by pingLoop and integration tests.
func (c *Client) Ping(ctx context.Context) error {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "ping",
		Params:  pingParams{},
	}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("acp: ping: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			if frame.Error.Code == -32099 {
				return ErrClientClosed
			}
			// kiro-cli may respond with method-not-found; treat as non-fatal.
			return nil
		}
		return nil
	}
}

// SetModel sends session/set_model to switch the active model for a session.
// ACP-03.
func (c *Client) SetModel(ctx context.Context, sessionID, modelID string) error {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/set_model",
		Params:  setModelParams{SessionID: sessionID, ModelID: modelID},
	}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("acp: session/set_model: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			if frame.Error.Code == -32099 {
				return ErrClientClosed
			}
			return fmt.Errorf("acp: session/set_model: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		return nil
	}
}

// Prompt sends session/prompt and returns a Stream for receiving chunks.
// The stream receives session/update chunks via handleNotification until the session
// signals it is done (prompt response closes the stream).
// D-03: streaming from day 1.
func (c *Client) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (*Stream, error) {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	stream := newStream(ctx, sessionID)

	// Register the active stream BEFORE sending the prompt so no update is missed.
	c.streamMu.Lock()
	c.activeStream = stream
	c.streamMu.Unlock()

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/prompt",
		Params:  promptParams{SessionID: sessionID, Blocks: blocks},
	}); err != nil {
		c.streamMu.Lock()
		c.activeStream = nil
		c.streamMu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.disp.cancel(id)
		c.streamMu.Lock()
		c.activeStream = nil
		c.streamMu.Unlock()
		// Best-effort cancel notification (no id = notification).
		c.sendNotification(rpcNotification{
			JSONRPC: "2.0",
			Method:  "session/cancel",
			Params:  cancelParams{SessionID: sessionID},
		})
		return nil, fmt.Errorf("acp: prompt: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			c.streamMu.Lock()
			c.activeStream = nil
			c.streamMu.Unlock()
			if frame.Error.Code == -32099 {
				return nil, ErrClientClosed
			}
			return nil, fmt.Errorf("acp: prompt: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		return stream, nil
	}
}

// Cancel sends a session/cancel notification (best-effort; no response expected).
func (c *Client) Cancel(sessionID string) {
	c.sendNotification(rpcNotification{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Params:  cancelParams{SessionID: sessionID},
	})
}

// handleNotification is the dispatcher's onNotif callback.
// Called by the readLoop goroutine for every nil-ID frame.
//
// Handles:
//   - session/request_permission → auto-grant (ACP-04)
//   - session/update / _kiro.dev/session/update → canonical.Chunk → push to activeStream (ACP-05)
func (c *Client) handleNotification(frame rpcFrame) {
	switch frame.Method {
	case "session/request_permission":
		// CRITICAL: Must auto-grant immediately or kiro-cli blocks forever (ACP-04, Pitfall 1).
		// T-02-05: auto-grant uses writeCh (not direct framer call) for serialisation.
		var params permissionParams
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			c.cfg.Logger.Warn("acp: malformed permission request", "err", err)
			return
		}
		grantID := c.nextID.Add(1)
		data, err := json.Marshal(rpcRequest{
			JSONRPC: "2.0",
			ID:      grantID,
			Method:  "session/grant_permission",
			Params: grantParams{
				RequestID: params.RequestID,
				OptionID:  "allow_always", // exact wire name — not optionID
				Granted:   true,
			},
		})
		if err != nil {
			c.cfg.Logger.Warn("acp: marshal grant failed", "err", err)
			return
		}
		select {
		case c.writeCh <- data:
		case <-c.clientCtx.Done():
			// Client closing — drop grant.
		default:
			c.cfg.Logger.Warn("acp: writeCh full, dropping grant_permission")
		}

	case "session/update", "_kiro.dev/session/update":
		// Translate to canonical.Chunk and push to the active stream (ACP-05).
		var update sessionUpdateParams
		if err := json.Unmarshal(frame.Params, &update); err != nil {
			c.cfg.Logger.Warn("acp: malformed session update", "err", err)
			return
		}
		chunk := translateUpdate(update)

		// REVIEW FIX (Codex MEDIUM — activeStream invariant):
		// Acquire the mutex, read activeStream, release. If nil, log Warn and drop.
		c.streamMu.Lock()
		s := c.activeStream
		c.streamMu.Unlock()

		if s == nil {
			c.cfg.Logger.Warn("acp: session/update for unknown session — dropped",
				"method", frame.Method)
			return
		}
		// push with client lifetime context for backpressure (D-03 + REVIEW FIX).
		if err := s.push(c.clientCtx, chunk); err != nil {
			c.cfg.Logger.Warn("acp: stream push failed", "err", err)
		}

	default:
		c.cfg.Logger.Debug("acp: unhandled notification", "method", frame.Method)
	}
}

// Close shuts down the client cleanly.
// D-07: idempotent via sync.Once. Shutdown order is documented below and is mandatory.
// T-02-02: goroutine leak gate covered by goleak.VerifyTestMain.
func (c *Client) Close() error {
	var firstErr error
	c.closeOnce.Do(func() {
		// MANDATORY CLOSE ORDER (D-07 + REVIEW FIX Codex HIGH):
		// 1. cancel() — signal all goroutines to stop via clientCtx.
		//    This also prevents send() from writing to writeCh after step 3.
		c.cancel()

		// 2. Drain pending map — send ErrClientClosed to all waiting callers.
		//    Done early so callers blocked in Initialize/NewSession/Ping/SetModel unblock
		//    and exit their goroutines promptly (before wg.Wait()).
		c.failPending(ErrClientClosed)

		// 3a/3b. Close the I/O source so the readLoop's scanner unblocks.
		if c.cmd != nil {
			// Subprocess path: closing stdin sends EOF → kiro-cli exits → scanner returns EOF.
			if err := c.stdin.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("acp: close stdin: %w", err)
			}
		} else if c.rwc != nil {
			// NewWithConn path: close the injected RWC → scanner unblocks.
			if err := c.rwc.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("acp: close rwc: %w", err)
			}
		}

		// 4. wg.Wait() — wait for readLoop + writerLoop + pingLoop goroutines.
		//    writerLoop exits via ctx.Done() (set in step 1) and drains writeCh.
		c.wg.Wait()

		// 5. cmd.Wait() — reap the subprocess (only if we spawned it).
		if c.cmd != nil {
			if err := c.cmd.Wait(); err != nil && firstErr == nil {
				// Ignore "signal: killed" — that's expected when context kills the process.
				firstErr = fmt.Errorf("acp: cmd wait: %w", err)
			}
		}
	})
	return firstErr
}
