package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func makeJWT(t *testing.T, exp int64) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload, _ := json.Marshal(map[string]int64{"exp": exp})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return hdr + "." + body + ".sig"
}

func TestTokenExpired(t *testing.T) {
	now := time.Now().Unix()

	if !TokenExpired(makeJWT(t, now-3600)) {
		t.Error("token exp 1h ago should be expired")
	}
	if TokenExpired(makeJWT(t, now+3600)) {
		t.Error("token exp 1h ahead should not be expired")
	}
	// Opaque / non-JWT tokens are "unknown" → not treated as expired (defer to server).
	if TokenExpired("not-a-jwt") {
		t.Error("opaque token should not be reported expired")
	}
	if TokenExpired("") {
		t.Error("empty token should not be reported expired")
	}
}

func TestTokenExpiry(t *testing.T) {
	want := time.Now().Unix() + 1000
	got, ok := TokenExpiry(makeJWT(t, want))
	if !ok || got != want {
		t.Errorf("TokenExpiry = (%d, %v), want (%d, true)", got, ok, want)
	}
	if _, ok := TokenExpiry("garbage"); ok {
		t.Error("garbage token should return ok=false")
	}
}
