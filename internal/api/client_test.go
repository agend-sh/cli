package api

import "testing"

func TestValidateBaseURL(t *testing.T) {
	valid := []string{
		"https://api.agend.sh",
		"https://api.example.com:8443",
		"http://localhost:8080",
		"http://127.0.0.1:3000",
		"http://[::1]:3000",
	}
	for _, u := range valid {
		if err := validateBaseURL(u); err != nil {
			t.Errorf("validateBaseURL(%q) = %v, want nil", u, err)
		}
	}

	invalid := []string{
		"http://api.agend.sh",
		"http://evil.example.com",
		"http://10.0.0.5:8080", // private but not loopback — still cleartext on a network
		"ftp://api.agend.sh",
		"api.agend.sh", // no scheme
	}
	for _, u := range invalid {
		if err := validateBaseURL(u); err == nil {
			t.Errorf("validateBaseURL(%q) = nil, want error", u)
		}
	}
}
