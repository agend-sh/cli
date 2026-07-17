package auth

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"
)

func setupHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)        // unix
	t.Setenv("USERPROFILE", tmp) // windows — os.UserHomeDir reads this there
}

func TestSaveAndLoadToken(t *testing.T) {
	setupHome(t)

	if err := SaveToken("tok_abc123"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != "tok_abc123" {
		t.Errorf("LoadToken = %q, want %q", got, "tok_abc123")
	}
}

func TestLoadTokenNoConfig(t *testing.T) {
	setupHome(t)

	_, err := LoadToken()
	if err == nil {
		t.Fatal("LoadToken should fail when no config exists")
	}
}

func TestRemoveTokenNoConfig(t *testing.T) {
	setupHome(t)

	if err := RemoveToken(); err != nil {
		t.Fatalf("RemoveToken on missing config should not error: %v", err)
	}
}

func TestRemoveTokenDeletesFile(t *testing.T) {
	setupHome(t)

	if err := SaveToken("tok_delete_me"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := RemoveToken(); err != nil {
		t.Fatalf("RemoveToken: %v", err)
	}

	_, err := LoadToken()
	if err == nil {
		t.Fatal("LoadToken should fail after RemoveToken")
	}
}

func TestSaveAndLoadEnvironment(t *testing.T) {
	setupHome(t)

	// Env state belongs to the active account, so log in first.
	if err := SaveToken("tok_env"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveEnvironment("env-1", "host:443", "s3cret"); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}

	envID, endpoint, secret, sessionToken, err := LoadEnvironment()
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if envID != "env-1" || endpoint != "host:443" || secret != "s3cret" {
		t.Errorf("got env=(%q,%q,%q), want (env-1, host:443, s3cret)", envID, endpoint, secret)
	}
	if sessionToken != "" {
		t.Errorf("sessionToken = %q, want empty", sessionToken)
	}

	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s store
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	a := activeAccount(&s)
	if s.Version != storeVersion || a == nil || a.ControlPlaneVersion != v2ControlPlaneVersion {
		t.Fatalf("saved environment lacks v2 generation marker: version=%d account=%+v", s.Version, a)
	}
}

func TestClearEnvironmentPreservesToken(t *testing.T) {
	setupHome(t)

	if err := SaveToken("tok_keep"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveEnvironment("env-2", "ep", "sec"); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}

	if err := ClearEnvironment(); err != nil {
		t.Fatalf("ClearEnvironment: %v", err)
	}

	tok, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken after ClearEnvironment: %v", err)
	}
	if tok != "tok_keep" {
		t.Errorf("token = %q, want %q", tok, "tok_keep")
	}

	envID, endpoint, secret, sessionToken, err := LoadEnvironment()
	if err != nil {
		t.Fatalf("LoadEnvironment after clear: %v", err)
	}
	if envID != "" || endpoint != "" || secret != "" || sessionToken != "" {
		t.Errorf("env fields should be empty after clear, got (%q,%q,%q,%q)", envID, endpoint, secret, sessionToken)
	}
	s, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}
	if a := activeAccount(s); a == nil || a.ControlPlaneVersion != "" {
		t.Fatalf("ClearEnvironment retained generation marker: %+v", a)
	}
}

func TestClearEnvironmentNoConfig(t *testing.T) {
	setupHome(t)

	if err := ClearEnvironment(); err != nil {
		t.Fatalf("ClearEnvironment on missing config should not error: %v", err)
	}
}

