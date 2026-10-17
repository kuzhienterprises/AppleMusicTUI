// Package player runs audio through mpv and controls it over mpv's IPC
// protocol.
package player

import "fmt"

// Track is a single playable item handed to the engine.
type Track struct {
	ID         string
	Title      string
	Artist     string
	Album      string
	Artwork    string
	URL        string
	DurationMs int
	IsPreview  bool
}

// Label renders "Title — Artist".
func (t Track) Label() string {
	if t.Artist == "" {
		return t.Title
	}
	return t.Title + " — " + t.Artist
}

// Subtitle renders "Album · m:ss".
func (t Track) Subtitle() string {
	seconds := t.DurationMs / 1000
	duration := fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
	if t.IsPreview {
		duration += " (preview)"
	}
	if t.Album == "" {
		return duration
	}
	return t.Album + " · " + duration
}

// URLs extracts the playable URLs of a queue, in order.

func urlsOf(tracks []Track) []string {
	urls := make([]string, 0, len(tracks))
	for _, track := range tracks {
		urls = append(urls, track.URL)
	}
	return urls
}
