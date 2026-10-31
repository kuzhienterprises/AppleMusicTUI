package apple

import (
	"context"
	"os"
	"testing"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
)

// TestLiveLyrics reads a real song's words through the real session.
//
// It is worth running by hand, because the endpoint is not the documented one:
// lyrics are served by the hosts the web player uses, and a token that loses
// the permission there fails only against Apple, never offline. The song is
// found by search so the test works in any storefront.
//
//	AM_LIVE_LYRICS=1 go test ./internal/apple -run TestLiveLyrics -v
func TestLiveLyrics(t *testing.T) {
	if os.Getenv("AM_LIVE_LYRICS") == "" {
		t.Skip("set AM_LIVE_LYRICS=1 to read real lyrics")
	}

	cfg := config.Load()
	if cfg.DevToken == "" {
		t.Skip("no signed-in session to read lyrics with")
	}

	ctx := context.Background()
	client := NewClient(cfg.DevToken, cfg.MusicUserToken, cfg.Storefront)

	query := os.Getenv("AM_LIVE_SONG")
	if query == "" {
		query = "bohemian rhapsody"
	}
	songs, err := client.SearchSongs(ctx, query, 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(songs) == 0 {
		t.Fatalf("no song matched %q", query)
	}

	doc, err := client.Lyrics(ctx, songs[0].CatalogID)
	if err != nil {
		t.Fatalf("Lyrics(%q): %v", songs[0].Name, err)
	}
	if len(doc.Lines) == 0 {
		t.Fatalf("%q came back with no lines", songs[0].Name)
	}
	if !doc.Synced {
		t.Errorf("%q has %d lines but no timings", songs[0].Name, len(doc.Lines))
	}
	t.Logf("%q: %d lines, first at %v", songs[0].Name, len(doc.Lines), doc.Lines[0].Start)
}
