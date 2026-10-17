package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// A very small slice of the Chrome DevTools Protocol: enough to read cookies
// from a browser we launched ourselves.
type cdpClient struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	nextID int
}

type cdpCookie struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Domain  string  `json:"domain"`
	Path    string  `json:"path"`
	Expires float64 `json:"expires"`
}

func dialCDP(ctx context.Context, wsURL string) (*cdpClient, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to the browser debugger: %w", err)
	}
	return &cdpClient{conn: conn}, nil
}

func (c *cdpClient) close() {
	_ = c.conn.Close()
}

// call issues a CDP command and waits for the matching reply, discarding
// events that arrive in between.
func (c *cdpClient) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := c.conn.WriteJSON(payload); err != nil {
		return fmt.Errorf("sending %s: %w", method, err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", method)
		}
		_ = c.conn.SetReadDeadline(deadline)

		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("reading reply to %s: %w", method, err)
		}

		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			continue
		}
		if message.ID != id {
			continue // an event or a reply to something else
		}
		if message.Error != nil {
			return fmt.Errorf("%s failed: %s", method, message.Error.Message)
		}
		if out != nil && len(message.Result) > 0 {
			return json.Unmarshal(message.Result, out)
		}
		return nil
	}
}

// cookies returns every cookie in the browser's cookie jar.
func (c *cdpClient) cookies(ctx context.Context) ([]cdpCookie, error) {
	var result struct {
		Cookies []cdpCookie `json:"cookies"`
	}
	if err := c.call(ctx, "Storage.getCookies", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Cookies, nil
}

// findCookie looks for a named cookie on an apple.com domain.
func findCookie(cookies []cdpCookie, name string) (string, bool) {
	for _, cookie := range cookies {
		if cookie.Name != name {
			continue
		}
		if cookie.Domain != "" && !isAppleDomain(cookie.Domain) {
			continue
		}
		if cookie.Value == "" {
			continue
		}
		return cookie.Value, true
	}
	return "", false
}

func isAppleDomain(domain string) bool {
	trimmed := domain
	if len(trimmed) > 0 && trimmed[0] == '.' {
		trimmed = trimmed[1:]
	}
	switch trimmed {
	case "apple.com":
		return true
	}
	const suffix = ".apple.com"
	if len(trimmed) > len(suffix) && trimmed[len(trimmed)-len(suffix):] == suffix {
		return true
	}
	return false
}
