package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
)

func newPlayCommand() *cobra.Command {
	var (
		all       bool
		stopAfter time.Duration
	)

	cmd := &cobra.Command{
		Use:   "play <query|id>",
		Short: "Play a track by name or Apple Music id",
		Long: `Resolves the argument against the catalogue and plays it.

A numeric argument is treated as an Apple Music song id; anything else is
searched by name and the best match wins. Pass --all to queue every match.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			query := strings.Join(args, " ")

			session, err := auth.Connect(ctx, false)
			if err != nil {
				return err
			}

			songs, err := resolveSongs(ctx, session, query, all)
			if err != nil {
				return err
			}

			return playSongs(ctx, session, songs, 0, stopAfter)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "queue every search match instead of the best one")
	cmd.Flags().DurationVar(&stopAfter, "stop-after", 0, "stop playback after this duration, e.g. 30s or 5m")

	return cmd
}

// resolveSongs turns a query or id into a list of playable songs.
func resolveSongs(ctx context.Context, session *auth.Session, query string, all bool) ([]apple.Song, error) {
	if isNumeric(query) {
		song, err := session.Client.Song(ctx, query)
		if err != nil {
			return nil, err
		}
		return []apple.Song{song}, nil
	}

	songs, err := session.Client.SearchSongs(ctx, query, 25)
	if err != nil {
		return nil, err
	}
	if len(songs) == 0 {
		return nil, fmt.Errorf("no track matched %q", query)
	}
	if all {
		return songs, nil
	}
	return songs[:1], nil
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
