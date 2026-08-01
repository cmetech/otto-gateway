package privacy

import "errors"

// ErrTriageUnavailable is returned when no strict scope store exists. It is a
// stable sentinel only; callers must not serialize underlying errors.
var ErrTriageUnavailable = errors.New("privacy triage unavailable")

// TriageCapability is the intentionally narrow, explicit capability used by
// the protected localhost transport. It does not expose transformation policy
// or the underlying ScopeStore.
type TriageCapability interface {
	ListScopes() []ScopeInfo
	InspectScope(string) ([]MappingEntry, error)
	ClearScope(string) (ClearResult, error)
	ClearAllScopes() ClearResult
}

type triageCapability struct {
	service *Service
}

// TriageCapability returns the service's mapping-lifecycle capability. Route
// authorization remains the responsibility of the admin transport.
func (s *Service) TriageCapability() TriageCapability {
	return triageCapability{service: s}
}

func (c triageCapability) ListScopes() []ScopeInfo {
	if c.service == nil || c.service.store == nil {
		return []ScopeInfo{}
	}
	return c.service.store.List()
}

func (c triageCapability) InspectScope(scopeID string) ([]MappingEntry, error) {
	if c.service == nil || c.service.store == nil {
		return nil, ErrTriageUnavailable
	}
	return c.service.store.Inspect(scopeID)
}

func (c triageCapability) ClearScope(scopeID string) (ClearResult, error) {
	if c.service == nil || c.service.store == nil {
		return "", ErrTriageUnavailable
	}
	return c.service.store.Clear(scopeID)
}

func (c triageCapability) ClearAllScopes() ClearResult {
	if c.service == nil || c.service.store == nil {
		return ClearCompleted
	}
	if summary := c.service.store.ClearAll(); summary.Closing != 0 {
		return ClearClosing
	}
	return ClearCompleted
}
