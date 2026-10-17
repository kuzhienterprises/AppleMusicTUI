package player

import "context"

// Status is a snapshot of what a playback engine is doing.
type Status struct {
	// Index is the position in the queue.
	Index int
	// Position and Duration are in seconds. Duration is zero until it is
	// known.
	Position float64
	Duration float64
	// Paused is true while playback is suspended.
	Paused bool
	// Volume is a percentage, 0-100.
	Volume float64
	// Finished is true once the queue has run out.
	Finished bool
	// PreviewFallback is true when Apple served a preview clip in place of
	// the full track, which happens when the stored session is no longer
	// valid.
	PreviewFallback bool
}

// Engine is a playback backend.
//
// Two implementations exist. The mpv engine plays the 30 second preview clips
// Apple hands out to signed-out clients. The browser engine plays full-length
// audio by driving Apple's own web player, because that audio is DRM encrypted
// and only a browser hosts the decryption module.
type Engine interface {
	// Play starts playback at the given queue position.
	Play(ctx context.Context, index int) error
	// TogglePause pauses a playing queue, or resumes a paused one.
	TogglePause(ctx context.Context) error
	// Seek moves the playhead by a relative number of seconds.
	Seek(ctx context.Context, deltaSeconds float64) error
	// Next and Previous move through the queue.
	Next(ctx context.Context) error
	Previous(ctx context.Context) error
	// Enqueue adds tracks to the end of the queue, leaving what is playing
	// and everything already queued where it is.
	Enqueue(ctx context.Context, tracks []Track) error
	// AdjustVolume changes the volume by a relative percentage.
	AdjustVolume(ctx context.Context, deltaPercent float64) error
	// Status reports the current state, used by both interfaces to draw
	// themselves.
	Status(ctx context.Context) (Status, error)
	// Close shuts the backend down and releases its resources.
	Close() error
}
