package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// account holds one logged-in account's credentials and its active
// environment. Each account is independent, so operating on one never
// disturbs another.
type account struct {
	Email        string `json:"email,omitempty"`
	Token        string `json:"token"`
	EnvID        string `json:"env_id,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Secret       string `json:"secret,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
}

// store is the on-disk credentials file (v2): multiple accounts keyed by email
// plus a pointer to the active one, so `agend login` adds-not-clobbers and you
// can switch accounts without losing sessions. The v1 single-account flat
// format is migrated transparently on first load.
type store struct {
	Version  int                 `json:"version"`
	Active   string              `json:"active,omitempty"`
	Accounts map[string]*account `json:"accounts,omitempty"`
	APIURL   string              `json:"api_url,omitempty"`
}

const storeVersion = 2

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agend", "credentials.json"), nil
}

// accountKey derives the map key for a token: its JWT email claim, or
// "default" for an opaque/unparseable token (so migration still works).
func accountKey(token string) string {
	if email, ok := TokenEmail(token); ok {
		return email
	}
	return "default"
}

// loadStore reads the credentials file, migrating the v1 flat format if found.
// A missing file yields an empty store (not an error).
func loadStore() (*store, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &store{Version: storeVersion, Accounts: map[string]*account{}}, nil
	}
	if err != nil {
		return nil, err
	}

	var s store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Accounts != nil {
		return &s, nil // already v2
	}

	// v1 (flat single-account) → migrate into the v2 store.
	var legacy struct {
		Token        string `json:"token"`
		EnvID        string `json:"env_id"`
		Endpoint     string `json:"endpoint"`
		Secret       string `json:"secret"`
		SessionToken string `json:"session_token"`
		APIURL       string `json:"api_url"`
	}
	_ = json.Unmarshal(data, &legacy)
	migrated := &store{Version: storeVersion, Accounts: map[string]*account{}, APIURL: legacy.APIURL}
	if legacy.Token != "" {
		key := accountKey(legacy.Token)
		migrated.Accounts[key] = &account{
			Email:        key,
			Token:        legacy.Token,
			EnvID:        legacy.EnvID,
			Endpoint:     legacy.Endpoint,
			Secret:       legacy.Secret,
			SessionToken: legacy.SessionToken,
		}
		migrated.Active = key
	}
	return migrated, nil
}

