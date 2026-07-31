package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jwtFor builds a JWT carrying email + a far-future exp.
func jwtFor(email string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload, _ := json.Marshal(map[string]any{"email": email, "exp": time.Now().Unix() + 86400})
	return hdr + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)        // unix
	t.Setenv("USERPROFILE", tmp) // windows — os.UserHomeDir reads this there
	return tmp
}

func writeCredentials(t *testing.T, home string, value any) {
	t.Helper()
	dir := filepath.Join(home, ".config", "agend")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRetiredCredentialSchemasAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "flat", value: map[string]any{
			"token": jwtFor("old@acme.test"), "api_url": "https://api.example.test",
		}},
		{name: "schema_two", value: map[string]any{
			"version": 2, "active": "old@acme.test",
			"accounts": map[string]any{"old@acme.test": map[string]any{
				"email": "old@acme.test", "token": jwtFor("old@acme.test"),
				"control_plane_version": "v2",
			}},
		}},
		{name: "future_schema", value: map[string]any{
			"version": 99, "accounts": map[string]any{},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateHome(t)
			writeCredentials(t, home, tc.value)
			if _, err := LoadToken(); err == nil || !strings.Contains(err.Error(), "log in again") {
				t.Fatalf("retired schema error = %v, want login requirement", err)
			}
		})
	}
}

func TestStoreRequiresExactSchemaAndV2EnvironmentMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		marker  string
	}{
		{name: "unstamped", version: storeVersion},
		{name: "retired_marker", version: storeVersion, marker: "retired"},
		{name: "future_marker", version: storeVersion, marker: "v3"},
		{name: "unknown_marker", version: storeVersion, marker: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateHome(t)
			account := map[string]any{
				"email":         "ambiguous@acme.test",
				"token":         jwtFor("ambiguous@acme.test"),
				"env_id":        "env-ambiguous",
				"endpoint":      "https://old.example.test",
				"secret":        "old-secret",
				"session_token": "old-session",
			}
			if tc.marker != "" {
				account["control_plane_version"] = tc.marker
			}
			writeCredentials(t, home, map[string]any{
				"version":  tc.version,
				"active":   "ambiguous@acme.test",
				"accounts": map[string]any{"ambiguous@acme.test": account},
			})

			envID, endpoint, secret, session, err := LoadEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			if envID != "" || endpoint != "" || secret != "" || session != "" {
				t.Fatalf("unsupported environment authority escaped: %q %q %q %q",
					envID, endpoint, secret, session)
			}
			saved, err := SaveSessionTokenForEnvironment(
				"env-ambiguous", "old-secret", "old-session", "replacement-session",
			)
			if err != nil {
				t.Fatal(err)
			}
			if saved {
				t.Fatal("session token was saved for a non-v2 environment")
			}
		})
	}
}

// login adds-not-clobbers, and switching to a fresh account must NOT inherit
// the previous account's environment (bug #3).
func TestLoginAddNotClobber_NoEnvInheritance(t *testing.T) {
	isolateHome(t)

	if err := SaveToken(jwtFor("a@acme.test")); err != nil {
		t.Fatal(err)
	}
	if err := SaveEnvironment("env-a", "https://a", "secret-a"); err != nil {
		t.Fatal(err)
	}

	// Log in as a different account.
	if err := SaveToken(jwtFor("b@acme.test")); err != nil {
		t.Fatal(err)
	}
	if ActiveEmail() != "b@acme.test" {
		t.Fatalf("active = %q, want b@acme.test", ActiveEmail())
	}
	// b is fresh — must NOT see a's environment.
	if envID, _, _, _, _ := LoadEnvironment(); envID != "" {
		t.Fatalf("new account inherited env %q (bug #3)", envID)
	}

	// Both accounts are stored; a's env is preserved.
	accts, _ := ListAccounts()
	if len(accts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(accts))
	}
	if err := SwitchAccount("a@acme.test"); err != nil {
		t.Fatal(err)
	}
	if envID, _, _, _, _ := LoadEnvironment(); envID != "env-a" {
		t.Fatalf("account a lost its env: %q", envID)
	}
}

func TestSwitchAndRemove(t *testing.T) {
	isolateHome(t)
	SaveToken(jwtFor("a@acme.test"))
	SaveToken(jwtFor("b@acme.test")) // active = b

	if err := SwitchAccount("missing@acme.test"); err == nil {
		t.Error("switching to unknown account should error")
	}
	if err := RemoveAccount("b@acme.test"); err != nil {
		t.Fatal(err)
	}
	// Removing the active account promotes the remaining one.
	if ActiveEmail() != "a@acme.test" {
		t.Fatalf("after removing active, active = %q, want a@acme.test", ActiveEmail())
	}
	if err := RemoveAllAccounts(); err != nil {
		t.Fatal(err)
	}
	if accts, _ := ListAccounts(); len(accts) != 0 {
		t.Fatalf("RemoveAllAccounts left %d", len(accts))
	}
}
