package visualizer

import (
	"math"
	"testing"
	"time"
)

// Start has to hand back something usable whatever the machine looks like:
// a real capture where that is possible, and a stand-in where it is not.
func TestStartReturnsUsableFrames(t *testing.T) {
	monitor := Start()
	defer monitor.Close()

	if got := len(monitor.Bands()); got != BandCount {
		t.Fatalf("frame has %d bands, want %d", got, BandCount)
	}

	// Let the capture run, then confirm the frames stay well formed. Values
	// outside 0..1 would be drawn as garbage.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		for band, level := range monitor.Bands() {
			if level < 0 || level > 1 || math.IsNaN(level) {
				t.Fatalf("band %d out of range: %v", band, level)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Closing twice must not panic, because the player defers a close and a failed
// startup closes too.
func TestMonitorCloseIsSafe(t *testing.T) {
	monitor, err := StartLoopback()
	if err != nil {
		t.Skipf("system audio capture unavailable here: %v", err)
	}
	if !monitor.Live() {
		t.Error("a loopback monitor should report itself as live")
	}
	if err := monitor.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
