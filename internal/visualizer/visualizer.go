// Package visualizer turns what the machine is playing into a spectrum the
// player can draw.
//
// On Windows the levels come from WASAPI loopback on the default output
// device, so the bars follow the real audio. Everywhere else, and whenever
// the device cannot be opened, Start returns a clearly labelled stand-in
// animation rather than failing: a player should still draw something.
package visualizer

import (
	"math"
	"time"
)

// Monitor yields spectrum frames.
type Monitor interface {
	// Bands returns the most recent frame: BandCount levels in 0..1 from low
	// frequency to high. The caller owns the result.
	Bands() []float64
	// Live reports whether the frames come from real system audio. When it is
	// false the levels are a stand-in animation.
	Live() bool
	// Close releases the audio device.
	Close() error
}

// Start opens a monitor over whatever the machine is playing, falling back to
// a stand-in animation when system audio cannot be captured.
func Start() Monitor {
	if monitor, err := StartLoopback(); err == nil {
		return monitor
	}
	return newSimulated()
}

// simulated is the honest fallback: it is not capturing anything, it just
// moves in a way that reads as music. Monitors that are not Live say so in the
// interface.
type simulated struct {
	start  time.Time
	levels []float64
}

func newSimulated() *simulated {
	return &simulated{start: time.Now(), levels: make([]float64, BandCount)}
}

func (s *simulated) Live() bool { return false }

func (s *simulated) Close() error { return nil }

func (s *simulated) Bands() []float64 {
	t := time.Since(s.start).Seconds()

	// A beat that pushes the whole spectrum, energy that leans low, and a
	// little wobble per band so it never looks like a static picture.
	beat := 0.35 + 0.65*math.Pow(math.Abs(math.Sin(t*1.6)), 3)

	for i := range s.levels {
		position := float64(i) / float64(BandCount-1)
		energy := math.Exp(-2.6 * position)
		wobble := 0.55 + 0.45*math.Sin(t*2.1+float64(i)*0.7)

		target := clamp01(energy*wobble*beat + 0.03)
		s.levels[i] += (target - s.levels[i]) * 0.35
	}
	return s.levels
}
