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

// V1 flat credentials must migrate into a v2 account without losing the
// token/env, and become the active account.
func TestMigrateV1ToV2(t *testing.T) {
	home := isolateHome(t)
	tok := jwtFor("old@acme.test")
	dir := filepath.Join(home, ".config", "agend")
	os.MkdirAll(dir, 0700)
	v1 := `{"token":"` + tok + `","env_id":"env-1","endpoint":"https://x","session_token":"st"}`
	os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(v1), 0600)

	if got, _ := LoadToken(); got != tok {
		t.Fatal("migrated token mismatch")
	}
	envID, endpoint, _, st, _ := LoadEnvironment()
	if envID != "env-1" || endpoint != "https://x" || st != "st" {
		t.Fatalf("migrated env lost: %q %q %q", envID, endpoint, st)
	}
	if ActiveEmail() != "old@acme.test" {
		t.Fatalf("active = %q, want old@acme.test", ActiveEmail())
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
