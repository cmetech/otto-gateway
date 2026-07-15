package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/version"
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
	// Now returns the current wall-clock time; defaults to time.Now when nil.
	// Test seam for the ping suspend-guard, which detects a laptop
	// suspend/resume by the wall-clock gap between successive ticks.
	Now func() time.Time
	// OnPingEscalate / OnPingSuspendSkip are optional observability hooks fired
	// by pingLoop when a liveness ping escalates to a worker teardown / is
	// skipped after a detected suspend-resume. The pool wires these to
	// aggregate the gw_acp_ping_* metrics (Track 4b). nil = no-op.
	OnPingEscalate    func()
	OnPingSuspendSkip func()

	// OnTurnMeter / OnContextPct / OnMCPInit are optional kiro-usage hooks
	// fired by handleNotification when kiro emits its `_kiro.dev/metadata` /
	// `_kiro.dev/mcp/*` notifications (kiro usage-metrics parity build). The
	// pool + session registry wire these to a shared metrics recorder (and the
	// registry additionally uses OnContextPct to drive proactive context
	// recycle). Same optional-hook pattern as OnPing* above; nil = no-op.
	//
	//   - OnTurnMeter(credits, turnMs, ctxPct, hasCtxPct) fires once per completed
	//     turn — a metadata frame carrying `meteringUsage` (present, even if
	//     empty). credits is the sum of the credit-unit metering entries; turnMs
	//     is `turnDurationMs` (0 if absent); ctxPct/hasCtxPct carry the same
	//     frame's `contextUsagePercentage` when present, so the consumer can
	//     record the end-of-turn context utilization exactly once per turn.
	//   - OnContextPct(pct) fires on any `contextUsagePercentage` frame
	//     (including mid-turn). pct is on a 0–100 PERCENT scale (kiro's native
	//     scale — NOT 0..1). Used to track the latest per-session context for
	//     proactive recycle; it does NOT drive the metrics histogram (that is
	//     observed once per turn via OnTurnMeter).
	//   - OnMCPInit(server, ok) fires on the two MCP init methods; ok=false for
	//     server_init_failure.
	OnTurnMeter  func(credits float64, turnMs int64, ctxPct float64, hasCtxPct bool)
	OnContextPct func(pct float64)
	OnMCPInit    func(server string, ok bool)

	// OnRawFrame, when set, is fired by readLoop for every inbound kiro frame
	// (after a successful json.Unmarshal, before dispatch) with the frame's
	// method and raw params. Used by the Track 0 capture ring to observe kiro's
	// raw wire behavior. nil = no capture (one nil check on the read path, zero
	// cost when disabled).
	OnRawFrame func(method string, params json.RawMessage)
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
	if c.Now == nil {
		c.Now = time.Now
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

// rpcResponse is the outbound JSON-RPC 2.0 response envelope (no method).
//
// Phase 1.1 D-20: session/request_permission is RESPONDED to on the original
// frame id rather than triggering a new permission-granting request. Without
// this echo, kiro-cli blocks forever waiting for the response to the id it
// sent — Phase 2's first tool-using prompt would deadlock.
//
// Phase 1.1 CR-01: ID is json.RawMessage so it can echo whatever JSON id
// shape arrived on the request (string, number, or null) byte-for-byte.
// Typing this as uint64 silently corrupts string ids — a regression on the
// same D-20 surface this struct is supposed to harden.
//
// Placed inline next to rpcRequest/rpcNotification per CONTEXT.md §Claude's
// Discretion: with only three envelope shapes the splitting threshold from
// PATTERNS.md ("split into rpc.go once there are 3+ envelope shapes") is at
// the boundary — keep them grouped for now; split if a fourth envelope lands.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

// initializeParams is the params payload for the initialize request.
// Phase 1.1 D-08: spec-compliant shape — adds ProtocolVersion and renames
// the Capabilities field to ClientCapabilities (with fs + terminal nested
// flags). Phase 1's empty `capabilities: {}` shape was wrong; kiro-cli
// tolerated it but the live ACP spec requires this shape.
type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientInfo         clientInfo         `json:"clientInfo"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// clientCapabilities declares what reverse RPCs the gateway can service.
// Phase 1.1 D-08: the gateway does NOT implement fs/* or terminal/* reverse
// handlers, so all flags are false. The Node implementation declares true
// because it relays those calls; we don't (yet).
type clientCapabilities struct {
	Fs       fsCapabilities `json:"fs"`
	Terminal bool           `json:"terminal"`
}

// fsCapabilities declares whether the client supports fs/read_text_file and
// fs/write_text_file reverse requests from the agent. Phase 1.1: both false.
type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// initializeResult is the result envelope returned by the initialize call.
// Phase 1.1 D-09: capture agentCapabilities.promptCapabilities so callers can
// inspect the agent's content-block support via PromptCapabilities(). Missing
// or empty promptCapabilities → zero struct (all false); this is defensive
// behaviour required by D-09.
type initializeResult struct {
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
}

// agentCapabilities is the wire shape of the initialize-response capabilities.
type agentCapabilities struct {
	PromptCapabilities promptCapabilitiesWire `json:"promptCapabilities"`
}

// promptCapabilitiesWire is the JSON-tagged twin of canonical.PromptCapabilities.
// Wire shapes live inside internal/acp; canonical types stay JSON-tag-free per
// Phase 1 D-11. Field-by-field translation happens in Initialize.
type promptCapabilitiesWire struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// mcpServer is the wire element type for the session/new params.mcpServers[] array.
// Phase 1.1 has no MCP server fields; this empty struct exists so the wire frame
// can ship an explicit `[]` (not `null`) per D-10 — older kiro-cli versions may
// treat a missing or null mcpServers as an error. Phase 8+ may grow the type.
type mcpServer struct{}

// sessionNewParams is the params payload for session/new.
// Phase 1.1 D-10: include mcpServers as an explicit empty array on the wire
// (initialised via make([]mcpServer, 0) at the call site so json.Marshal renders
// `[]` instead of `null`).
type sessionNewParams struct {
	CWD        string      `json:"cwd"`
	MCPServers []mcpServer `json:"mcpServers"`
}

// sessionNewResult is the result from session/new.
// Phase 1.1 D-11: accept both `sessionId` and `id` (older kiro-cli versions used
// `id`); NewSession picks whichever is non-empty via firstNonEmpty.
// Phase 1.1 D-12: surface result.models.availableModels[] via the canonical
// ModelInfo type so callers see {ID, Name}.
type sessionNewResult struct {
	SessionID string                 `json:"sessionId"`
	ID        string                 `json:"id"`
	Models    sessionNewResultModels `json:"models"`
}

// sessionNewResultModels carries the `models` envelope returned by session/new.
type sessionNewResultModels struct {
	AvailableModels []sessionNewResultModelEntry `json:"availableModels"`
	CurrentModelID  string                       `json:"currentModelId"`
}

// sessionNewResultModelEntry carries a single available-model entry on the wire.
// Wire field is `modelId` (per acp_wire_shapes.md §2), not `id`. Maps to
// canonical.ModelInfo{ID: ModelID, Name: Name} inside NewSession.
type sessionNewResultModelEntry struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}

// setModelParams is the params payload for session/set_model.
type setModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// promptParams is the params payload for session/prompt.
//
// CR-05 fix: the block slice is typed as []wireBlock (not []canonical.Block).
// The canonical type encodes via Go's default reflect encoder, which produces
// a shape kiro-cli cannot parse. translateBlocks (in translate.go) converts the
// caller's canonical slice to the wire shape just before the send.
//
// Phase 1.1 D-13: ship BOTH `prompt` AND `content` carrying the same translated
// slice. The Node implementation does this at acp-ollama-server.js:296-303
// because older kiro-cli versions read the old field name. Keep both fields —
// do not collapse to one. JSON marshal of two []wireBlock fields pointing at
// the same slice serialises each as its own array (no aliasing on the wire).
type promptParams struct {
	SessionID string      `json:"sessionId"`
	Prompt    []wireBlock `json:"prompt"`
	Content   []wireBlock `json:"content"`
}

// promptResult is the result envelope returned by the session/prompt call.
// Phase 1.1 D-07: parse `stopReason` from the wire and surface it as
// canonical.StopReason on Stream.Result() via FinalResult.StopReason.
type promptResult struct {
	StopReason string `json:"stopReason"`
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

	wg        sync.WaitGroup
	clientCtx context.Context // the client-lifetime context; cancelled by Close()
	cancel    context.CancelFunc
	closeOnce sync.Once

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

	// caps and models hold handshake state captured during Initialize/NewSession.
	// Guarded by stateMu (D-06: separate from streamMu so the active-stream
	// critical section stays narrow). models is populated by NewSession (Plan 03);
	// declared here so the struct layout is final.
	stateMu sync.RWMutex
	caps    canonical.PromptCapabilities
	models  []canonical.ModelInfo
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
	// D-18-04 REL-OBSV-03: capture kiro-cli stderr into the gateway's
	// structured logger instead of routing it to os.Stderr (the pre-Phase
	// 18 behavior that bypassed slog and left operators blind when
	// kiro-cli printed warnings or errors). StderrPipe MUST be called
	// BEFORE cmd.Start; the goroutine that drains it is launched AFTER
	// Start so it has a valid cmd.Process.Pid to log. Goroutine exit on
	// pipe close (subprocess exit) is gated by goleak.VerifyTestMain.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stderr pipe: %w", err)
	}
	// 260531-ra6 RA6-02: place the kiro-cli child in its own process
	// group on darwin/linux so exec.CommandContext's SIGKILL-on-ctx-cancel
	// is delivered to the leader (and, via the leader's pgrp, to any
	// grandchildren). No-op on Windows. Build-tagged in
	// pool_pgid_{unix,windows}.go.
	applyPgidAttr(cmd)
	// Audit acp-grandchildren-not-killed-on-close: override
	// exec.CommandContext's default cancel (single SIGKILL to leader PID
	// only) with a pgrp-aware kill so kiro-cli's tool-server children
	// (MCP servers, language servers, file readers) are reaped along
	// with the leader. Without this they reparent to init and survive
	// the gateway. No-op on Windows via the killProcessGroup stub.
	//
	// WaitDelay bounds how long Wait() waits for I/O to drain after
	// the process is signaled before forcibly closing stdio. Two
	// seconds is generous for kiro-cli's shutdown handler.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// SIGKILL is delivered to the whole pgrp (leader + children).
		// Ignore ESRCH so an already-exited subprocess doesn't surface
		// a noisy error on the shutdown path.
		return killProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
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

	// D-18-04 REL-OBSV-03: drain kiro-cli stderr line-by-line into slog.Warn.
	// Tracked in c.wg so Close() waits for it to exit (goleak gate).
	c.wg.Add(1)
	go c.stderrDrainLoop(stderrPipe, cmd.Process.Pid)

	return c, nil
}

// stderrDrainLoop reads kiro-cli stderr until EOF (subprocess exit / pipe
// close) and emits each non-empty line as a slog.Warn record. Implements
// D-18-04 REL-OBSV-03.
//
// Uses bufio.Reader.ReadString('\n') — NOT bufio.Scanner — because Scanner
// silently stops on bufio.ErrTooLong, dropping every line after the
// oversized one (including the one we still want to see). ReadString
// keeps going; the per-line byte cap is applied here after read.
//
// Goroutine exit contract:
//   - Loop exits when ReadString returns a non-nil error (io.EOF on
//     normal subprocess exit, *os.PathError on premature pipe close).
//   - Deferred pipe.Close belt-and-braces in case ReadString returns
//     a wrapped error before EOF.
//   - c.wg.Done at the tail so Close()'s wg.Wait() observes the exit.
func (c *Client) stderrDrainLoop(pipe io.ReadCloser, pid int) {
	defer c.wg.Done()
	defer func() { _ = pipe.Close() }()

	const maxLineBytes = 1024 * 1024 // 1 MB hard cap per logged line
	reader := bufio.NewReader(pipe)
	for {
		line, rerr := reader.ReadString('\n')
		if len(line) > 0 {
			// Trim trailing \n (and a preceding \r from CRLF-terminated
			// shells). Empty after trimming → skip (blank stderr line).
			trimmed := strings.TrimRight(line, "\n")
			trimmed = strings.TrimRight(trimmed, "\r")
			if trimmed != "" {
				droppedBytes := 0
				if len(trimmed) > maxLineBytes {
					// WR-04: slice on a UTF-8 rune boundary. A naive
					// trimmed[:maxLineBytes] can split a multi-byte
					// rune (kiro-cli stderr is UTF-8 by default in Go),
					// producing invalid UTF-8 in the slog "line" field.
					// Downstream JSON encoders / Splunk / ELK pipelines
					// reject or garble such records — precisely on the
					// most-important diagnostic line. Walk back from
					// the cap until we land on a rune-start byte; cost
					// is at most utf8.UTFMax-1 (3) byte-walks. The
					// resulting trim is "≤ maxLineBytes on UTF-8-safe
					// boundary", not a strict byte-cap.
					originalLen := len(trimmed)
					n := maxLineBytes
					for n > 0 && !utf8.RuneStart(trimmed[n]) {
						n--
					}
					trimmed = trimmed[:n]
					// WR-09: record bytes dropped so the operator has
					// telemetry on the truncation. The pathological
					// "all continuation bytes" case (n==0) drops up to
					// maxLineBytes bytes and would otherwise surface as
					// a silent empty `line` WARN with no diagnostic.
					// The "well-formed UTF-8 just past 1MB" case drops
					// only `originalLen - maxLineBytes` bytes (plus up
					// to 3 walk-back bytes). Either way, including
					// dropped_bytes lets operators distinguish an
					// actual empty stderr line from a corrupted-encoding
					// payload eaten by the cap.
					droppedBytes = originalLen - n
				}
				if c.cfg.Logger != nil {
					// WR-09: include dropped_bytes (and truncated: true)
					// only when the cap fired. The base field set
					// (worker_pid, line) is preserved byte-exact per
					// D-18-04 contract; new fields are additive.
					if droppedBytes > 0 {
						c.cfg.Logger.Warn(
							"kiro-cli stderr",
							"worker_pid", pid,
							"line", trimmed,
							"truncated", true,
							"dropped_bytes", droppedBytes,
						)
					} else {
						c.cfg.Logger.Warn(
							"kiro-cli stderr",
							"worker_pid", pid,
							"line", trimmed,
						)
					}
				}
			}
		}
		if rerr != nil {
			// io.EOF on subprocess exit; other errors on pipe close races.
			// Either way the goroutine has nothing left to do.
			return
		}
	}
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
	// Belt-and-suspenders panic guard. The primary fix for send-on-closed
	// races lives in Stream.push/close (sendMu serialization). This recover
	// exists so a future regression in dispatch/translation code cannot
	// crash the whole gateway from this goroutine — net/http's per-handler
	// recover does not cover readLoop.
	defer func() {
		if r := recover(); r != nil {
			c.cfg.Logger.Error("acp: readLoop panic recovered",
				"err", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()))
			c.failPending(ErrClientClosed)
		}
	}()
	// CR-03 fix: propagate readLoop death to clientCtx. If the subprocess
	// crashes before Close() is called, clientCtx must be cancelled so any
	// subsequent caller of send() unblocks with ErrClientClosed instead of
	// hanging on c.writeCh forever.
	//
	// Defer order (LIFO; last-registered runs first):
	//   1. recover (registered first → runs last)
	//   2. stream-cleanup defer (registered last → runs first)
	//   3. c.cancel() (cancels clientCtx → unblocks Prompt/Initialize callers)
	//   4. c.wg.Done() (signals Close()'s wg.Wait() to proceed)
	// Do NOT reorder these defers.
	defer c.cancel()
	defer func() {
		// On any readLoop exit, close the active stream if one exists so callers
		// waiting on Result() don't hang. Identity guard (REL-POOL-03 CAS fix):
		// only clear and close the stream this readLoop instance owns; a stale
		// readLoop goroutine must not clobber a stream owned by a newer Prompt call.
		c.streamMu.Lock()
		s := c.activeStream
		if s != nil {
			c.activeStream = nil
		}
		c.streamMu.Unlock()
		if s != nil {
			s.close(nil, ErrClientClosed)
		}
	}()
	for {
		frame, err := c.framer.readFrame()
		if err != nil {
			// Distinguish payload-too-big from generic EOF / pipe close so
			// operators can correlate slot churn with frame size in
			// /admin tail. The slot still dies (cap exceeded → scanner
			// cannot resync) but the failure mode is now nameable.
			if errors.Is(err, ErrFrameTooLong) {
				c.cfg.Logger.Warn("acp.framer.frame_too_long",
					"max_bytes", maxFrameSize)
			}
			c.failPending(ErrClientClosed)
			return
		}
		var f rpcFrame
		if err := json.Unmarshal(frame, &f); err != nil {
			c.cfg.Logger.Warn("acp: malformed frame", "err", err)
			continue // log and continue — don't kill session on parse error (T-02-04)
		}
		// Track 0 capture: hand the raw method + params to the capture ring
		// before dispatch. Cheap nil check when capture is disabled.
		if c.cfg.OnRawFrame != nil {
			c.cfg.OnRawFrame(f.Method, f.Params)
		}
		c.disp.route(f)
		_ = ctx // ctx is passed for future cancellation use; readLoop exits on EOF
	}
}

// writerLoop is the sole goroutine that calls framer.writeFrame.
// All other goroutines send their serialised request bytes to writeCh.
// ACP-02: one reader + one writer goroutine; writeCh serialises all framer writes.
//
// Audit acp-writer-loop-error-deadlocks-pending-callers: defer c.cancel()
// so a write failure (subprocess stdin pipe closed prematurely, broken
// pipe) propagates immediately to clientCtx. Without this, concurrent
// send() callers blocked on writeCh would only unblock when the readLoop
// eventually detected subprocess death (stdout EOF) — a window that can
// be hundreds of seconds long if kiro-cli closes stdin but keeps stdout
// alive. Mirrors readLoop's existing defer c.cancel() pattern.
func (c *Client) writerLoop(ctx context.Context) {
	defer c.wg.Done()
	defer c.cancel()
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
//
// Audit acp-ping-loop-exits-silently-on-non-cancel-failure +
// pool-hung-subprocess-not-replaceable-no-liveness-preemption: previously
// any non-{Canceled, ErrClientClosed} ping failure logged a Warn and
// returned the loop, leaving the slot "alive" from the pool's
// perspective (Done() never fired). A hung-but-not-exited kiro-cli
// (laptop sleep/wake, embedding-model contention, wedged tool call)
// would silently wedge a slot until the user's per-request ctx
// expired. Now: on unexpected ping failure, call c.cancel() to
// propagate teardown — Done() closes, exit_watcher sees the slot is
// dead, respawnSlot fires on the next acquire. Matches the
// crash-and-replace deployment posture.
func (c *Client) pingLoop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.PingInterval)
	defer ticker.Stop()
	// lastTick is the wall-clock time of the previous cycle; seeded at loop
	// entry so the first real tick (~one interval later) has a normal gap.
	lastTick := c.cfg.Now()
	for {
		select {
		case <-ticker.C:
			var stop bool
			if lastTick, stop = c.pingTick(ctx, lastTick); stop {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// pingTick runs one liveness cycle and returns the updated lastTick plus whether
// the ping loop should stop (an expected Close() or an escalated teardown).
//
// Suspend-guard: if the wall clock jumped by more than 2x the ping interval
// since the previous tick, the machine was almost certainly suspended/frozen
// (normal ticker jitter is sub-millisecond). The first post-resume tick would
// otherwise fire against a 10s deadline that has effectively already elapsed and
// SIGKILL a HEALTHY worker. So that one cycle skips the liveness check entirely.
// The guard is one-shot: the very next tick is a normal cycle, so a genuinely
// hung worker is still caught within one interval, and a worker that actually
// exited during sleep is detected independently by the exit-watcher via Done().
func (c *Client) pingTick(ctx context.Context, lastTick time.Time) (time.Time, bool) {
	now := c.cfg.Now()
	if gap := now.Sub(lastTick); gap > 2*c.cfg.PingInterval {
		c.cfg.Logger.Info("acp.ping.skipped_after_resume",
			"gap", gap.String(), "interval", c.cfg.PingInterval.String())
		if c.cfg.OnPingSuspendSkip != nil {
			c.cfg.OnPingSuspendSkip()
		}
		return now, false
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, ErrClientClosed) {
			return now, true // expected on Close()
		}
		c.cfg.Logger.Warn("acp.ping.escalated_to_close", "err", err)
		if c.cfg.OnPingEscalate != nil {
			c.cfg.OnPingEscalate()
		}
		c.cancel()
		return now, true
	}
	return now, false
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
//
// Phase 1.1 D-08/D-09: emits the spec-compliant params shape and captures
// agentCapabilities.promptCapabilities from the response into c.caps under
// c.stateMu. The signature is unchanged per D-05 so main.go and the future
// pool do not need to rebuild. Defensive parse: a missing or empty
// promptCapabilities object leaves c.caps at the zero value (all false).
func (c *Client) Initialize(ctx context.Context) error {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "initialize",
		Params: initializeParams{
			ProtocolVersion: 1,
			ClientInfo:      clientInfo{Name: "otto-gateway", Version: version.Version},
			ClientCapabilities: clientCapabilities{
				Fs: fsCapabilities{
					ReadTextFile:  false,
					WriteTextFile: false,
				},
				Terminal: false,
			},
		},
	}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("acp: initialize: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			if frame.Error.Code == closeSentinelCode {
				return ErrClientClosed
			}
			return fmt.Errorf("acp: initialize: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		// D-09: capture promptCapabilities. An empty/missing object naturally
		// unmarshals into the zero struct (all false), which is the documented
		// defensive-parse contract — callers should not see an error for that.
		var r initializeResult
		if len(frame.Result) > 0 {
			if err := json.Unmarshal(frame.Result, &r); err != nil {
				return fmt.Errorf("acp: initialize result: %w", err)
			}
		}
		caps := canonical.PromptCapabilities{
			Image:           r.AgentCapabilities.PromptCapabilities.Image,
			Audio:           r.AgentCapabilities.PromptCapabilities.Audio,
			EmbeddedContext: r.AgentCapabilities.PromptCapabilities.EmbeddedContext,
		}
		c.stateMu.Lock()
		c.caps = caps
		c.stateMu.Unlock()
		return nil
	}
}

// NewSession sends session/new with the given working directory and returns the
// session ID. ACP-03.
//
// Phase 1.1 D-10: params include mcpServers as an explicit empty array.
// Phase 1.1 D-11: accept either result.sessionId or result.id (kiro-cli versions
// vary); errors if both are empty.
// Phase 1.1 D-12: populate c.models from result.models.availableModels under
// c.stateMu — callers read via the AvailableModels() accessor.
func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	defer c.disp.cancel(id)

	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/new",
		Params: sessionNewParams{
			CWD:        cwd,
			MCPServers: make([]mcpServer, 0),
		},
	}); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("acp: session/new: %w", ctx.Err())
	case frame := <-respCh:
		if frame.Error != nil {
			if frame.Error.Code == closeSentinelCode {
				return "", ErrClientClosed
			}
			return "", fmt.Errorf("acp: session/new: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		var r sessionNewResult
		if err := json.Unmarshal(frame.Result, &r); err != nil {
			return "", fmt.Errorf("acp: session/new result: %w", err)
		}
		sid := firstNonEmpty(r.SessionID, r.ID)
		if sid == "" {
			return "", fmt.Errorf("acp: session/new result: missing sessionId and id")
		}
		// D-12: translate availableModels into canonical.ModelInfo and store
		// under stateMu. A nil/empty source produces a nil destination.
		var models []canonical.ModelInfo
		if n := len(r.Models.AvailableModels); n > 0 {
			models = make([]canonical.ModelInfo, 0, n)
			for _, entry := range r.Models.AvailableModels {
				models = append(models, canonical.ModelInfo{
					ID:   entry.ModelID,
					Name: entry.Name,
				})
			}
		}
		c.stateMu.Lock()
		c.models = models
		c.stateMu.Unlock()
		return sid, nil
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
			if frame.Error.Code == closeSentinelCode {
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
			if frame.Error.Code == closeSentinelCode {
				return ErrClientClosed
			}
			return fmt.Errorf("acp: session/set_model: rpc error %d: %s", frame.Error.Code, frame.Error.Message)
		}
		return nil
	}
}

// Prompt sends session/prompt and returns a Stream for receiving chunks.
// The stream receives session/update chunks via handleNotification until the
// session signals it is done (prompt response closes the stream).
// D-03: streaming from day 1.
//
// Concurrency contract (Phase 8.3): Prompt returns (*Stream, nil) as soon as
// c.send accepts the session/prompt request payload — it does NOT wait for the
// session/prompt response. A per-prompt goroutine, awaitPromptResult, owns the
// response wait and finalizes the Stream via stream.close(...) when one of:
// (a) ctx is cancelled (defensive session/cancel notification sent, terminal
// error wraps ctx.Err); (b) an RPC error arrives (or the close-sentinel from
// failPending); (c) the happy-path session/prompt response is parsed and
// FinalResult{StopReason} is recorded.
//
// Callers obtain the terminal StopReason / error via Stream.Result(), which
// blocks on <-s.done as the single sync point. Drainage of Stream.Chunks
// MAY happen on whichever goroutine called Prompt for throughput, but is no
// longer REQUIRED for correctness — push() backpressure on a slow consumer
// cannot cascade into the response-wait path because the response-wait runs
// on its own goroutine independent of the chunk drainage rate. This removes
// the Phase 1.1 chunk-buffer-overflow deadlock that wedged any response with
// more than 64 session/update chunks before its session/prompt response.
//
// See Stream's godoc (internal/acp/stream.go) for the full lifecycle.
func (c *Client) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (*Stream, error) {
	id := c.nextID.Add(1)
	respCh := c.disp.register(id)
	// Phase 8.3: defer c.disp.cancel(id) moves INTO awaitPromptResult so the
	// dispatcher entry outlives Prompt's return. On the send-failure path
	// below (no goroutine spawned), we cancel explicitly.

	stream := newStream(ctx, sessionID)

	// Track 3a: record the per-turn deny-builtin-tools signal (set by
	// engine.Run via WithDenyBuiltinTools) BEFORE the stream is published as
	// c.activeStream, so the permission handler (later task) always observes
	// a consistent value once the stream is reachable.
	stream.setDenyBuiltinTools(DenyBuiltinTools(ctx))

	// Register the active stream BEFORE sending the prompt so no update is missed.
	c.streamMu.Lock()
	c.activeStream = stream
	c.streamMu.Unlock()

	// Translate once; ship the same slice on both `prompt` and `content` per
	// D-13 (defensive duplicate for older kiro-cli versions).
	wire := translateBlocks(blocks)
	if err := c.send(ctx, id, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/prompt",
		// CR-05 fix: convert canonical.Block slice to wire shape so kiro-cli
		// receives {"type":"text","text":"..."} rather than the Go default
		// discriminated-struct encoding.
		Params: promptParams{SessionID: sessionID, Prompt: wire, Content: wire},
	}); err != nil {
		// Phase 8.3 Pitfall 4: explicit cancel REQUIRED here — no goroutine
		// will be spawned to defer it.
		c.disp.cancel(id)
		c.streamMu.Lock()
		c.activeStream = nil
		c.streamMu.Unlock()
		return nil, err
	}

	// Phase 8.3 D-04 / Pattern P2: register the per-prompt goroutine with c.wg
	// BEFORE the `go` statement (Go memory model — Add must happen-before the
	// goroutine starts so Client.Close()'s c.wg.Wait observes the registration).
	c.wg.Add(1)
	go c.awaitPromptResult(ctx, id, sessionID, stream, respCh)

	return stream, nil
}

// awaitPromptResult waits for the session/prompt response (or ctx cancel /
// client close) and finalizes the Stream via stream.close(...). Lifetime is
// registered with c.wg so Client.Close() observes its completion before
// returning. Lifted-and-shifted from the synchronous Prompt body in Phase 8.3
// per RESEARCH.md Pattern 1.
//
// Defer order (LIFO; last-registered runs first):
//  1. c.disp.cancel(id) (registered last → runs first on return)
//  2. c.wg.Done() (registered first → runs last so Close()'s wg.Wait observes
//     the dispatcher cleanup before unblocking)
//
// Three exit arms:
//
//   - ctx.Done() — caller cancelled. Clear c.activeStream, send a best-effort
//     session/cancel notification (matches the historical synchronous path;
//     duplicates engine.Run's context.AfterFunc watchdog harmlessly because
//     ACPClient.Cancel is idempotent per CONTEXT.md D-01), finalize the stream
//     with wrapped ctx.Err().
//   - frame := <-respCh with frame.Error != nil — RPC error (or close-sentinel
//     from failPending). Clear c.activeStream, finalize the stream with
//     ErrClientClosed (on close-sentinel) or a wrapped RPC error.
//   - frame := <-respCh with frame.Error == nil — happy path. Parse stopReason
//     (D-07 forward-compat via parseStopReason), read ChunkCount under
//     stream.mu, emit engine.prompt.completed Debug log line (Phase 8.3 D-02 —
//     load-bearing diagnostic for the previously-invisible response latency),
//     finalize the stream with FinalResult{StopReason: stop}.
func (c *Client) awaitPromptResult(
	ctx context.Context,
	id uint64,
	sessionID string,
	stream *Stream,
	respCh <-chan rpcFrame,
) {
	defer c.wg.Done()
	defer c.disp.cancel(id)

	select {
	case <-ctx.Done():
		c.streamMu.Lock()
		// REL-POOL-03 CAS identity guard: only clear activeStream when it still
		// points to the stream this goroutine owns. A stale awaitPromptResult
		// goroutine must not nil out a newer Prompt's activeStream reference.
		if c.activeStream == stream {
			c.activeStream = nil
		}
		c.streamMu.Unlock()
		// Best-effort cancel notification (no id = notification).
		// Two-owner pattern per CONTEXT.md D-01: engine.Run's
		// context.AfterFunc watchdog is also firing in parallel; both are
		// safe because ACPClient.Cancel is idempotent (engine.go:152-158)
		// and the ACP spec treats duplicate session/cancel as no-ops.
		c.sendNotification(rpcNotification{
			JSONRPC: "2.0",
			Method:  "session/cancel",
			Params:  cancelParams{SessionID: sessionID},
		})
		// WR-01: emit engine.prompt.completed on ctx-cancel arm so
		// every prompt that fires engine.prompt.sent is paired with a
		// terminal line (no silent gaps in ops dashboards).
		c.cfg.Logger.Debug(
			"engine.prompt.completed",
			"session_id", sessionID,
			"chunks", chunkCountUnderMu(stream),
			"stop_reason", "ctx_canceled",
		)
		stream.close(nil, fmt.Errorf("acp: prompt: %w", ctx.Err()))
		return

	case frame := <-respCh:
		c.streamMu.Lock()
		// REL-POOL-03 CAS identity guard: only clear activeStream when it still
		// points to the stream this goroutine owns.
		if c.activeStream == stream {
			c.activeStream = nil
		}
		c.streamMu.Unlock()
		if frame.Error != nil {
			// WR-01: emit engine.prompt.completed on the RPC-error /
			// close-sentinel arm. stop_reason distinguishes the two:
			// "client_closed" when the sentinel arrives via failPending
			// (Close() in progress), "rpc_error" otherwise. Chunks may
			// have landed before the error, so include the count.
			stopReason := "rpc_error"
			if frame.Error.Code == closeSentinelCode {
				stopReason = "client_closed"
			}
			c.cfg.Logger.Debug(
				"engine.prompt.completed",
				"session_id", sessionID,
				"chunks", chunkCountUnderMu(stream),
				"stop_reason", stopReason,
			)
			if frame.Error.Code == closeSentinelCode {
				stream.close(nil, ErrClientClosed)
				return
			}
			stream.close(nil, fmt.Errorf("acp: prompt: rpc error %d: %s", frame.Error.Code, frame.Error.Message))
			return
		}
		// Phase 1.1 D-07: parse result.stopReason (forward-compat: unknown
		// wire values map to StopUnknown via parseStopReason — do NOT fail
		// the prompt over an unrecognised stop reason).
		var r promptResult
		if len(frame.Result) > 0 {
			if err := json.Unmarshal(frame.Result, &r); err != nil {
				c.cfg.Logger.Warn("acp: prompt result parse failed", "err", err)
			}
		}
		stop := parseStopReason(r.StopReason)
		// Phase 8.3 D-02: new engine.prompt.completed line carries the
		// response-landed signal that ops dashboards previously timed via
		// engine.prompt.sent (whose semantics shifted to send-accepted in
		// this phase). Emit RAW wire r.StopReason — canonical.StopReason
		// has no String() method and CONTEXT.md forbids canonical-type
		// changes in this phase. chunkCountUnderMu reads stream.result
		// .ChunkCount under stream.mu (matches stream.go:85-87 pattern).
		c.cfg.Logger.Debug(
			"engine.prompt.completed",
			"session_id", sessionID,
			"chunks", chunkCountUnderMu(stream),
			"stop_reason", r.StopReason,
		)
		stream.close(&FinalResult{StopReason: stop}, nil)
	}
}

// chunkCountUnderMu reads stream.result.ChunkCount while holding stream.mu.
// Factored out so all three terminal arms of awaitPromptResult share the same
// pattern (WR-01: emit engine.prompt.completed on every exit arm).
func chunkCountUnderMu(s *Stream) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result.ChunkCount
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
//   - session/request_permission → RESPOND on the original frame id with
//     {optionId:"allow_always", granted:true} (Phase 1.1 D-20). The Phase 1
//     path that sent a new grant-permission request is removed —
//     kiro-cli waits for the response to its original id, so sending a new
//     request id would deadlock the subprocess.
//   - session/update / session/notification / _kiro.dev/session/update →
//     canonical.Chunk → push to activeStream (ACP-05). All three method
//     names are matched explicitly per D-16; discriminator + body variance
//     is absorbed inside translateUpdate per D-17/D-18/D-19.
func (c *Client) handleNotification(frame rpcFrame) {
	switch frame.Method {
	case "session/request_permission":
		// D-20: respond on the original frame id. Without this, kiro-cli
		// blocks forever waiting for the response — the deadlock unblock for
		// Phase 2's first tool-using prompt.
		if !frame.hasID() {
			// A permission "request" with no id cannot be responded to —
			// treat as a kiro-cli protocol break and log loudly.
			c.cfg.Logger.Warn("acp: permission request without id — dropped")
			return
		}
		// Best-effort Debug log of the inbound RequestID (useful when DEBUG=1).
		// Parse failure does not block the response — the response only needs
		// the original frame id, which is already in hand.
		// CR-01: frame.ID is json.RawMessage; log its raw byte form so string
		// AND numeric ids both render readably.
		var params permissionParams
		if err := json.Unmarshal(frame.Params, &params); err == nil && params.RequestID != "" {
			c.cfg.Logger.Debug("acp: auto-granting permission",
				"requestId", params.RequestID, "frameId", string(frame.ID))
		}
		// CR-01: echo frame.ID verbatim so a string-id permission request
		// gets a string-id response. The Phase 1.1-pre code dereffed a
		// *uint64 here, which would silently drop string ids and reintroduce
		// the D-20 deadlock.
		data, err := json.Marshal(rpcResponse{
			JSONRPC: "2.0",
			ID:      frame.ID,
			Result: map[string]any{
				"optionId": "allow_always",
				"granted":  true,
			},
		})
		if err != nil {
			c.cfg.Logger.Warn("acp: marshal permission response failed", "err", err)
			return
		}
		// WR-02 (Phase 1.1 review): the permission response writes directly
		// via the framer rather than queueing on writeCh. The readLoop
		// goroutine is BOTH the consumer of inbound frames AND the sole
		// producer of this specific outbound frame. Queueing the response
		// behind a full writeCh (capacity 16) means the readLoop blocks here
		// while the writer goroutine drains — and while readLoop is blocked
		// no new frames are read from the subprocess pipe, including the
		// session/prompt response that would unblock callers and reduce
		// writeCh backlog. The framer's internal mutex (framer.go:58-65)
		// already serialises against the writer goroutine, so calling
		// writeFrame directly is race-free; the only side-effect is that
		// permission responses no longer share fifo ordering with normal
		// RPC sends, which is fine — they're independent JSON-RPC frames.
		if err := c.framer.writeFrame(json.RawMessage(data)); err != nil {
			// Audit acp-permission-response-direct-write-races-shutdown:
			// kiro-cli deadlocks waiting for this response (D-20). A
			// dropped response — whether from Close racing the readLoop
			// or a transient pipe stall — makes the slot unusable
			// regardless. Escalate by cancelling clientCtx so the slot
			// gets replaced via exit_watcher instead of silently wedging.
			c.cfg.Logger.Warn("acp.permission_response.write_failed_escalating",
				"err", err)
			c.cancel()
		}

	case "session/update", "session/notification", "_kiro.dev/session/update":
		// D-16: all three method names route to the same tolerant parser.
		// D-17/D-18/D-19 variance is absorbed inside translateUpdate.
		var update sessionUpdateParams
		if err := json.Unmarshal(frame.Params, &update); err != nil {
			c.cfg.Logger.Warn("acp: malformed session update",
				"method", frame.Method, "err", err)
			return
		}
		// WR-05: translateUpdate returns ok=false when the inner-update
		// payload is malformed. Drop the notification rather than push a
		// phantom empty chunk that the consumer cannot distinguish from a
		// real empty message.
		chunk, ok := translateUpdate(c.cfg.Logger, update)
		if !ok {
			return
		}

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
		// Audit acp-late-update-cross-session-leak: late session/update
		// notifications carrying a stale session id (kiro-cli buffering
		// after the prompt response, or specific tool_call_update
		// flushes) used to push onto whatever activeStream was non-nil,
		// corrupting the new prompt's stream with text from the old one.
		// Compare update.SessionID against the stream's session id and
		// drop the chunk on mismatch.
		if streamSID := s.SessionID(); streamSID != "" && update.SessionID != "" && update.SessionID != streamSID {
			c.cfg.Logger.Warn("acp.stream.cross_session_drop",
				"update_session_id", update.SessionID,
				"active_session_id", streamSID,
				"method", frame.Method)
			return
		}
		// P-4 fix (REL-POOL-04): push with the stream's PER-REQUEST ctx
		// rather than c.clientCtx (the client lifetime). A stalled SSE
		// consumer that fills the 64-chunk buffer used to block the
		// readLoop goroutine on c.clientCtx — the readLoop is the same
		// goroutine that dispatches ping responses, so a stalled
		// consumer starved the pingLoop and triggered
		// "acp.ping.escalated_to_close" SIGKILL on a healthy worker.
		// Using s.Ctx() means the stalled consumer fails its OWN
		// request when ctx expires and the readLoop is freed.
		if err := s.push(s.Ctx(), chunk); err != nil {
			if errors.Is(err, errPushAfterClose) {
				// Late notification — the active stream was closed (per-prompt
				// ctx cancel or readLoop teardown) between our streamMu unlock
				// and the push. Drop the chunk. This is the race that used to
				// panic on send-to-closed-channel before sendMu serialization.
				c.cfg.Logger.Warn("acp.stream.push_after_close",
					"method", frame.Method)
			} else {
				c.cfg.Logger.Warn("acp: stream push failed", "err", err)
			}
		}

	case "_kiro.dev/metadata":
		// kiro usage-metrics parity: per-turn utilization stream. Fire
		// OnContextPct on any contextUsagePercentage (0–100 percent) and
		// OnTurnMeter only when meteringUsage is present (turn complete).
		var meta metadataParams
		if err := json.Unmarshal(frame.Params, &meta); err != nil {
			c.cfg.Logger.Warn("acp: malformed kiro metadata", "err", err)
			return
		}
		if meta.ContextUsagePercentage != nil && c.cfg.OnContextPct != nil {
			c.cfg.OnContextPct(*meta.ContextUsagePercentage)
		}
		// Presence of meteringUsage (even an empty array) signals turn
		// completion; nil means a mid-turn frame. Do NOT gate on len > 0 — a
		// completed zero-cost turn still reports its turn + duration. The same
		// frame's contextUsagePercentage (if present) is the end-of-turn context,
		// threaded through so the consumer observes the ctx histogram once per turn.
		if meta.MeteringUsage != nil && c.cfg.OnTurnMeter != nil {
			var turnMs int64
			if meta.TurnDurationMs != nil {
				turnMs = *meta.TurnDurationMs
			}
			var ctxPct float64
			hasCtxPct := meta.ContextUsagePercentage != nil
			if hasCtxPct {
				ctxPct = *meta.ContextUsagePercentage
			}
			c.cfg.OnTurnMeter(sumCredits(*meta.MeteringUsage), turnMs, ctxPct, hasCtxPct)
		}

	case "_kiro.dev/mcp/server_initialized":
		c.handleMCPInit(frame, true)
	case "_kiro.dev/mcp/server_init_failure":
		c.handleMCPInit(frame, false)

	default:
		c.cfg.Logger.Debug("acp: unhandled notification", "method", frame.Method)
	}
}

// handleMCPInit fires the OnMCPInit hook for the two `_kiro.dev/mcp/*`
// notifications (kiro usage-metrics parity). ok=true for server_initialized,
// false for server_init_failure. A malformed frame is logged and dropped —
// never fatal (mirrors the metadata + session/update tolerant-parse policy).
func (c *Client) handleMCPInit(frame rpcFrame, ok bool) {
	if c.cfg.OnMCPInit == nil {
		return
	}
	var p mcpServerParams
	if err := json.Unmarshal(frame.Params, &p); err != nil {
		c.cfg.Logger.Warn("acp: malformed kiro mcp init", "method", frame.Method, "err", err)
		return
	}
	c.cfg.OnMCPInit(p.ServerName, ok)
}

// Done returns a channel that is closed when the client's subprocess has
// exited (either via Close() or via the readLoop's defer c.cancel() that
// fires on EOF / pipe close / ping-loop failure). The channel is closed
// exactly once.
//
// Done is the push-based exit signal added in Phase 5 (D-01) for the
// per-slot exit-watcher in internal/pool. It is intentionally a
// receive-only chan struct{} (no error payload).
//
// The channel is derived from the existing private clientCtx — Close()
// step 1 (cancel()) cancels clientCtx, so Done() fires for the same
// teardown paths that already fire ErrClientClosed for in-flight callers.
// No new fields, no new goroutines — pure accessor.
func (c *Client) Done() <-chan struct{} {
	return c.clientCtx.Done()
}

// Pid returns the kiro-cli subprocess OS process id, or 0 when the
// subprocess is not spawned (e.g. *Client constructed via NewWithConn
// for in-memory tests) or has already exited. Used by pool.respawnSlot
// for the D-18-05 lazy-respawn-success INFO log so an operator can
// correlate the recovered slot's NEW pid against the previous death log.
//
// Never panics: nil-guard on cmd and cmd.Process so test fakes and
// post-Wait clients return 0 safely.
func (c *Client) Pid() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
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
		//
		// WR-03 (Phase 1.1 review): the cancel() call in step 1 propagates to
		// exec.CommandContext which sends SIGKILL to the subprocess. The
		// subsequent Wait() then surfaces an *exec.ExitError reporting
		// "signal: killed" — that's our own teardown, not an external fault,
		// and surfacing it to every Close() caller forces them to log-and-
		// ignore (which every test in this package does). Filter the expected
		// "killed by our cancel" signal here so Close() returns nil on the
		// happy teardown path; only genuinely unexpected exit errors flow
		// through to firstErr.
		if c.cmd != nil {
			pid := 0
			if c.cmd.Process != nil {
				pid = c.cmd.Process.Pid
			}
			if err := c.cmd.Wait(); err != nil && firstErr == nil {
				if !isExpectedTeardownExit(err) {
					firstErr = fmt.Errorf("acp: cmd wait: %w", err)
				} else {
					c.cfg.Logger.Debug("acp: cmd exited via context cancellation (expected)", "err", err)
				}
			}
			// Audit acp-grandchildren-not-killed-on-close: defensive
			// second pgrp-kill to catch any race where the leader exited
			// before its children reparented. ESRCH (no such process)
			// is the expected outcome and is silently ignored — we are
			// signaling -pid, so it fires only if grandchildren are
			// still alive in that pgrp. No-op on Windows.
			if pid > 0 {
				_ = killProcessGroup(pid, syscall.SIGKILL)
			}
		}
	})
	return firstErr
}

// isExpectedTeardownExit reports whether err from cmd.Wait() is the result of
// our own context cancellation (subprocess killed by signal) rather than an
// independent subprocess failure. We treat any exit that reports the process
// as not having exited normally (signaled) as expected teardown — the cancel
// in Close() step 1 SIGKILLs the process via exec.CommandContext, and an
// ExitError with ProcessState.Exited()==false is the signal-driven termination
// path. Genuinely-failed subprocess exits (non-zero exit status from a regular
// exit) still propagate to firstErr so callers can diagnose them.
func isExpectedTeardownExit(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	if exitErr.ProcessState == nil {
		return false
	}
	// Exited()==false means the process was terminated by a signal (or
	// stopped); that's the SIGKILL path we expect on Close.
	return !exitErr.Exited()
}

// PromptCapabilities returns the agent's prompt capabilities captured during
// the most recent successful Initialize. Before Initialize succeeds (or if
// the agent omitted the field), this returns the zero canonical.PromptCapabilities
// (all flags false). The returned value is a snapshot — callers may compare
// it freely without locking.
//
// D-05: no context arg — this is a cached read, not an RPC.
func (c *Client) PromptCapabilities() canonical.PromptCapabilities {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.caps
}

// AvailableModels returns a snapshot of the models exposed by the agent in
// the most recent successful NewSession response. Before NewSession succeeds,
// this returns nil. The returned slice is a defensive copy — callers may
// mutate it without affecting the client's internal state.
//
// D-05: no context arg — this is a cached read, not an RPC.
// Plan 03 populates c.models; this plan declares the field and the accessor.
func (c *Client) AvailableModels() []canonical.ModelInfo {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.models == nil {
		return nil
	}
	return append([]canonical.ModelInfo(nil), c.models...)
}
