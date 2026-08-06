package admin

// TailState is the browser-safe health state for a configured log source.
type TailState string

const (
	TailStateOpening    TailState = "opening"
	TailStateMissing    TailState = "missing"
	TailStateUnreadable TailState = "unreadable"
	TailStateEmpty      TailState = "empty"
	TailStateWatching   TailState = "watching"
)

// TailStatus deliberately excludes the configured path and raw OS errors.
// Those diagnostics stay in the Gateway's own structured log.
type TailStatus struct {
	State      TailState `json:"state"`
	SizeBytes  *int64    `json:"size_bytes,omitempty"`
	ModifiedAt string    `json:"modified_at,omitempty"`
	Level      string    `json:"level,omitempty"`
}

func tailStatusesEqual(left, right TailStatus) bool {
	if left.State != right.State || left.ModifiedAt != right.ModifiedAt || left.Level != right.Level {
		return false
	}
	if left.SizeBytes == nil || right.SizeBytes == nil {
		return left.SizeBytes == nil && right.SizeBytes == nil
	}
	return *left.SizeBytes == *right.SizeBytes
}
