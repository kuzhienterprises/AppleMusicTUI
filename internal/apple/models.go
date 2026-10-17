package apple

import (
	"fmt"
	"strings"
)

// Song is a normalised track, whether it came from the catalogue or the
// user's library.
type Song struct {
	ID         string
	Name       string
	Artist     string
	Album      string
	DurationMs int
	Artwork    string // URL template containing {w} and {h}
	PreviewURL string
	URL        string
	CatalogID  string
	ISRC       string
}

// Playable reports whether the track has an audio source we can hand to the
// player.
func (s Song) Playable() bool { return s.PreviewURL != "" }

// Label renders "Title — Artist" for lists.
func (s Song) Label() string {
	if s.Artist == "" {
		return s.Name
	}
	return s.Name + " — " + s.Artist
}

// Duration renders the track length as m:ss.
func (s Song) Duration() string {
	seconds := s.DurationMs / 1000
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// Playlist is a library or catalogue playlist.
type Playlist struct {
	ID          string
	Name        string
	Description string
	TrackCount  int
	Artwork     string
	IsLibrary   bool
}

// Label renders the playlist name with its track count.
func (p Playlist) Label() string {
	if p.TrackCount > 0 {
		return fmt.Sprintf("%s (%d tracks)", p.Name, p.TrackCount)
	}
	return p.Name
}

func cleanDescription(raw string) string {
	text := strings.TrimSpace(raw)
	if idx := strings.Index(text, "\n"); idx > 0 {
		text = text[:idx]
	}
	return strings.TrimSpace(text)
}
