package visualizer

import (
	"math"
	"testing"
)

// bandCenterHz is the frequency a band is centred on, mirroring the layout
// NewSpectrum builds.
func bandCenterHz(index, sampleRate int) float64 {
	low := float64(lowestHz)
	high := math.Min(highestHz, float64(sampleRate)/2)
	lo := low * math.Pow(high/low, float64(index)/float64(BandCount))
	hi := low * math.Pow(high/low, float64(index+1)/float64(BandCount))
	return math.Sqrt(lo * hi)
}

func strongest(levels []float64) int {
	best := 0
	for i, v := range levels {
		if v > levels[best] {
			best = i
		}
	}
	return best
}

// A pure tone has to land in the band that owns its frequency, or the bars
// are decoration rather than a spectrum.
func TestSpectrumPlacesAToneInTheRightBand(t *testing.T) {
	for _, frequency := range []float64{100, 440, 1000, 4000} {
		analysis := NewSpectrum(48000)

		// Feed a second of audio through the analyser in chunks, the way the
		// capture loop does.
		var levels []float64
		for i := 0; i < 48000; i++ {
			value := 0.5 * math.Sin(2*math.Pi*frequency*float64(i)/48000)
			analysis.Push([]float64{value})
			if i%480 == 479 {
				levels = append([]float64(nil), analysis.Levels()...)
			}
		}

		index := strongest(levels)
		center := bandCenterHz(index, 48000)
		if ratio := center / frequency; ratio < 0.8 || ratio > 1.25 {
			t.Errorf("tone at %.0f Hz peaked in band %d (centred on %.0f Hz), off by %.2fx",
				frequency, index, center, ratio)
		}
	}
}

// Silence has to reach the floor, otherwise a paused player would leave the
// bars frozen mid-song.
func TestSpectrumFallsToSilence(t *testing.T) {
	analysis := NewSpectrum(48000)

	for i := 0; i < 48000; i++ {
		analysis.Push([]float64{0.5 * math.Sin(2*math.Pi*1000*float64(i)/48000)})
	}
	if levels := analysis.Levels(); strongest(levels) == 0 && levels[0] == 0 {
		t.Fatal("expected the tone to register before decaying")
	}

	// Roughly a second of silence, in 40 ms frames like the capture loop.
	for frame := 0; frame < 25; frame++ {
		analysis.Push(make([]float64, 1920))
		analysis.Levels()
	}

	for i, level := range analysis.Levels() {
		if level > 0.02 {
			t.Fatalf("band %d still at %.3f after a second of silence", i, level)
		}
	}
}

// Bands must come back inside the range the interface draws with.
func TestSpectrumLevelsStayNormalized(t *testing.T) {
	analysis := NewSpectrum(44100)

	for i := 0; i < 200000; i++ {
		value := math.Sin(2*math.Pi*220*float64(i)/44100)*0.9 +
			math.Sin(2*math.Pi*6000*float64(i)/44100)*0.4
		analysis.Push([]float64{value})
		if i%5000 == 4999 {
			for band, level := range analysis.Levels() {
				if level < 0 || level > 1 || math.IsNaN(level) {
					t.Fatalf("band %d out of range: %v", band, level)
				}
			}
		}
	}
}

func TestFFTMatchesDirectTransform(t *testing.T) {
	const n = 16
	re := make([]float64, n)
	im := make([]float64, n)
	for i := range re {
		re[i] = math.Sin(float64(i)) + 0.3*float64(i%3)
	}
	want := make([]complex128, n)
	for k := 0; k < n; k++ {
		var sum complex128
		for i := 0; i < n; i++ {
			angle := -2 * math.Pi * float64(k*i) / float64(n)
			sum += complex(re[i], im[i]) * complex(math.Cos(angle), math.Sin(angle))
		}
		want[k] = sum
	}

	fft(re, im)

	for k := 0; k < n; k++ {
		if math.Abs(re[k]-real(want[k])) > 1e-9 || math.Abs(im[k]-imag(want[k])) > 1e-9 {
			t.Fatalf("bin %d: got (%v,%v), want (%v,%v)", k, re[k], im[k], real(want[k]), imag(want[k]))
		}
	}
}
