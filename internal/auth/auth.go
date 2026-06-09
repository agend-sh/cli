package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type config struct {
	Token        string `json:"token"`
	EnvID        string `json:"env_id,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Secret       string `json:"secret,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	APIURL       string `json:"api_url,omitempty"`
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agend", "credentials.json"), nil
}

func loadConfig() (*config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func saveConfig(cfg *config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

func SaveToken(token string) error {
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &config{}
	}
	cfg.Token = token
	return saveConfig(cfg)
}

func LoadToken() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}

	if cfg.Token == "" {
		return "", errors.New("no token found")
	}

	return cfg.Token, nil
}

func RemoveToken() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SaveEnvironment stores the active environment info.
func SaveEnvironment(envID, endpoint, secret string) error {
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &config{}
	}
	cfg.EnvID = envID
	cfg.Endpoint = endpoint
	cfg.Secret = secret
	return saveConfig(cfg)
}

// LoadEnvironment returns the active environment ID, endpoint, VM secret, and session token.
func LoadEnvironment() (envID, endpoint, secret, sessionToken string, err error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", "", "", "", err
	}
	return cfg.EnvID, cfg.Endpoint, cfg.Secret, cfg.SessionToken, nil
}

// SaveSessionToken persists the gRPC session token for reuse across CLI invocations.
// Clears the one-time secret since it was consumed to obtain this token.
func SaveSessionToken(token string) error {
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &config{}
	}
	cfg.SessionToken = token
	cfg.Secret = "" // consumed — never valid again
	return saveConfig(cfg)
}

// ClearSessionToken drops a stored gRPC session token without touching
// the rest of the environment record. Used by the retry path when reauth
// rotates the one-time secret — the old session token is no longer valid.
func ClearSessionToken() error {
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		return nil
	}
	cfg.SessionToken = ""
	return saveConfig(cfg)
}

// ClearEnvironment removes stored environment info.
func ClearEnvironment() error {
	cfg, err := loadConfig()
	if err != nil {
		return nil // nothing to clear
	}
	cfg.EnvID = ""
	cfg.Endpoint = ""
	cfg.Secret = ""
	cfg.SessionToken = ""
	return saveConfig(cfg)
}

// SaveAPIURL stores a custom API base URL (for dev/testing).
func SaveAPIURL(url string) error {
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &config{}
	}
	cfg.APIURL = url
	return saveConfig(cfg)
}

// LoadAPIURL returns the stored API URL or empty string for default.
func LoadAPIURL() string {
	cfg, _ := loadConfig()
	if cfg == nil {
		return ""
	}
	return cfg.APIURL
}

// BrowserLogin starts a local HTTP server, opens the browser for OAuth,
// and waits for the callback with the token.
func BrowserLogin() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("failed to start local server: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://localhost:%d/callback", port)
	authURL := fmt.Sprintf("https://agend.sh/auth/cli?callback=%s", callbackURL)

	openBrowser(authURL)

	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
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

	select {
	case token := <-tokenCh:
		server.Close()
		return token, nil
	case err := <-errCh:
		server.Close()
		return "", err
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
