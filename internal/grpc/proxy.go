package grpc

import (
	"context"
	"fmt"
	"net"

	"nhooyr.io/websocket"
)

// wsTunnelDialer returns a gRPC ContextDialer that connects through a
// Cloudflare TCP tunnel via WebSocket. Each gRPC connection dials a new
// WebSocket to the tunnel hostname — gRPC multiplexes over one connection.
//
// Uses nhooyr.io/websocket which handles Cloudflare's HTTP/1.1 upgrade
// correctly. The older golang.org/x/net/websocket fails with "bad status"
// because it can't negotiate with Cloudflare's edge.
func wsTunnelDialer(hostname string) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		wsURL := "wss://" + hostname + "/"
		ws, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			return nil, fmt.Errorf("tunnel connection to %s failed — environment may be stopped or expired. Run 'agend env wake' or 'agend env create': %w", hostname, err)
		}
		return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
	}
}
