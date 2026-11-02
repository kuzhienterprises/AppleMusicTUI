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
//
// extend, when it is not nil, is asked for more tracks before the queue is
// allowed to end: the radio queue works the same way here as on screen, with a
// printed line per track instead of a full interface.
func PlayPlain(ctx context.Context, engine Engine, tracks []Track, startIndex int, stopAfter time.Duration, extend func(context.Context, []Track) ([]Track, error)) error {
	if len(tracks) == 0 {
		return errors.New("nothing to play")
	}
	if startIndex < 0 || startIndex >= len(tracks) {
		startIndex = 0
	}

	printTrack := func(track Track) {
		suffix := ""
		if track.IsPreview {
			suffix = " (preview)"
		}
		ui.Info("\u25b6 %s%s", track.Label(), suffix)
	}
	for _, track := range tracks[startIndex:] {
		printTrack(track)
	}

	var deadline time.Time
	if stopAfter > 0 {
		deadline = time.Now().Add(stopAfter)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	warned := false
	failures := 0
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
				ui.Debug("position %.1fs / %.1fs \u00b7 paused=%v \u00b7 index=%d", status.Position, status.Duration, status.Paused, status.Index)
			}
			if status.PreviewFallback && !warned {
				warned = true
				ui.Warn("Apple served a preview clip: run `am login` to restore full-length playback")
			}
			if !status.Finished {
				continue
			}

			if extend == nil || failures >= 3 {
				return nil
			}
			more, err := extend(ctx, tracks)
			if err != nil || len(more) == 0 {
				failures++
				if err != nil {
					ui.Warn("could not add more tracks: %v", err)
				}
				continue
			}
			if err := engine.Enqueue(ctx, more); err != nil {
				failures++
				ui.Warn("could not queue more tracks: %v", err)
				continue
			}

			// The queue had stopped, so it has to be told to start again:
			// nothing resumes on its own at the end.
			firstNew := len(tracks)
			tracks = append(tracks, more...)
			for _, track := range more {
				printTrack(track)
			}
			if err := engine.Play(ctx, firstNew); err != nil {
				return err
			}
		}
	}
}
