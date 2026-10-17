package visualizer

import "math"

const (
	// BandCount is the number of frequency bands every frame carries.
	BandCount = 32

	// fftSize is the analysis window length in samples. 2048 at 48 kHz is
	// about 43 ms, which is fine enough to feel immediate and long enough to
	// resolve the bass.
	fftSize = 2048

	// lowestHz and highestHz bound the part of the spectrum that is drawn.
	// The top of the range is clipped to Nyquist for low sample rates.
	lowestHz  = 40
	highestHz = 16000

	// floorDB and ceilingDB are the levels that map to the bottom and top of
	// a bar. Everything outside is clamped, so the display never goes flat or
	// saturates completely.
	floorDB   = -85
	ceilingDB = -25

	// tiltDBPerOctave lifts the top of the spectrum. Real music falls off
	// with frequency, so without this every bar leans left.
	tiltDBPerOctave = 3

	// attack and release are the per-frame easing applied to the bars. The
	// bars jump up and sink slowly, which is what makes a spectrum readable.
	attack  = 0.55
	release = 0.16
)

// Spectrum turns a stream of mono samples into normalized band levels.
//
// It is not safe for concurrent use: the capture loop that feeds it owns it.
type Spectrum struct {
	sampleRate int
	window     []float64 // Hann coefficients
	ring       []float64 // rolling time-domain window, oldest at write
	write      int
	fresh      int // samples pushed since the last analysis
	levels     []float64
	edges      []int // FFT bin index for each band boundary
	tilt       []float64
	re, im     []float64
}

// NewSpectrum prepares an analyser for the given sample rate.
func NewSpectrum(sampleRate int) *Spectrum {
	if sampleRate <= 0 {
		sampleRate = 48000
	}

	s := &Spectrum{
		sampleRate: sampleRate,
		window:     make([]float64, fftSize),
		ring:       make([]float64, fftSize),
		levels:     make([]float64, BandCount),
		edges:      make([]int, BandCount+1),
		tilt:       make([]float64, BandCount),
		re:         make([]float64, fftSize),
		im:         make([]float64, fftSize),
	}

	// A Hann window keeps a signal that does not land on a bin centre from
	// smearing across the whole spectrum.
	for i := range s.window {
		s.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
	}

	top := math.Min(highestHz, float64(sampleRate)/2)
	binHz := float64(sampleRate) / float64(fftSize)
	low, high := float64(lowestHz), top
	for i := 0; i <= BandCount; i++ {
		freq := low * math.Pow(high/low, float64(i)/float64(BandCount))
		s.edges[i] = int(math.Round(freq / binHz))
	}
	for i := 0; i < BandCount; i++ {
		lo := low * math.Pow(high/low, float64(i)/float64(BandCount))
		hi := low * math.Pow(high/low, float64(i+1)/float64(BandCount))
		center := math.Sqrt(lo * hi)
		s.tilt[i] = tiltDBPerOctave * math.Log2(center/1000)
	}

	return s
}

// Push adds mono samples, which must be in the range -1..1.
func (s *Spectrum) Push(samples []float64) {
	for _, v := range samples {
		s.ring[s.write] = v
		s.write = (s.write + 1) % fftSize
	}
	s.fresh += len(samples)
}

// Levels returns the current band levels, each in 0..1, ordered from low
// frequency to high. The slice is owned by the analyser and is only valid
// until the next call.
func (s *Spectrum) Levels() []float64 {
	// Analysing on every display frame would be wasted work; a quarter of a
	// window of new audio is plenty to move the bars.
	if s.fresh >= fftSize/4 {
		s.fresh = 0
		s.analyse()
	}
	return s.levels
}

func (s *Spectrum) analyse() {
	// Oldest sample first, so the window reads left to right in time.
	for i := 0; i < fftSize; i++ {
		s.re[i] = s.ring[(s.write+i)%fftSize] * s.window[i]
		s.im[i] = 0
	}
	fft(s.re, s.im)

	bins := fftSize / 2
	// A full-scale sine comes out of this FFT with a magnitude of fftSize/2,
	// so scaling by 2/fftSize leaves amplitude in 0..1 and makes the dB
	// thresholds below meaningful.
	scale := 2 / float64(fftSize)

	for band := 0; band < BandCount; band++ {
		lo, hi := s.edges[band], s.edges[band+1]
		if hi <= lo {
			hi = lo + 1
		}
		if lo >= bins {
			lo = bins - 1
		}
		if hi > bins {
			hi = bins
		}

		sum := 0.0
		for k := lo; k < hi; k++ {
			sum += math.Hypot(s.re[k], s.im[k]) * scale
		}
		mean := sum / float64(hi-lo)

		db := 20*math.Log10(mean+1e-12) + s.tilt[band]
		target := clamp01((db - floorDB) / (ceilingDB - floorDB))

		ease := release
		if target > s.levels[band] {
			ease = attack
		}
		s.levels[band] += (target - s.levels[band]) * ease
	}
}

// fft replaces re/im with their in-place radix-2 complex Fourier transform.
// Both slices must have the same power-of-two length.
func fft(re, im []float64) {
	n := len(re)

	// Reorder into bit-reversed index order so the butterflies below can run
	// bottom-up.
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}

	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		wr, wi := math.Cos(angle), math.Sin(angle)

		for start := 0; start < n; start += length {
			cr, ci := 1.0, 0.0
			for k := 0; k < length/2; k++ {
				a := start + k
				b := a + length/2

				vr := re[b]*cr - im[b]*ci
				vi := re[b]*ci + im[b]*cr

				re[b], im[b] = re[a]-vr, im[a]-vi
				re[a], im[a] = re[a]+vr, im[a]+vi

				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
}

func clamp01(v float64) float64 {
	switch {
	case v != v: // NaN
		return 0
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}
