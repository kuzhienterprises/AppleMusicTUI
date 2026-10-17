package cdp

import (
	"context"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// Dial connects to a browser target's debugger WebSocket.
func Dial(ctx context.Context, wsURL string) (*Client, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to the browser debugger: %w", err)
	}
	return NewClient(conn), nil
}
