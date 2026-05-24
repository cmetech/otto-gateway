package pool

// Stats is a point-in-time snapshot of pool occupancy returned by
// Pool.Stats. The /health endpoint (Plan 06) exposes these fields under
// the OBSV-01 observability surface.
//
// Alive counts slots whose Client is non-nil (every successfully-spawned
// slot until Pool.Close runs). Busy is len(all) - len(slots) — slots
// that are checked out for an in-flight request.
type Stats struct {
	// Size is the configured pool size (cfg.Size after applyDefaults).
	Size int
	// Alive is the count of slots whose Client is non-nil.
	Alive int
	// Busy is the count of slots currently checked out for an in-flight
	// request (computed as len(all) - len(slots)).
	Busy int
}
