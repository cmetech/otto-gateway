package privacy

import (
	"strings"
	"testing"
)

func TestActionsPreserveStandardModes(t *testing.T) {
	key, err := DeriveEncryptionKey("compat-encrypt-key")
	if err != nil {
		t.Fatalf("DeriveEncryptionKey: %v", err)
	}
	cases := []struct {
		name   string
		action Action
		want   string
	}{
		{name: "replace", action: ActionReplace, want: "[EMAIL_2]"},
		{name: "mask", action: ActionMask, want: "co***@ex***.com"},
		{name: "hash", action: ActionHash, want: "[EMAIL:h-dcf2c438]"},
		{name: "drop", action: ActionDrop, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyAction(tc.action, "Email", "corey@example.com", 2, []byte("compat-key"), key)
			if got != tc.want {
				t.Fatalf("ApplyAction: got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("encrypt", func(t *testing.T) {
		token := ApplyAction(ActionEncrypt, "Email", "corey@example.com", 2, nil, key)
		entity, payload, ok := ParseEncryptedToken(token)
		if !ok || entity != "Email" {
			t.Fatalf("ParseEncryptedToken(%q): entity=%q ok=%v", token, entity, ok)
		}
		got, err := DecryptToken(key, entity, payload)
		if err != nil {
			t.Fatalf("DecryptToken: %v", err)
		}
		if got != "corey@example.com" {
			t.Fatalf("round trip: got %q", got)
		}
	})
}

func TestActionsEncryptionFailureNeverReturnsRawValue(t *testing.T) {
	const raw = "corey@example.com"
	got := ApplyAction(ActionEncrypt, "Email", raw, 7, nil, nil)
	if strings.Contains(got, raw) || got != "[EMAIL_7]" {
		t.Fatalf("encrypt failure: got %q", got)
	}
}

func TestActionsOneWaySecretLabelIsScopedAndContainsNoRawValue(t *testing.T) {
	const raw = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"
	first := OneWaySecretLabel([]byte("alias-key"), "scope-a", "API_KEY", raw)
	repeat := OneWaySecretLabel([]byte("alias-key"), "scope-a", "API_KEY", raw)
	otherScope := OneWaySecretLabel([]byte("alias-key"), "scope-b", "API_KEY", raw)
	if first != repeat {
		t.Fatalf("same scoped input changed: %q != %q", first, repeat)
	}
	if first == otherScope {
		t.Fatalf("different scopes linked through %q", first)
	}
	if strings.Contains(first, raw) || !strings.HasPrefix(first, "[SECRET:API_KEY_") {
		t.Fatalf("unsafe label %q", first)
	}
}
