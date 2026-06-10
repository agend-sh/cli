package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

type Client struct {
	conn  *grpc.ClientConn
	Agent pb.AgentServiceClient

	mu              sync.Mutex
	secret          string             // one-time secret (cleared after handshake)
	token           string             // session token (set after handshake)
	OnTokenReceived func(token string) // called when session token is received
}

func Dial(ctx context.Context, addr, secret, sessionToken string) (*Client, error) {
	c := &Client{secret: secret, token: sessionToken}

	var opts []grpc.DialOption
	dialTimeout := 10 * time.Second

	if needsTCPTunnel(addr) {
		// WebSocket tunnel — connect gRPC through Cloudflare TCP tunnel via WS
		hostname := strings.TrimPrefix(addr, "https://")
		hostname = strings.TrimSuffix(hostname, "/")
		opts = append(opts, grpc.WithContextDialer(wsTunnelDialer(hostname)))
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		addr = hostname // gRPC needs an address string but the dialer ignores it
	} else if needsTLS(addr) {
		// TLS mode for named tunnels or other HTTPS endpoints
		addr = strings.TrimPrefix(addr, "https://")
		addr = strings.TrimSuffix(addr, "/")
		host := addr
		if h, _, err := net.SplitHostPort(addr); err == nil {
			host = h
		}
		if !strings.Contains(addr, ":") {
			addr = addr + ":443"
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})))
	} else {
		// Plaintext gRPC. The auth interceptor attaches the session token to
		// every call, so allowing this for arbitrary hosts would leak
		// credentials to anything that controls the stored endpoint (e.g. a
		// tampered credentials.json or a malicious control-plane response).
		// Only loopback/private addresses are allowed, unless the user
		// explicitly opts in via AGEND_INSECURE_TRANSPORT=1.
		if !isPrivateAddr(addr) && os.Getenv("AGEND_INSECURE_TRANSPORT") != "1" {
			return nil, fmt.Errorf("refusing plaintext gRPC to non-private address %q — use an https:// endpoint, or set AGEND_INSECURE_TRANSPORT=1 if you really want this", addr)
		}
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	opts = append(opts, grpc.WithBlock())
	opts = append(opts, grpc.WithUnaryInterceptor(c.authInterceptor()))
	// Match the daemon's 16MB limits so 10MB chunked transfers aren't rejected
	// by gRPC's 4MB default on either the request (FilePutChunked) or the
	// response (FileGetChunked).
	const maxMsgSize = 16 << 20
	opts = append(opts, grpc.WithDefaultCallOptions(
		grpc.MaxCallRecvMsgSize(maxMsgSize),
		grpc.MaxCallSendMsgSize(maxMsgSize),
	))

	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, opts...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("environment unreachable — it may have stopped or the tunnel expired. Run 'agend env status' to check")
		}
		return nil, fmt.Errorf("dial agentd at %s: %w", addr, err)
	}

	c.conn = conn
	c.Agent = pb.NewAgentServiceClient(conn)

	return c, nil
}

// needsTCPTunnel returns true if the address is a Cloudflare quick tunnel
// that requires WebSocket proxying (TCP mode tunnels).
func needsTCPTunnel(addr string) bool {
	lower := strings.ToLower(addr)
	lower = strings.TrimPrefix(lower, "https://")
	return strings.HasSuffix(lower, ".trycloudflare.com") ||
		strings.Contains(lower, ".trycloudflare.com:")
}

// isPrivateAddr reports whether addr points at a loopback, RFC 1918,
// link-local, or unique-local destination — the only places plaintext gRPC
// is acceptable (local dev daemons, TAP-networked dev VMs). Hostnames other
// than "localhost" are NOT resolved: a DNS name could re-resolve to a public
// host, so anything non-literal is treated as public.
func isPrivateAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// needsTLS returns true if the address is a tunnel or cloud endpoint requiring TLS.
func needsTLS(addr string) bool {
	if strings.HasPrefix(addr, "https://") {
		return true
	}
	lower := strings.ToLower(addr)
	return strings.HasSuffix(lower, ".agend.sh") ||
		strings.Contains(lower, ".agend.sh:")
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// authInterceptor injects auth headers and captures session tokens from responses.
func (c *Client) authInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		c.mu.Lock()
		token := c.token
		secret := c.secret
		c.mu.Unlock()

		// Attach auth metadata
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-session-token", token)
		} else if secret != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-one-time-secret", secret)
		}

		// Capture trailing metadata for session token
		var trailer metadata.MD
		opts = append(opts, grpc.Trailer(&trailer))

		err := invoker(ctx, method, req, reply, cc, opts...)

		// Extract session token from response
		if tokens := trailer.Get("x-session-token"); len(tokens) > 0 {
			c.mu.Lock()
			c.token = tokens[0]
			c.secret = "" // no longer needed
			cb := c.OnTokenReceived
			c.mu.Unlock()
			if cb != nil {
				cb(tokens[0])
			}
		}

		return err
	}
}
