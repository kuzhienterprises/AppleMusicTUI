package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/tui"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

func newPlaylistsCommand() *cobra.Command {
	var (
		asJSON    bool
		listOnly  bool
		stopAfter time.Duration
	)

	cmd := &cobra.Command{
		Use:     "playlists",
		Aliases: []string{"ls"},
		Short:   "List your library playlists and play one",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			session, err := auth.Connect(ctx, true)
			if err != nil {
				return err
			}

			playlists, err := session.Client.LibraryPlaylists(ctx)
			if err != nil {
				return err
			}
			if len(playlists) == 0 {
				ui.Warn("Your library has no playlists yet")
				return nil
			}

			if asJSON {
				return printJSON(playlists)
			}

			// The picker draws the list; printing it as well would leave it on
			// screen underneath the player.
			if listOnly || !interactive() {
				printPlaylists(playlists)
				return nil
			}

			choice, err := tui.Choose(playlistItems(playlists), "Your playlists")
			if err != nil || choice == nil {
				return err
			}

			ui.Step("Loading %s", playlists[choice.Index].Name)
			tracks, err := session.Client.LibraryPlaylistTracks(ctx, playlists[choice.Index].ID)
			if err != nil {
				return err
			}
			if len(tracks) == 0 {
				ui.Warn("That playlist is empty")
				return nil
			}
			return playSongs(ctx, session, tracks, 0, stopAfter)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print playlists as JSON")
	cmd.Flags().BoolVar(&listOnly, "list", false, "only list, never open the player")
	cmd.Flags().DurationVar(&stopAfter, "stop-after", 0, "stop playback after this duration, e.g. 30s or 5m")

	return cmd
}

func newPlaylistCommand() *cobra.Command {
	var stopAfter time.Duration

	cmd := &cobra.Command{
		Use:   "playlist <name|id>",
		Short: "Play a playlist by name or id",
		Long: `Plays a playlist from your library, or from the catalogue when no library
playlist matches.

Accepts a library id (p.…), a catalogue id (pl.…), or a name.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			query := strings.Join(args, " ")

			session, err := auth.Connect(ctx, true)
			if err != nil {
				return err
			}

			tracks, name, err := resolvePlaylist(ctx, session, query)
			if err != nil {
				return err
			}
			if len(tracks) == 0 {
				ui.Warn("%s has no playable tracks", name)
				return nil
			}

			ui.Info("\u25b6 %s (%d tracks)", name, len(tracks))
			return playSongs(ctx, session, tracks, 0, stopAfter)
		},
	}

	cmd.Flags().DurationVar(&stopAfter, "stop-after", 0, "stop playback after this duration, e.g. 30s or 5m")

	return cmd
}

// resolvePlaylist finds a playlist by id or name, preferring the user's
// library and falling back to the catalogue.
func resolvePlaylist(ctx context.Context, session *auth.Session, query string) ([]apple.Song, string, error) {
	client := session.Client

	switch {
	case strings.HasPrefix(query, "p."):
		playlist, err := client.FindLibraryPlaylist(ctx, query)
		if err != nil {
			return nil, "", err
		}
		tracks, err := client.LibraryPlaylistTracks(ctx, playlist.ID)
		return tracks, playlist.Name, err

	case strings.HasPrefix(query, "pl."):
		tracks, err := client.CatalogPlaylistTracks(ctx, query)
		return tracks, query, err
	}

	if playlist, err := client.FindLibraryPlaylist(ctx, query); err == nil {
		if tracks, err := client.LibraryPlaylistTracks(ctx, playlist.ID); err == nil {
			return tracks, playlist.Name, nil
		}
	}

	playlists, err := client.SearchPlaylists(ctx, query, 5)
	if err != nil {
		return nil, "", err
	}
	if len(playlists) == 0 {
		return nil, "", fmt.Errorf("no playlist matched %q", query)
	}

	chosen := playlists[0]
	tracks, err := client.CatalogPlaylistTracks(ctx, chosen.ID)
	if err != nil {
		return nil, "", err
	}
	return tracks, chosen.Name, nil
}
