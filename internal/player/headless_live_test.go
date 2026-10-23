package player

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/visualizer"
)

// TestHeadlessBrowserPlaysThroughTheOutputDevice is a live check rather than a
// unit test: it needs a signed-in session, an audio device, and a machine that
// is allowed to make a little noise.
//
// It exists because the question a headless browser raises cannot be answered
// with a fake. Chrome in --headless=new has to complete Apple's DRM handshake
// and then hand the decoded audio to the sound card, and a browser that fails
// the second part still looks perfectly healthy to the interface: MusicKit
// keeps reporting a position that advances, so the progress bar moves and only
// a human would notice the silence. Loopback is what can tell the difference,
// because it reads the output device rather than the player.
//
//	AM_LIVE_BROWSER=1 go test ./internal/player -run Headless -v
//
// AM_LIVE_QUERY picks the search term, so this can be pointed at a track whose
// length is known.
func TestHeadlessBrowserPlaysThroughTheOutputDevice(t *testing.T) {
	if os.Getenv("AM_LIVE_BROWSER") == "" {
		t.Skip("set AM_LIVE_BROWSER=1 to run the live browser check; it plays audio out loud")
	}

	query := os.Getenv("AM_LIVE_QUERY")
	if query == "" {
		query = "bohemian rhapsody"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	session, err := auth.Connect(ctx, true)
	if err != nil {
		t.Skipf("this machine has no signed-in session: %v", err)
	}
	songs, err := session.Client.SearchSongs(ctx, query, 1)
	if err != nil {
		t.Fatalf("searching the catalogue: %v", err)
	}
	if len(songs) == 0 {
		t.Fatalf("no catalogue results for %q", query)
	}
	song := songs[0]
	t.Logf("track: %s (%s)", song.Label(), song.Duration())

	monitor := visualizer.Start()
	defer func() { _ = monitor.Close() }()
	if !monitor.Live() {
		t.Fatal("this check needs the real system audio monitor, and this machine fell back to a stand-in")
	}

	tracks := []Track{{
		ID:         song.ID,
		Title:      song.Name,
		Artist:     song.Artist,
		Album:      song.Album,
		DurationMs: song.DurationMs,
	}}

	launched := time.Now()
	mode := "headless"
	if os.Getenv("AM_LIVE_OFFSCREEN") != "" {
		mode = "off-screen"
	}
	engine, err := StartBrowserEngine(ctx, tracks, BrowserOptions{Headless: mode == "headless"})
	if err != nil {
		t.Fatalf("starting the %s browser: %v", mode, err)
	}
	defer func() { _ = engine.Close() }()
	t.Logf("%s browser ready %s after launch", mode, time.Since(launched).Round(100*time.Millisecond))

	if err := engine.Play(ctx, 0); err != nil {
		t.Fatalf("starting playback: %v", err)
	}
	t.Logf("playback started %s after launch", time.Since(launched).Round(100*time.Millisecond))

	// Quiet enough to be neighbourly, loud enough that the device still shows a
	// signal: MusicKit starts at full volume, and loopback reads the device.
	if err := engine.AdjustVolume(ctx, -80); err != nil {
		t.Logf("could not lower the volume, the check will be louder than intended: %v", err)
	}

	var (
		position float64
		peak     float64
		stopped  bool
		preview  bool
	)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Status(ctx)
		if err != nil {
			t.Fatalf("reading playback state: %v", err)
		}
		if status.Position > position {
			position = status.Position
		}
		stopped = stopped || status.Paused
		preview = preview || status.PreviewFallback

		for _, level := range monitor.Bands() {
			if level > peak {
				peak = level
			}
		}
		if position > 8 && peak > 0.05 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	t.Logf("playhead reached %.1fs, loudest band on the output device %.3f", position, peak)

	switch {
	case position < 1:
		t.Errorf("the playhead never moved: headless playback did not start (paused=%v)", stopped)
	case preview:
		t.Errorf("Apple served a preview instead of the full track, which is exactly what this must not cause")
	case peak < 0.05:
		t.Errorf("the playhead advanced but nothing reached the output device: headless playback is silent")
	default:
		t.Logf("headless playback works: a full-length track, audible at the device")
	}
}
