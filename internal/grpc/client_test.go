package grpc

import "testing"

func TestNeedsTCPTunnel(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"https://abc-def.trycloudflare.com", true},          // legacy quick tunnel
		{"abc.trycloudflare.com:443", true},                  // quick tunnel with port
		{"https://et-srv-hetzner-01-03.agend.sh", true},      // named pool tunnel
		{"et-srv-hetzner-01-03.agend.sh:443", true},          // named pool tunnel + port
		{"https://srv-hetzner-01-ctl.agend.sh", false},       // control plane (HTTP, not a TCP env tunnel)
		{"https://api.agend.sh", false},                      // control plane API
		{"localhost:50051", false},                           // local dev
	}
	for _, c := range cases {
		if got := needsTCPTunnel(c.addr); got != c.want {
			t.Errorf("needsTCPTunnel(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestIsPrivateAddr(t *testing.T) {
	private := []string{
		"localhost:50051",
		"localhost",
		"127.0.0.1:50051",
		"[::1]:50051",
		"10.1.2.3:50051",
		"172.16.0.2:50051", // dev VM TAP network
		"192.168.1.10:50051",
		"169.254.10.10:50051",
	}
	for _, addr := range private {
		if !isPrivateAddr(addr) {
			t.Errorf("isPrivateAddr(%q) = false, want true", addr)
		}
	}

	public := []string{
		"example.com:50051",
		"evil.example.com",
		"8.8.8.8:50051",
		"203.0.113.7:50051",
		"my-host.internal:50051", // DNS names are never trusted
	}
	for _, addr := range public {
		if isPrivateAddr(addr) {
			t.Errorf("isPrivateAddr(%q) = true, want false", addr)
		}
	}
}
