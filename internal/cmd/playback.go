package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/tui"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// queueSearchLimit is how many results the in-player search asks for. The panel
// only has room for a handful, and choosing from a short list beats scrolling
// one that has everything ever called "Mercy".
const queueSearchLimit = 10

// searchQueue is the in-player search: catalogue results converted exactly the
// way the initial queue is, so what gets added behaves like what is already
// playing. Preview playback and full playback disagree about which tracks are
// usable, and that judgement has to match.
func searchQueue(session *auth.Session, full bool) tui.Searcher {
	if session == nil {
		return nil
	}

	return func(ctx context.Context, query string) ([]player.Track, error) {
		songs, err := session.Client.SearchSongs(ctx, query, queueSearchLimit)
		if err != nil {
			return nil, err
		}
		tracks, _, _ := buildTracks(songs, !full)
		return tracks, nil
	}
}

// playSongs converts API songs into a play queue, picks a playback engine, and
// hands both to the interface (or the plain printer). startIndex refers to a
// position in songs, not in the resulting queue. The session is what lets the
// player search the catalogue again to add to the queue.
func playSongs(ctx context.Context, session *auth.Session, songs []apple.Song, startIndex int, stopAfter time.Duration) error {
	full, err := useFullPlayback()
	if err != nil {
		return err
	}

	tracks, indexMap, skipped := buildTracks(songs, !full)
	if len(tracks) == 0 {
		if full {
			return errors.New("none of those tracks can be played")
		}
		return errors.New("none of those tracks have audio available")
	}
	if skipped > 0 {
		ui.Warn("%d track(s) had no audio and were skipped", skipped)
	}
	if mapped, ok := indexMap[startIndex]; ok {
		startIndex = mapped
	} else {
		startIndex = 0
	}

	// The interface comes up first and shows the wait. Bringing the engine up
	// beforehand would mean staring at nothing for the seconds a browser takes
	// to boot, which is the difference between "it is starting" and "it is
	// broken".
	if interactive() {
		return tui.Play(tui.Options{
			Start: func(ctx context.Context) (player.Engine, error) {
				return startEngine(ctx, tracks, startIndex, full, true)
			},
			Search:    searchQueue(session, full),
			Tracks:    tracks,
			StopAfter: stopAfter,
		})
	}

	if full {
		ui.Step("Starting the hidden player; the first run takes a few seconds")
	}
	engine, err := startEngine(ctx, tracks, startIndex, full, false)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()

	if !flagPlain {
		ui.Info("(stdout is not a terminal: playing without the interface)")
	}
	return player.PlayPlain(ctx, engine, tracks, startIndex, stopAfter)
}

// useFullPlayback decides between full-length audio and the 30 second preview
// clips. Full-length playback goes through Apple's own player in a hidden
// browser, so it needs a signed-in session.
func useFullPlayback() (bool, error) {
	signedIn := config.Load().MusicUserToken != ""

	switch flagEngine {
	case "preview":
		return false, nil
	case "full":
		if !signedIn {
			return false, errors.New("full-length playback needs a signed-in session: run `am login` first")
		}
		return true, nil
	case "", "auto":
		return signedIn, nil
	default:
		return false, fmt.Errorf("unknown engine %q: use auto, full or preview", flagEngine)
	}
}

// startEngine brings up the chosen backend and starts playback. It does not
// announce itself: whoever calls it owns the screen, and the interface has its
// own way of saying it is waiting.
func startEngine(ctx context.Context, tracks []player.Track, startIndex int, full, idle bool) (player.Engine, error) {
	if !full {
		// mpv manages its own start position from the playlist.
		return player.StartMpvEngine(tracks, startIndex, idle)
	}

	engine, err := player.StartBrowserEngine(ctx, tracks, player.BrowserOptions{})
	if err != nil {
		return nil, err
	}
	if err := engine.Play(ctx, startIndex); err != nil {
		_ = engine.Close()
		return nil, err
	}
	return engine, nil
}

// buildTracks turns songs into queue entries, dropping anything that cannot be
// played and recording how the indices shifted. Preview playback needs a
// preview URL; full-length playback only needs a catalogue song id.
func buildTracks(songs []apple.Song, requirePreview bool) ([]player.Track, map[int]int, int) {
	tracks := make([]player.Track, 0, len(songs))
	indexMap := make(map[int]int, len(songs))
	skipped := 0

	for i, song := range songs {
		// MusicKit resolves either id space natively: library entries carry
		// an "i." id, catalogue entries a plain numeric one. The library id
		// is the better choice for library tracks, because it also resolves
		// songs that the current storefront's catalogue does not carry.
		id := song.ID
		if id == "" {
			id = song.CatalogID
		}

		if requirePreview {
			if !song.Playable() {
				skipped++
				continue
			}
		} else if id == "" {
			skipped++
			continue
		}

		indexMap[i] = len(tracks)
		tracks = append(tracks, player.Track{
			ID:         id,
			Title:      song.Name,
			Artist:     song.Artist,
			Album:      song.Album,
			Artwork:    song.Artwork,
			URL:        song.PreviewURL,
			DurationMs: song.DurationMs,
			IsPreview:  requirePreview,
		})
	}
	return tracks, indexMap, skipped
}
