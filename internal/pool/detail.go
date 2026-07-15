package pool

// AgentSlot is the per-slot detail row consumed by Plan 05-03's
// /health/agents handler (D-15). The JSON tags are the load-bearing
// wire contract; downstream consumers — agentsHandler in
// internal/server/agents.go and any operator dashboard — depend on the
// snake_case shape verbatim. CurrentSessionID is *string so a slot with
// no active session renders as `"current_session_id": null` instead of
// `"current_session_id": ""` — matches the D-15 example shape.
type AgentSlot struct {
	Label            string  `json:"label"`
	Alive            bool    `json:"alive"`
	Busy             bool    `json:"busy"`
	CurrentSessionID *string `json:"current_session_id"`
}

// Detail returns a point-in-time snapshot of per-slot state for
// /health/agents (D-15). Caller receives a fresh slice — internal pool
// state is never aliased out. Empty pool (Detail() before Warmup, or
// after a respawn-failure shrink consumed all slots) returns a
// zero-length slice rather than nil so the handler always encodes
// `"slots": []` (not `"slots": null`).
//
// Concurrency: holds p.mu for the snapshot; no slot.Client method calls
// under the lock. Mirrors the stats.go discipline (short critical
// section).
func (p *Pool) Detail() []AgentSlot {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Invert p.sessionSlots (sid → slot) into slot → sid so each row
	// can render its CurrentSessionID. A slot may legitimately appear
	// in p.sessionSlots zero or one time — concurrent sessions on the
	// same slot are not supported by the slot-stateless semantics.
	slotToSID := make(map[*Slot]string, len(p.sessionSlots))
	for sid, slot := range p.sessionSlots {
		slotToSID[slot] = sid
	}

	rows := make([]AgentSlot, 0, len(p.all))
	for _, slot := range p.all {
		if slot == nil {
			continue
		}
		row := AgentSlot{
			Label: slot.Label,
			Alive: !slot.dead,
		}
		if sid, ok := slotToSID[slot]; ok {
			// Defensive copy of the string into a fresh pointer so the
			// row owns its data independently of the snapshot loop's
			// scope. Without this every row would alias the loop
			// variable `sid` and end up pointing to the same string.
			sidCopy := sid
			row.Busy = true
			row.CurrentSessionID = &sidCopy
		}
		rows = append(rows, row)
	}
	return rows
}

// WorkerProc pairs a slot's stable label with the live OS pid of the kiro-cli
// subprocess it wraps. It is the input to the gateway's per-worker resource
// metrics (Prometheus gw_worker_* series and the admin dashboard perf tiles) —
// the label is the bounded, respawn-stable series key, never the pid.
type WorkerProc struct {
	Label string
	Pid   int
}

// WorkerProcs returns the (label, pid) of every live slot for per-worker CPU/RSS
// sampling. Dead, nil, and not-yet-spawned slots (pid <= 0) are skipped so a
// caller only sees processes it can actually read.
//
// Concurrency mirrors Detail: the (label, Client) pairs are snapshotted under
// p.mu, then the lock is released BEFORE any slot.Client.Pid() call — upholding
// the pool invariant that no slot.Client method runs while p.mu is held. Pid()
// is a cheap non-blocking getter, but keeping it outside the critical section
// keeps the discipline uniform with the exit-watcher and stats paths.
func (p *Pool) WorkerProcs() []WorkerProc {
	type labelled struct {
		label  string
		client PoolClient
	}

	p.mu.Lock()
	pending := make([]labelled, 0, len(p.all))
	for _, slot := range p.all {
		if slot == nil || slot.dead || slot.Client == nil {
			continue
		}
		pending = append(pending, labelled{label: slot.Label, client: slot.Client})
	}
	p.mu.Unlock()

	out := make([]WorkerProc, 0, len(pending))
	for _, e := range pending {
		pid := e.client.Pid()
		if pid <= 0 {
			continue
		}
		out = append(out, WorkerProc{Label: e.label, Pid: pid})
	}
	return out
}
