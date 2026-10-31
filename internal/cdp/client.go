// Package cdp is a small slice of the Chrome DevTools Protocol: enough to
// drive a browser we launched ourselves over its debugger WebSocket.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client is a single WebSocket connection to one browser target.
type Client struct {
	conn   wsConn
	mu     sync.Mutex
	nextID int
}

// wsConn is the part of a websocket connection this package needs. Keeping it
// behind an interface makes the protocol logic testable without a browser.
type wsConn interface {
	WriteJSON(v any) error
	ReadMessage() (int, []byte, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// NewClient wraps an already-connected websocket.
func NewClient(conn wsConn) *Client { return &Client{conn: conn} }

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

// ErrGone reports that the browser closed the connection: it crashed, was
// killed, or its page went away. The transport error underneath is kept
// because that is what a bug report needs, but on its own it says nothing a
// person can act on.
var ErrGone = errors.New("the player browser went away")

// closedConnection reports whether a read failed because the socket went away
// rather than because the command did. Gorilla hands back the transport error
// itself for a reset connection, an unexpected EOF for a truncated frame, and
// a CloseError when the browser closes the socket politely.
func closedConnection(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var closed *websocket.CloseError
	if errors.As(err, &closed) {
		return true
	}

	// Windows says "An existing connection was forcibly closed by the remote
	// host", which matches nothing above. A timeout is deliberately not in
	// this list: a browser that is slow is not a browser that is gone.
	text := strings.ToLower(err.Error())
	for _, phrase := range []string{"forcibly closed", "connection reset", "broken pipe", "use of closed network connection"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// Call sends a protocol command and decodes the reply into out.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := c.conn.WriteJSON(payload); err != nil {
		if closedConnection(err) {
			return fmt.Errorf("%w while sending %s (%v)", ErrGone, method, err)
		}
		return fmt.Errorf("sending %s: %w", method, err)
	}

	// The reply can be slow when the browser is busy: MusicKit's queue calls run
	// against a page that is decoding audio at the same time, and on a machine
	// under load that has been seen to take far longer than a call should. This
	// is patience, not a hang detector.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", method)
		}
		_ = c.conn.SetReadDeadline(deadline)

		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if closedConnection(err) {
				return fmt.Errorf("%w while waiting for %s (%v)", ErrGone, method, err)
			}
			return fmt.Errorf("reading the reply to %s: %w", method, err)
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
			continue // an event, or a reply to another command
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

// Evaluate runs JavaScript in the page and decodes the resulting value into
// out. Promises are awaited, so callers can use async scripts.
func (c *Client) Evaluate(ctx context.Context, expression string, out any) error {
	var reply struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}

	err := c.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, &reply)
	if err != nil {
		return err
	}
	if reply.ExceptionDetails != nil {
		detail := reply.ExceptionDetails.Text
		if reply.ExceptionDetails.Exception != nil && reply.ExceptionDetails.Exception.Description != "" {
			detail = reply.ExceptionDetails.Exception.Description
		}
		return fmt.Errorf("page script failed: %s", detail)
	}
	if out == nil || len(reply.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(reply.Result.Value, out)
}

// EvaluateBool runs a boolean expression in the page.
func (c *Client) EvaluateBool(ctx context.Context, expression string) (bool, error) {
	var value bool
	if err := c.Evaluate(ctx, expression, &value); err != nil {
		return false, err
	}
	return value, nil
}

// Record is a convenience for scripts that return an object.
func (c *Client) Record(ctx context.Context, expression string) (map[string]any, error) {
	var value map[string]any
	if err := c.Evaluate(ctx, expression, &value); err != nil {
		return nil, err
	}
	return value, nil
}