func TestSaveSessionTokenAppearsInLoadEnvironment(t *testing.T) {
	setupHome(t)

	if err := SaveToken("tok_sess"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveEnvironment("env-3", "ep3", "sec3"); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	if err := SaveSessionToken("sess_xyz"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	_, _, _, sessionToken, err := LoadEnvironment()
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if sessionToken != "sess_xyz" {
		t.Errorf("sessionToken = %q, want %q", sessionToken, "sess_xyz")
	}
}

func TestSaveEnvironmentFreshSecretClearsSessionToken(t *testing.T) {
	setupHome(t)

	if err := SaveToken("tok_rotate"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveEnvironment("env-rotate", "old:443", "old-secret"); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	if err := SaveSessionToken("old-session"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	// Wake/reauth returns a fresh one-time secret. The old session must not
	// shadow it because the gRPC client gives session tokens precedence.
	if err := SaveEnvironment("env-rotate", "new:443", "new-secret"); err != nil {
		t.Fatalf("SaveEnvironment after rotation: %v", err)
	}
	_, endpoint, secret, sessionToken, err := LoadEnvironment()
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if endpoint != "new:443" || secret != "new-secret" || sessionToken != "" {
		t.Fatalf("rotated credentials = endpoint %q, secret %q, session %q", endpoint, secret, sessionToken)
	}
}

func TestSaveEnvironmentEmptySecretPreservesSessionToken(t *testing.T) {
	setupHome(t)

	if err := SaveToken("tok_refresh"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveEnvironment("env-refresh", "old:443", "secret"); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	if err := SaveSessionToken("active-session"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	// An endpoint-only refresh must keep an otherwise-valid session.
	if err := SaveEnvironment("env-refresh", "new:443", ""); err != nil {
		t.Fatalf("SaveEnvironment after endpoint refresh: %v", err)
	}
	_, endpoint, secret, sessionToken, err := LoadEnvironment()
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if endpoint != "new:443" || secret != "" || sessionToken != "active-session" {
		t.Fatalf("refreshed credentials = endpoint %q, secret %q, session %q", endpoint, secret, sessionToken)
	}
}

func TestSaveSessionTokenForEnvironmentDoesNotClobberAnotherEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveToken("opaque-token"); err != nil {
		t.Fatal(err)
	}
	if err := SaveEnvironment("env-active", "active:443", "active-secret"); err != nil {
		t.Fatal(err)
	}

	saved, err := SaveSessionTokenForEnvironment("env-other", "active-secret", "", "other-session")
	if err != nil {
		t.Fatal(err)
	}
	if saved {
		t.Fatal("session token for a different environment was persisted")
	}
	envID, _, secret, session, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if envID != "env-active" || secret != "active-secret" || session != "" {
		t.Fatalf("active credentials changed: env=%q secret=%q session=%q", envID, secret, session)
	}

	saved, err = SaveSessionTokenForEnvironment("env-active", "active-secret", "", "active-session")
	if err != nil {
		t.Fatal(err)
	}
	if !saved {
		t.Fatal("matching environment session token was not persisted")
	}
	_, _, secret, session, err = LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" || session != "active-session" {
		t.Fatalf("persisted credentials = secret %q session %q", secret, session)
	}

	if err := SaveEnvironment("env-active", "active:443", "newer-secret"); err != nil {
		t.Fatal(err)
	}
	saved, err = SaveSessionTokenForEnvironment("env-active", "active-secret", "", "late-old-session")
	if err != nil {
		t.Fatal(err)
	}
	if saved {
		t.Fatal("late session token replaced a newer one-time secret")
	}
	_, _, secret, session, err = LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "newer-secret" || session != "" {
		t.Fatalf("newer credentials changed: secret %q session %q", secret, session)
	}
}

func TestSaveAndLoadAPIURL(t *testing.T) {
	setupHome(t)

	if err := SaveAPIURL("http://localhost:8080"); err != nil {
		t.Fatalf("SaveAPIURL: %v", err)
	}

	got := LoadAPIURL()
	if got != "http://localhost:8080" {
		t.Errorf("LoadAPIURL = %q, want %q", got, "http://localhost:8080")
	}
}

func TestLoadAPIURLNoConfig(t *testing.T) {
	setupHome(t)

	got := LoadAPIURL()
	if got != "" {
		t.Errorf("LoadAPIURL with no config = %q, want empty", got)
	}
}

func TestConfigFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits are not meaningful on Windows (ACLs protect the profile dir)")
	}
	setupHome(t)

	if err := SaveToken("tok_perm"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	path, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %04o, want 0600", perm)
	}
}
