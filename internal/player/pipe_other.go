//go:build !windows

package player

import (
	"fmt"
	"net"
	"time"
)

// dialIPC connects to mpv's unix socket, retrying while mpv starts up.
func dialIPC(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no connection attempt succeeded")
	}
	return nil, fmt.Errorf("connecting to mpv's IPC socket %s: %w", path, lastErr)
}
