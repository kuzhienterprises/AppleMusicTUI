//go:build windows

package player

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// dialIPC connects to an mpv named pipe, retrying while mpv finishes starting.
func dialIPC(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		conn, err := winio.DialPipe(path, nil)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("no attempt was made")
	}
	return nil, fmt.Errorf("connecting to mpv's IPC pipe %s: %w", path, lastErr)
}
