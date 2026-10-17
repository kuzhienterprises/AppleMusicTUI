package player

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMpvControlChannel exercises the part of mpv integration the interactive
// player depends on: launching mpv, correlating request/response commands, and
// receiving observe_property notifications.
//
// It plays a few seconds of generated silence, so it makes no sound and needs
// no network.
func TestMpvControlChannel(t *testing.T) {
	if _, err := FindMpv(); err != nil {
		t.Skipf("mpv is not installed: %v", err)
	}

	wavPath := filepath.Join(t.TempDir(), "silence.wav")
	if err := writeSilenceWav(wavPath, 5); err != nil {
		t.Fatalf("writing the test audio file: %v", err)
	}

	instance, err := StartMpv([]Track{{
		ID:         "test",
		Title:      "Silence",
		URL:        wavPath,
		DurationMs: 5000,
	}}, Options{Idle: true})
	if err != nil {
		t.Fatalf("StartMpv: %v", err)
	}
	defer func() { _ = instance.Close() }()

	// Request/response: mpv should report the duration of the file.
	var duration float64
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := instance.GetProperty("duration", &duration); err == nil && duration > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if duration <= 0 {
		t.Fatalf("mpv never reported a duration (got %v)", duration)
	}
	if duration < 4 || duration > 6 {
		t.Errorf("unexpected duration: %v seconds", duration)
	}

	// Observation: rewind so time-pos keeps moving, then wait for events.
	if err := instance.Command("seek", 0, "absolute"); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if err := instance.Observe(1, "time-pos"); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	timeout := time.After(10 * time.Second)
	for {
		select {
		case event, open := <-instance.Events():
			if !open {
				t.Fatal("the event channel closed before any property change arrived")
			}
			if event.Event == "property-change" && event.Name == "time-pos" {
				return // the control channel works end to end
			}
		case <-timeout:
			t.Fatal("no time-pos property changes arrived")
		}
	}
}

// Adding to the queue has to leave what is playing alone, and the playlist has
// to actually grow. This is the preview engine's half of the in-player search.
func TestMpvEnqueueAppendsWithoutInterrupting(t *testing.T) {
	if _, err := FindMpv(); err != nil {
		t.Skipf("mpv is not installed: %v", err)
	}

	wavPath := filepath.Join(t.TempDir(), "silence.wav")
	if err := writeSilenceWav(wavPath, 5); err != nil {
		t.Fatalf("writing the test audio file: %v", err)
	}

	engine, err := StartMpvEngine([]Track{{
		ID: "first", Title: "First", URL: wavPath, DurationMs: 5000,
	}}, 0, true)
	if err != nil {
		t.Fatalf("StartMpvEngine: %v", err)
	}
	defer func() { _ = engine.Close() }()

	if err := engine.Play(context.Background(), 0); err != nil {
		t.Fatalf("Play: %v", err)
	}

	// Wait for playback to begin, so the addition has something to not
	// interrupt.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var position float64
		if err := engine.mpv.GetProperty("time-pos", &position); err == nil && position > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// A track with no preview audio cannot be queued, and saying so beats
	// letting the queue on screen drift away from mpv's.
	if err := engine.Enqueue(context.Background(), []Track{{ID: "none", Title: "No audio"}}); err == nil {
		t.Error("expected a track with no preview URL to be refused")
	}
	if engine.trackCount() != 1 {
		t.Fatalf("a refused track changed the queue: %d tracks", engine.trackCount())
	}

	if err := engine.Enqueue(context.Background(), []Track{
		{ID: "second", Title: "Second", URL: wavPath, DurationMs: 5000},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	var count int
	if err := engine.mpv.GetProperty("playlist-count", &count); err != nil {
		t.Fatalf("playlist-count: %v", err)
	}
	if count != 2 || engine.trackCount() != 2 {
		t.Errorf("mpv holds %d tracks and the engine %d, want 2 and 2", count, engine.trackCount())
	}

	// The track that was playing is still the one playing.
	var index int
	if err := engine.mpv.GetProperty("playlist-pos", &index); err != nil {
		t.Fatalf("playlist-pos: %v", err)
	}
	if index != 0 {
		t.Errorf("playlist-pos = %d after adding, want 0", index)
	}
}

// writeSilenceWav writes a mono 16-bit PCM WAV of the requested length.
func writeSilenceWav(path string, seconds int) error {
	const (
		sampleRate    = 44100
		bitsPerSample = 16
		channels      = 1
	)

	dataSize := sampleRate * seconds * channels * bitsPerSample / 8
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.WriteString("RIFF"); err != nil {
		return err
	}
	write := func(value any) error { return binary.Write(file, binary.LittleEndian, value) }

	_ = write(uint32(36 + dataSize))
	_, _ = file.WriteString("WAVE")
	_, _ = file.WriteString("fmt ")
	_ = write(uint32(16))                                        // PCM header size
	_ = write(uint16(1))                                         // format: PCM
	_ = write(uint16(channels))                                  // channels
	_ = write(uint32(sampleRate))                                // sample rate
	_ = write(uint32(sampleRate * channels * bitsPerSample / 8)) // byte rate
	_ = write(uint16(channels * bitsPerSample / 8))              // block align
	_ = write(uint16(bitsPerSample))                             // bits per sample
	_, _ = file.WriteString("data")
	_ = write(uint32(dataSize))
	_, err = file.Write(make([]byte, dataSize))
	return err
}
