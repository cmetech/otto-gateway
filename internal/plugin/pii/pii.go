package pii

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/privacy"
)

var (
	decryptTokenRe = regexp.MustCompile(`\[PII:([A-Za-z0-9_]+):([A-Za-z0-9_-]+)\]`)
	barePayloadRe  = regexp.MustCompile(`[A-Za-z0-9_-]{38,}`)
)

// PIIRedactionHook preserves the registered compatibility name while routing
// all transformations through one privacy Service. The deprecated fields are
// a temporary construction shim for callers migrated in Task 8.
type PIIRedactionHook struct { //nolint:revive // compatibility-facing registered name
	Service *privacy.Service

	// Deprecated: construct Service explicitly for new code.
	Recognizers     []Recognizer
	Enabled         bool
	Mode            string
	HashKey         []byte
	EncryptKey      []byte
	EntityActions   map[string]string
	EnabledEntities []string
	Logger          *slog.Logger
	NER             *nerEngine

	legacyOnce    sync.Once
	legacyService *privacy.Service
	legacyErr     error
}

func (h *PIIRedactionHook) Name() string { return "PIIRedactionHook" }

func (h *PIIRedactionHook) Describe() (string, map[string]any) {
	service := h.delegate()
	if service == nil {
		return "Pre,Post", map[string]any{}
	}
	return "Pre,Post", service.Describe()
}

func (h *PIIRedactionHook) Before(ctx context.Context, req *canonical.ChatRequest) (resp *canonical.ChatResponse, err error) {
	defer func() {
		if recover() != nil {
			resp = nil
			err = &privacy.Error{Code: privacy.CodeInternalError, Stage: "input"}
		}
	}()
	service := h.delegate()
	if service == nil {
		return nil, nil
	}
	return service.Before(ctx, req)
}

func (h *PIIRedactionHook) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) (err error) {
	defer func() {
		if recover() != nil {
			err = &privacy.Error{Code: privacy.CodeInternalError, Stage: "output"}
		}
	}()
	service := h.delegate()
	if service == nil {
		return nil
	}
	return service.After(ctx, req, resp)
}

func (h *PIIRedactionHook) delegate() *privacy.Service {
	if h == nil {
		return nil
	}
	if h.Service != nil {
		return h.Service
	}
	h.legacyOnce.Do(func() {
		entityActions := make(map[string]privacy.Action, len(h.EntityActions))
		for entity, action := range h.EntityActions {
			entityActions[entity] = privacy.Action(action)
		}
		activeNames := h.activeEntityNames()
		if h.NER != nil {
			allow := h.enabledEntitiesSet()
			for _, entity := range nerEntityNames {
				if allow != nil {
					if _, ok := allow[entity]; !ok {
						continue
					}
				}
				activeNames = append(activeNames, entity)
			}
		}
		h.legacyService, h.legacyErr = privacy.NewService(privacy.Config{
			DefaultProfile:   privacy.ProfileStandard,
			RequestProfiles:  []privacy.Profile{privacy.ProfileStandard},
			PIIEnabled:       h.Enabled,
			PIIMode:          privacy.Action(h.Mode),
			PIIHashKey:       h.HashKey,
			PIIEncryptKey:    h.EncryptKey,
			PIIEntityActions: entityActions,
			Recognizers:      activeNames,
			NEREnabled:       h.NER != nil,
			Classifier:       newPIIClassifierWithNER(h.Recognizers, h.EnabledEntities, h.NER),
		})
	})
	if h.legacyErr != nil {
		return nil
	}
	return h.legacyService
}

func (h *PIIRedactionHook) activeEntityNames() []string {
	if h == nil {
		return nil
	}
	if len(h.EnabledEntities) == 0 {
		out := make([]string, 0, len(h.Recognizers))
		for _, recognizer := range h.Recognizers {
			out = append(out, recognizer.Name)
		}
		return out
	}
	allow := h.enabledEntitiesSet()
	out := make([]string, 0, len(h.EnabledEntities))
	for _, recognizer := range h.Recognizers {
		if _, ok := allow[recognizer.Name]; ok {
			out = append(out, recognizer.Name)
		}
	}
	return out
}

func (h *PIIRedactionHook) enabledEntitiesSet() map[string]struct{} {
	if h == nil || len(h.EnabledEntities) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(h.EnabledEntities))
	for _, entity := range h.EnabledEntities {
		out[entity] = struct{}{}
	}
	return out
}

func (h *PIIRedactionHook) actionFor(entity string) string {
	if h == nil {
		return ""
	}
	if action, ok := h.EntityActions[entity]; ok {
		return action
	}
	return h.Mode
}

func (h *PIIRedactionHook) encryptActive() bool {
	if h == nil {
		return false
	}
	if h.Mode == string(privacy.ActionEncrypt) {
		return true
	}
	for _, action := range h.EntityActions {
		if action == string(privacy.ActionEncrypt) {
			return true
		}
	}
	return false
}

func classifyDecryptErr(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "bad base64"):
		return "bad_base64"
	case strings.Contains(message, "payload too short"):
		return "payload_too_short"
	case strings.Contains(message, "gcm open"):
		return "gcm_open"
	default:
		return "decrypt_other"
	}
}

var (
	_ engine.PreHook  = (*PIIRedactionHook)(nil)
	_ engine.PostHook = (*PIIRedactionHook)(nil)
)
