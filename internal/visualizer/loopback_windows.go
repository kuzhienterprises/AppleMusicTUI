//go:build windows

package visualizer

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

const (
	// frameInterval is how often the capture loop produces a frame.
	frameInterval = 40 * time.Millisecond

	// bufferDuration is the requested loopback buffer, in 100 ns units.
	bufferDuration = wca.REFERENCE_TIME(100 * 10000)

	// audclntBufferSilent marks a packet the engine left unfilled.
	audclntBufferSilent = 0x2
)

// StartLoopback captures the default output device with WASAPI loopback, so
// the bars follow the real audio no matter which player produced it.
//
// It is only implemented on Windows.
func StartLoopback() (Monitor, error) {
	monitor := &loopback{
		levels: make([]float64, BandCount),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	// COM is per-thread, so the whole capture has to live on one thread. The
	// goroutine reports whether it managed to open the device before the
	// caller is handed a monitor that would never produce anything.
	setup := make(chan error, 1)
	go monitor.run(setup)
	if err := <-setup; err != nil {
		<-monitor.done
		return nil, err
	}
	return monitor, nil
}

type loopback struct {
	mu     sync.Mutex
	levels []float64

	stop chan struct{}
	done chan struct{}

	stopOnce sync.Once
}

func (l *loopback) Bands() []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]float64, len(l.levels))
	copy(out, l.levels)
	return out
}

// Live always reports true: a monitor that exists is capturing.
func (l *loopback) Live() bool { return true }

func (l *loopback) Close() error {
	l.stopOnce.Do(func() { close(l.stop) })

	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (l *loopback) run(setup chan<- error) {
	defer close(l.done)

	// Without this the runtime may migrate the goroutine to another thread,
	// or reuse the thread, and the COM objects below would belong to an
	// apartment that no longer exists.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		setup <- fmt.Errorf("initializing COM: %w", err)
		return
	}
	defer ole.CoUninitialize()

	client, capture, format, err := openLoopback()
	if err != nil {
		setup <- err
		return
	}
	defer func() {
		_ = client.Stop()
		capture.Release()
		client.Release()
	}()

	if err := client.Start(); err != nil {
		setup <- fmt.Errorf("starting capture: %w", err)
		return
	}
	setup <- nil

	sampleRate := int(format.NSamplesPerSec)
	analysis := NewSpectrum(sampleRate)
	mix := newMixer(format)

	// When the device is quiet WASAPI may hand back nothing at all, and a
	// stale analysis window would leave the bars frozen mid-song forever.
	// Feeding a tick's worth of silence keeps the window moving so the bars
	// fall as they should.
	silence := make([]float64, sampleRate*int(frameInterval/time.Millisecond)/1000)

	samples := make([]float64, 0, 8192)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			samples = drainCapture(capture, mix, samples[:0])
			if len(samples) == 0 {
				samples = silence
			}
			analysis.Push(samples)

			l.mu.Lock()
			copy(l.levels, analysis.Levels())
			l.mu.Unlock()
		}
	}
}

// openLoopback wires up a capture stream on the default render endpoint.
func openLoopback() (*wca.IAudioClient, *wca.IAudioCaptureClient, *wca.WAVEFORMATEX, error) {
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return nil, nil, nil, fmt.Errorf("opening the audio device enumerator: %w", err)
	}

	var device *wca.IMMDevice
	if err := enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &device); err != nil {
		return nil, nil, nil, fmt.Errorf("finding the default output device: %w", err)
	}

	var client *wca.IAudioClient
	if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &client); err != nil {
		return nil, nil, nil, fmt.Errorf("activating the audio client: %w", err)
	}

	var format *wca.WAVEFORMATEX
	if err := client.GetMixFormat(&format); err != nil {
		client.Release()
		return nil, nil, nil, fmt.Errorf("reading the device mix format: %w", err)
	}

	// Capturing a render endpoint is what the loopback flag is for: the
	// "capture" client then sees what the device is playing, not a microphone.
	if err := client.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, wca.AUDCLNT_STREAMFLAGS_LOOPBACK, bufferDuration, 0, format, nil); err != nil {
		client.Release()
		return nil, nil, nil, fmt.Errorf("opening a loopback stream: %w", err)
	}

	var capture *wca.IAudioCaptureClient
	if err := client.GetService(wca.IID_IAudioCaptureClient, &capture); err != nil {
		client.Release()
		return nil, nil, nil, fmt.Errorf("opening the capture client: %w", err)
	}

	return client, capture, format, nil
}

// drainCapture empties the capture client and appends mono samples to out.
func drainCapture(capture *wca.IAudioCaptureClient, mix mixer, out []float64) []float64 {
	var pending uint32
	if err := capture.GetNextPacketSize(&pending); err != nil {
		return out
	}

	for pending != 0 {
		var data *byte
		var frames, flags uint32
		if err := capture.GetBuffer(&data, &frames, &flags, nil, nil); err != nil {
			return out
		}

		// A silent packet has valid length but no data worth reading.
		if flags&audclntBufferSilent == 0 && data != nil && frames > 0 {
			out = mix.append(out, data, int(frames))
		}
		_ = capture.ReleaseBuffer(frames)

		if err := capture.GetNextPacketSize(&pending); err != nil {
			return out
		}
	}
	return out
}

// sampleKind is how the samples in the mix buffer are encoded.
type sampleKind int

const (
	kindFloat32 sampleKind = iota
	kindInt16
	kindInt32
)

// mixer decodes the device's mix format once, so the hot path is a plain loop.
type mixer struct {
	channels int
	kind     sampleKind
}

func newMixer(format *wca.WAVEFORMATEX) mixer {
	mix := mixer{channels: int(format.NChannels), kind: kindFloat32}

	switch {
	case format.WFormatTag == 3: // WAVE_FORMAT_IEEE_FLOAT
		mix.kind = kindFloat32
	case format.WFormatTag == 0xFFFE: // WAVE_FORMAT_EXTENSIBLE
		// Shared-mode mix formats use a float subformat, so this is a safe
		// assumption; the fallbacks below cover the odd device.
		mix.kind = kindFloat32
	case format.WBitsPerSample == 16:
		mix.kind = kindInt16
	case format.WBitsPerSample == 32:
		mix.kind = kindInt32
	}

	if mix.channels <= 0 {
		mix.channels = 1
	}
	return mix
}

// append downmixes interleaved frames to mono and returns the result.
func (m mixer) append(out []float64, data *byte, frames int) []float64 {
	total := frames * m.channels

	switch m.kind {
	case kindInt16:
		raw := unsafe.Slice((*int16)(unsafe.Pointer(data)), total)
		return downmix(out, frames, m.channels, func(i int) float64 {
			return float64(raw[i]) / 32768
		})
	case kindInt32:
		raw := unsafe.Slice((*int32)(unsafe.Pointer(data)), total)
		return downmix(out, frames, m.channels, func(i int) float64 {
			return float64(raw[i]) / 2147483648
		})
	default:
		raw := unsafe.Slice((*float32)(unsafe.Pointer(data)), total)
		return downmix(out, frames, m.channels, func(i int) float64 {
			return float64(raw[i])
		})
	}
}

func downmix(out []float64, frames, channels int, sample func(int) float64) []float64 {
	scale := 1 / float64(channels)
	for frame := 0; frame < frames; frame++ {
		base := frame * channels
		sum := 0.0
		for channel := 0; channel < channels; channel++ {
			sum += sample(base + channel)
		}
		out = append(out, sum*scale)
	}
	return out
}
