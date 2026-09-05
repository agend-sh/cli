package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginPKCEAndExchange(t *testing.T) {
	verifier, challenge, err := loginPKCE()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(verifier))
	if len(verifier) != 43 || challenge != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("invalid PKCE pair")
	}
	next, _, err := loginPKCE()
	if err != nil || next == verifier {
		t.Fatal("verifier was reused", err)
	}
	code := strings.Repeat("a", 64)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/auth/cli/exchange" || r.URL.RawQuery != "" {
			t.Error("invalid exchange request")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["code"] != code || body["verifier"] != verifier {
			t.Error("missing exchange proof")
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "account-token"})
	}))
	defer server.Close()
	token, err := exchangeLoginCode(server.Client(), server.URL, code, verifier)
	if err != nil || token != "account-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if _, err := exchangeLoginCode(server.Client(), server.URL, "invalid", verifier); err == nil || calls != 1 {
		t.Fatal("invalid code reached server")
	}
}

func TestLoginExchangeRejectsRedirectAndMissingToken(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusOK} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(status)
			w.Write([]byte(`{}`))
		}))
		_, err := exchangeLoginCode(server.Client(), server.URL, strings.Repeat("a", 64), strings.Repeat("v", 43))
		server.Close()
		if err == nil {
			t.Fatalf("accepted HTTP %d without a token", status)
		}
	}
}
