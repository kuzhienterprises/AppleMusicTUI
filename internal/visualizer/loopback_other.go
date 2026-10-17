//go:build !windows

package visualizer

import "errors"

// StartLoopback is Windows only. Callers get the stand-in animation instead.
func StartLoopback() (Monitor, error) {
	return nil, errors.New("system audio capture is only implemented on Windows")
}
