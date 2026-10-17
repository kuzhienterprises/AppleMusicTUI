package player

import (
	"context"
	"errors"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// PlayPlain drives an engine with no interface at all: used when stdout is not
// a terminal, or when --plain is passed. It returns when the queue finishes,
// the stop timer fires, or the context is cancelled.
func PlayPlain(ctx context.Context, engine Engine, tracks []Track, startIndex int, stopAfter time.Duration) error {
	if len(tracks) == 0 {
		return errors.New("nothing to play")
	}
	if startIndex < 0 || startIndex >= len(tracks) {
		startIndex = 0
	}

	for _, track := range tracks[startIndex:] {
		suffix := ""
		if track.IsPreview {
			suffix = " (preview)"
		}
		ui.Info("\u25b6 %s%s", track.Label(), suffix)
	}

	var deadline time.Time
	if stopAfter > 0 {
		deadline = time.Now().Add(stopAfter)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	warned := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return nil
			}
			status, err := engine.Status(ctx)
			if err != nil {
				return err
			}
			if ui.Verbose() {
				ui.Debug("position %.1fs / %.1fs · paused=%v · index=%d", status.Position, status.Duration, status.Paused, status.Index)
			}
			if status.PreviewFallback && !warned {
				warned = true
				ui.Warn("Apple served a preview clip: run `am login` to restore full-length playback")
			}
			if status.Finished {
				return nil
			}
		}
	}
}
