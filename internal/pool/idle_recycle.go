package pool

import "time"

type recycleReason string

const (
	recycleReasonMaxTurns   recycleReason = "max_turns"
	recycleReasonIdleMemory recycleReason = "idle_memory"
)

type recycleTrigger struct {
	reason       recycleReason
	turns        int
	userRequests uint64
	pid          int
	rssBytes     uint64
	idle         time.Duration
}

func idleSweepCadence(after time.Duration) time.Duration {
	d := after / 4
	if d < time.Second {
		return time.Second
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func (p *Pool) startIdleRecycler() {
	if p.cfg.IdleRecycleAfter == 0 {
		return
	}
	if !p.cfg.WorkerMemorySupported || p.cfg.ReadWorkerMemory == nil {
		if p.cfg.Logger != nil {
			p.cfg.Logger.Warn("pool: idle-memory recycling unavailable on this platform")
		}
		return
	}
	if p.idleSweepLifecycleHook != nil {
		p.idleSweepLifecycleHook("before_admission")
	}
	p.idleSweepLifecycleMu.Lock()
	defer p.idleSweepLifecycleMu.Unlock()
	if p.idleSweepClosing {
		return
	}
	p.idleSweepOnce.Do(func() {
		if p.idleSweepLifecycleHook != nil {
			p.idleSweepLifecycleHook("admitted")
		}
		p.idleSweepWG.Add(1)
		go p.idleRecycleLoop()
	})
}

func (p *Pool) idleRecycleLoop() {
	defer p.idleSweepWG.Done()
	ticks := p.idleSweepTicks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(idleSweepCadence(p.cfg.IdleRecycleAfter))
		ticks = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-ticks:
			p.sweepIdleWorkers()
		case <-p.closing:
			return
		}
	}
}

func (p *Pool) sweepIdleWorkers() {
	free := len(p.slots)
	for i := 0; i < free; i++ {
		select {
		case slot := <-p.slots:
			p.inspectIdleWorker(slot)
		default:
			return
		}
	}
}

func (p *Pool) inspectIdleWorker(slot *Slot) {
	p.mu.Lock()
	closed := p.closed
	dead := slot.dead
	respawning := slot.respawning
	userRequests := slot.userRequestsSinceSpawn
	lastRelease := slot.lastUserReleaseAt
	turns := slot.turns
	client := slot.Client
	p.mu.Unlock()

	idle := p.cfg.Now().Sub(lastRelease)
	if idle < 0 {
		idle = 0
	}
	if closed || dead || respawning || userRequests == 0 || lastRelease.IsZero() || idle < p.cfg.IdleRecycleAfter ||
		!p.cfg.WorkerMemorySupported || p.cfg.ReadWorkerMemory == nil {
		p.releaseOrRecycle(slot)
		return
	}

	pid := 0
	if client != nil {
		pid = client.Pid()
	}
	if pid <= 0 {
		p.releaseOrRecycle(slot)
		return
	}
	rssBytes, ok := p.cfg.ReadWorkerMemory(pid)
	if !ok || rssBytes <= p.cfg.IdleRecycleMemoryBytes {
		p.releaseOrRecycle(slot)
		return
	}
	p.returnOrRecycle(slot, &recycleTrigger{
		reason:       recycleReasonIdleMemory,
		turns:        turns,
		userRequests: userRequests,
		pid:          pid,
		rssBytes:     rssBytes,
		idle:         idle,
	})
}