func saveStore(s *store) error {
	s.Version = storeVersion
	if s.Accounts == nil {
		s.Accounts = map[string]*account{}
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: temp file in the same directory, then rename. A crash
	// mid-write must never leave a truncated credentials file behind.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// activeAccount returns the currently-active account, or nil if none.
func activeAccount(s *store) *account {
	if s.Active == "" {
		return nil
	}
	return s.Accounts[s.Active]
}

// mutateActive applies fn to the active account and persists. No-op if there
// is no active account.
func mutateActive(fn func(*account)) error {
	s, err := loadStore()
	if err != nil {
		return err
	}
	a := activeAccount(s)
	if a == nil {
		return nil
	}
	fn(a)
	return saveStore(s)
}

// SaveToken records a freshly-obtained token under its account (keyed by the
// JWT email claim) and makes that account active — without discarding any
// other account or this account's own existing environment. This is what makes
// `agend login` add-not-clobber, so switching accounts never inherits another
// account's environment.
func SaveToken(token string) error {
	if token == "" {
		return errors.New("empty token")
	}
	s, err := loadStore()
	if err != nil {
		s = &store{Version: storeVersion, Accounts: map[string]*account{}}
	}
	if s.Accounts == nil {
		s.Accounts = map[string]*account{}
	}
	key := accountKey(token)
	a := s.Accounts[key]
	if a == nil {
		a = &account{Email: key}
		s.Accounts[key] = a
	}
	a.Token = token
	a.Email = key
	s.Active = key
	return saveStore(s)
}

func LoadToken() (string, error) {
	s, err := loadStore()
	if err != nil {
		return "", err
	}
	a := activeAccount(s)
	if a == nil || a.Token == "" {
		return "", errors.New("no token found")
	}
	return a.Token, nil
}

// RemoveToken logs out the active account: it is removed and, if other accounts
// remain, one becomes active. The file is deleted only when no accounts remain.
func RemoveToken() error {
	s, err := loadStore()
	if err != nil {
		return nil
	}
	if s.Active != "" {
		delete(s.Accounts, s.Active)
		s.Active = ""
		for k := range s.Accounts {
			s.Active = k
			break
		}
	}
	if len(s.Accounts) == 0 {
		path, perr := configPath()
		if perr == nil {
			_ = os.Remove(path)
		}
		return nil
	}
	return saveStore(s)
}

// TokenExpiry parses the `exp` claim of a JWT and returns it as a Unix
// timestamp (seconds). ok is false if the token isn't a parseable JWT with an
// exp claim — callers treat that as "can't tell locally, let the server
// decide" rather than forcing a re-login on an opaque token.
func TokenExpiry(token string) (exp int64, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return 0, false
	}
	return claims.Exp, true
}

// TokenExpired reports whether the stored session token is a JWT whose exp has
// passed. A non-JWT or claimless token returns false (unknown → defer to the
// server). Used to fail fast with a clear "session expired" message instead of
// 401-looping or hanging on a stale endpoint.
func TokenExpired(token string) bool {
	exp, ok := TokenExpiry(token)
	if !ok {
		return false
	}
	return time.Now().Unix() >= exp
}

// TokenEmail parses the `email` claim of a JWT. ok is false if the token isn't
// a parseable JWT with an email claim. Used to key accounts by email.
func TokenEmail(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Email == "" {
		return "", false
	}
	return claims.Email, true
}

// SaveEnvironment stores the active account's active environment.
func SaveEnvironment(envID, endpoint, secret string) error {
	return mutateActive(func(a *account) {
		a.EnvID = envID
		a.Endpoint = endpoint
		a.Secret = secret
	})
}

// LoadEnvironment returns the active account's environment ID, endpoint, VM
// secret, and session token.
func LoadEnvironment() (envID, endpoint, secret, sessionToken string, err error) {
	s, err := loadStore()
	if err != nil {
		return "", "", "", "", err
	}
	a := activeAccount(s)
	if a == nil {
		return "", "", "", "", nil
	}
	return a.EnvID, a.Endpoint, a.Secret, a.SessionToken, nil
}

// SaveSessionToken persists the gRPC session token for reuse across CLI invocations.
// Clears the one-time secret since it was consumed to obtain this token.
func SaveSessionToken(token string) error {
	return mutateActive(func(a *account) {
		a.SessionToken = token
		a.Secret = "" // consumed — never valid again
	})
}

// ClearSessionToken drops a stored gRPC session token without touching
// the rest of the environment record. Used by the retry path when reauth
// rotates the one-time secret — the old session token is no longer valid.
func ClearSessionToken() error {
	return mutateActive(func(a *account) { a.SessionToken = "" })
}

// ClearEnvironment removes the active account's environment info.
func ClearEnvironment() error {
	return mutateActive(func(a *account) {
		a.EnvID = ""
		a.Endpoint = ""
		a.Secret = ""
		a.SessionToken = ""
	})
}

// SaveAPIURL stores a custom API base URL (for dev/testing). It is global
// (not per-account).
func SaveAPIURL(url string) error {
	s, err := loadStore()
	if err != nil {
		s = &store{Version: storeVersion, Accounts: map[string]*account{}}
	}
	s.APIURL = url
	return saveStore(s)
}

// LoadAPIURL returns the stored API URL or empty string for default.
func LoadAPIURL() string {
	s, err := loadStore()
	if err != nil {
		return ""
	}
	return s.APIURL
}

// BrowserLogin starts a local HTTP server, opens the browser for OAuth,
// and waits for the callback with the token.
func BrowserLogin() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("failed to start local server: %w", err)
	}
	defer listener.Close()

	// Unguessable state nonce, embedded in the callback path. It travels
	// CLI → browser → agend.sh → back to the loopback server, so only the
	// flow this CLI started can hit the callback — a malicious local web
	// page racing the loopback port can't log the user into an
	// attacker-controlled account.
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generate state nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	// The callback URL uses the literal 127.0.0.1 the listener is bound to —
	// "localhost" can resolve to ::1 first (common on Windows), and a browser
	// that doesn't fall back to IPv4 would get connection-refused.
	port := listener.Addr().(*net.TCPAddr).Port
	callbackPath := "/callback/" + nonce
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath)
	authURL := "https://agend.sh/auth/cli?callback=" + url.QueryEscape(callbackURL)

	openBrowser(authURL)

	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback/", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Path), []byte(callbackPath)) != 1 {
			http.Error(w, "Invalid callback", http.StatusForbidden)
			return
		}

		token := r.URL.Query().Get("token")
		if token == "" {
			errCh <- errors.New("no token in callback")
			http.Error(w, "Missing token", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="font-family:system-ui;text-align:center;padding:4rem">
			<h2>Authenticated!</h2><p>You can close this tab and return to your terminal.</p>
		</body></html>`)

		tokenCh <- token
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	// Bounded wait — if the browser never completes the flow (blocked popup,
	// closed tab, IPv6-only resolver), fail with guidance instead of hanging
	// the terminal forever.
	select {
	case token := <-tokenCh:
		return token, nil
	case err := <-errCh:
		return "", err
	case <-time.After(5 * time.Minute):
		return "", errors.New("timed out waiting for browser login — try again, or use 'agend login --email you@example.com'")
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
