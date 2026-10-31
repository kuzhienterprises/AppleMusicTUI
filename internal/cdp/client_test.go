package cdp

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeConn stands in for a socket whose read failed. Call only needs a write
// that succeeds and a read that fails the way a browser that went away fails.
type fakeConn struct {
	readErr error
}

func (c fakeConn) WriteJSON(any) error               { return nil }
func (c fakeConn) ReadMessage() (int, []byte, error) { return 0, nil, c.readErr }
func (c fakeConn) SetReadDeadline(time.Time) error   { return nil }
func (c fakeConn) Close() error                      { return nil }

// resetError is the text Windows produces when the remote end drops a socket.
type resetError struct{}

func (resetError) Error() string {
	return "read tcp 127.0.0.1:55351->127.0.0.1:49451: wsarecv: An existing connection was forcibly closed by the remote host."
}

func TestCallReportsABrowserThatWentAway(t *testing.T) {
	cases := map[string]error{
		"forcibly closed": resetError{},
		"eof":             io.EOF,
		"unexpected eof":  io.ErrUnexpectedEOF,
		"close frame":     &websocket.CloseError{Code: websocket.CloseAbnormalClosure},
	}
	for name, readErr := range cases {
		t.Run(name, func(t *testing.T) {
			client := NewClient(fakeConn{readErr: readErr})
			err := client.Call(context.Background(), "Runtime.evaluate", nil, nil)
			if !errors.Is(err, ErrGone) {
				t.Fatalf("err = %v, want ErrGone", err)
			}
		})
	}
}

// A browser that is slow is not a browser that is gone: a timeout must not be
// mistaken for one, or a busy machine would pay for a fresh boot every time.
func TestCallKeepsOtherReadFailures(t *testing.T) {
	client := NewClient(fakeConn{readErr: errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: i/o timeout")})

	err := client.Call(context.Background(), "Runtime.evaluate", nil, nil)
	if err == nil {
		t.Fatal("a failed read was reported as success")
	}
	if errors.Is(err, ErrGone) {
		t.Fatalf("a timeout was reported as the browser going away: %v", err)
	}
}
