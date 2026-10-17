// Package cdp is a small slice of the Chrome DevTools Protocol: enough to
// drive a browser we launched ourselves over its debugger WebSocket.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
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
