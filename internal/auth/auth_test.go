package auth

import (
	"os"
	"testing"
)

func setupHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
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
}

func TestClearEnvironmentNoConfig(t *testing.T) {
	setupHome(t)

	if err := ClearEnvironment(); err != nil {
		t.Fatalf("ClearEnvironment on missing config should not error: %v", err)
	}
}

func TestSaveSessionTokenAppearsInLoadEnvironment(t *testing.T) {
	setupHome(t)

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
