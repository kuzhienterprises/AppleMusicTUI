package player

import (
	"context"
	"errors"
	"sync"
)

// MpvEngine exposes an mpv process as an Engine. mpv can only play the preview
// URLs Apple serves to signed-out clients; full-length tracks are DRM
// encrypted and need the browser engine.
type MpvEngine struct {
	mpv     *Mpv
	started bool

	// mu guards tracks, which grows while playback continues: additions come
	// from the interface's goroutine while the status poll reads the same
	// slice from another.
	mu     sync.Mutex
	tracks []Track
}

// StartMpvEngine launches mpv with the queue and returns it as an Engine.
func StartMpvEngine(tracks []Track, startIndex int, idle bool) (*MpvEngine, error) {
	instance, err := StartMpv(tracks, Options{Idle: idle, StartIndex: startIndex})
	if err != nil {
		return nil, err
	}
	return &MpvEngine{mpv: instance, tracks: tracks}, nil
}

// Play jumps to a queue position and unpauses.
func (e *MpvEngine) Play(_ context.Context, index int) error {
	if index < 0 || index >= e.trackCount() {
		index = 0
	}
	if err := e.mpv.Command("set_property", "playlist-pos", index); err != nil {
		return err
	}
	return e.mpv.Command("set_property", "pause", false)
}

// TogglePause cycles mpv's pause flag.
func (e *MpvEngine) TogglePause(context.Context) error {
	return e.mpv.Command("cycle", "pause")
}

// Seek moves the playhead by a relative number of seconds.
func (e *MpvEngine) Seek(_ context.Context, delta float64) error {
	return e.mpv.Command("seek", delta)
}

// Next skips forward, wrapping past the end of the playlist.
func (e *MpvEngine) Next(context.Context) error {
	return e.mpv.Command("playlist-next", "force")
}

// Previous skips backward.
func (e *MpvEngine) Previous(context.Context) error {
	return e.mpv.Command("playlist-prev", "force")
}

// Enqueue appends previews to mpv's playlist. mpv's own "append" flag is what
// keeps this from interrupting the track that is playing.
func (e *MpvEngine) Enqueue(_ context.Context, tracks []Track) error {
	for _, track := range tracks {
		// Previews are all mpv can play, so a track without a preview URL
		// cannot be queued at all. Refusing is better than letting the queue
		// on screen drift away from mpv's.
		if track.URL == "" {
			return errors.New("that track has no preview audio to add")
		}
	}

	for _, track := range tracks {
		if err := e.mpv.Command("loadfile", track.URL, "append"); err != nil {
			return err
		}

		e.mu.Lock()
		e.tracks = append(e.tracks, track)
		e.mu.Unlock()
	}
	return nil
}

// trackCount is the length of the queue, including anything added since
// playback began.
func (e *MpvEngine) trackCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.tracks)
}

// AdjustVolume adds a relative percentage to mpv's volume.
func (e *MpvEngine) AdjustVolume(_ context.Context, delta float64) error {
	return e.mpv.Command("add", "volume", delta)
}

// Status reads mpv's properties. A missing position means no track is loaded,
// which in idle mode is how the end of the queue presents itself; a missing
// position plus an exited process means one-shot playback has finished.
func (e *MpvEngine) Status(context.Context) (Status, error) {
	volume := 100.0
	_ = e.mpv.GetProperty("volume", &volume)

	var position float64
	if err := e.mpv.GetProperty("time-pos", &position); err != nil {
		select {
		case <-e.mpv.Exited():
			return Status{Finished: true}, nil
		default:
		}
		var idle bool
		_ = e.mpv.GetProperty("idle-active", &idle)
		if e.started && idle {
			return Status{Volume: volume, Finished: true}, nil
		}
		return Status{Volume: volume}, nil
	}

	var (
		duration float64
		index    int
		paused   bool
	)
	_ = e.mpv.GetProperty("duration", &duration)
	_ = e.mpv.GetProperty("pause", &paused)
	_ = e.mpv.GetProperty("playlist-pos", &index)
	if position > 0 || duration > 0 {
		e.started = true
	}

	return Status{
		Index:    index,
		Position: position,
		Duration: duration,
		Paused:   paused,
		Volume:   volume,
	}, nil
}

// Close stops mpv.
func (e *MpvEngine) Close() error { return e.mpv.Close() }
