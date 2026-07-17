package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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

func requireNoEnvironmentAuthority(t *testing.T) {
	t.Helper()
	envID, endpoint, secret, session, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if envID != "" || endpoint != "" || secret != "" || session != "" {
		t.Fatalf("legacy environment authority escaped: env=%q endpoint=%q secret=%q session=%q",
			envID, endpoint, secret, session)
	}
	accounts, err := ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].EnvID != "" {
		t.Fatalf("legacy environment exposed by ListAccounts: %+v", accounts)
	}
}

// Flat credentials predate the control-plane marker. Their common account
// state remains usable, but their environment must never cross into v2.
func TestMigrateFlatV1ToV3DropsEnvironmentAuthority(t *testing.T) {
	home := isolateHome(t)
	tok := jwtFor("old@acme.test")
	writeCredentials(t, home, map[string]any{
		"token":         tok,
		"api_url":       "https://api.example.test",
		"env_id":        "env-1",
		"endpoint":      "https://old.example.test",
		"secret":        "old-secret",
		"session_token": "old-session",
	})

	if got, _ := LoadToken(); got != tok {
		t.Fatal("migrated token mismatch")
	}
	if got := LoadAPIURL(); got != "https://api.example.test" {
		t.Fatalf("migrated API URL = %q", got)
	}
	if ActiveEmail() != "old@acme.test" {
		t.Fatalf("active = %q, want old@acme.test", ActiveEmail())
	}
	requireNoEnvironmentAuthority(t)

	// A subsequent common-state write persists the sanitized v3 shape.
	if err := SaveToken(tok); err != nil {
		t.Fatal(err)
	}
	s, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != storeVersion {
		t.Fatalf("store version = %d, want %d", s.Version, storeVersion)
	}
	requireNoEnvironmentAuthority(t)
}

// Even an injected v2-looking marker is not authoritative in schema 2,
// because that schema never defined the generation boundary.
func TestMigrateSchema2ToV3DropsEnvironmentAuthority(t *testing.T) {
	home := isolateHome(t)
	tok := jwtFor("schema2@acme.test")
	writeCredentials(t, home, map[string]any{
		"version": 2,
		"active":  "schema2@acme.test",
		"api_url": "https://api.example.test",
		"accounts": map[string]any{
			"schema2@acme.test": map[string]any{
				"email":                 "schema2@acme.test",
				"token":                 tok,
				"env_id":                "env-schema2",
				"endpoint":              "https://old.example.test",
				"secret":                "old-secret",
				"session_token":         "old-session",
				"control_plane_version": "v2",
			},
		},
	})

	if got, _ := LoadToken(); got != tok {
		t.Fatal("schema2 token mismatch")
	}
	if got := LoadAPIURL(); got != "https://api.example.test" {
		t.Fatalf("schema2 API URL = %q", got)
	}
	if ActiveEmail() != "schema2@acme.test" {
		t.Fatalf("active = %q", ActiveEmail())
	}
	requireNoEnvironmentAuthority(t)

	// Session writes cannot turn a migrated account back into an environment.
	if err := SaveSessionToken("new-session"); err != nil {
		t.Fatal(err)
	}
	requireNoEnvironmentAuthority(t)
	s, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	a := s.Accounts["schema2@acme.test"]
	if s.Version != storeVersion || a == nil || a.Token != tok || a.Email != "schema2@acme.test" {
		t.Fatalf("safe schema2 account state was not preserved: version=%d account=%+v", s.Version, a)
	}
}

func TestStoreRequiresExactSchemaAndV2EnvironmentMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		marker  string
	}{
		{name: "unstamped", version: storeVersion},
		{name: "v1_marker", version: storeVersion, marker: "v1"},
		{name: "future_marker", version: storeVersion, marker: "v3"},
		{name: "unknown_marker", version: storeVersion, marker: "unknown"},
		{name: "unknown_schema_with_v2_marker", version: 99, marker: "v2"},
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

			requireNoEnvironmentAuthority(t)
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
