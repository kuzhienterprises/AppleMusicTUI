package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/tui"
)

// isTerminal reports whether the file is attached to a terminal.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// interactive reports whether we should run the full-screen interface.
func interactive() bool {
	if flagPlain {
		return false
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func printSongs(songs []apple.Song) {
	for i, song := range songs {
		marker := " "
		if !song.Playable() {
			// No preview: the row can be listed but not played.
			marker = "·"
		}
		fmt.Printf("%s %2d. %s\n", marker, i+1, song.Label())

		album := song.Album
		if album == "" {
			album = "—"
		}
		fmt.Printf("        %s · %s\n", album, song.Duration())
	}
}

func printPlaylists(playlists []apple.Playlist) {
	for i, playlist := range playlists {
		fmt.Printf("%3d. %s\n", i+1, playlist.Label())
		if playlist.Description != "" {
			fmt.Printf("     %s\n", playlist.Description)
		}
	}
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// songItems converts songs into picker rows, remembering the original index
// so a selection can start playback at the right place in the queue.
func songItems(songs []apple.Song) []tui.Item {
	items := make([]tui.Item, 0, len(songs))
	for i, song := range songs {
		desc := song.Duration()
		if song.Album != "" {
			desc = song.Album + " · " + desc
		}
		if !song.Playable() {
			desc += " · no audio"
		}
		items = append(items, tui.Item{TitleText: song.Label(), Desc: desc, Index: i})
	}
	return items
}

func playlistItems(playlists []apple.Playlist) []tui.Item {
	items := make([]tui.Item, 0, len(playlists))
	for i, playlist := range playlists {
		desc := playlist.Description
		if desc == "" {
			desc = playlist.Label()
		}
		items = append(items, tui.Item{TitleText: playlist.Name, Desc: desc, Index: i})
	}
	return items
}
