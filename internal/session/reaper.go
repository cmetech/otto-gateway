package session

// reaperLoop is the per-Registry reaper goroutine. Task 0 STUB: returns
// immediately on the first <-r.closing signal so Registry.Close() +
// goleak gate behave correctly while the full ticker+TryLock loop is
// pending Task 2. The wg.Done deferral is load-bearing — without it
// Registry.Close's wg.Wait() would hang forever.
func (r *Registry) reaperLoop() {
	defer r.wg.Done()
	<-r.closing
}

// reapOnce performs one reaper iteration: snapshot under r.mu.RLock,
// release, then iterate the snapshot taking each entry's Mu.TryLock,
// reaping the truly-idle ones (D-11 + D-12 combined). Task 0 STUB:
// full implementation arrives in Task 2.
func (r *Registry) reapOnce() {
	// Task 2 stub — body is implemented in Task 2.
}
