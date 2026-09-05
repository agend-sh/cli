package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var loginCodeRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func loginPKCE() (verifier, challenge string, err error) {
	var data [32]byte
	if _, err = rand.Read(data[:]); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(data[:])
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func exchangeLoginCode(client *http.Client, base, code, verifier string) (string, error) {
	if !loginCodeRE.MatchString(code) {
		return "", fmt.Errorf("invalid login code")
	}
	if base == "" {
		base = "https://api.agend.sh"
	}
	body, err := json.Marshal(map[string]string{"code": code, "verifier": verifier})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/auth/cli/exchange", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copyClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("exchange login code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login exchange failed (%d); run agend login again", response.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil || result.Token == "" {
		return "", fmt.Errorf("invalid login exchange response")
	}
	return result.Token, nil
}
