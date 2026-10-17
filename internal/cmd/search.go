package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/tui"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

func newSearchCommand() *cobra.Command {
	var (
		limit     int
		asJSON    bool
		noPlay    bool
		queueAll  bool
		stopAfter time.Duration
	)

	cmd := &cobra.Command{
		Use:     "search <query>",
		Aliases: []string{"find", "s"},
		Short:   "Search the catalogue and play a result",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			query := strings.Join(args, " ")

			session, err := auth.Connect(ctx, false)
			if err != nil {
				return err
			}

			songs, err := session.Client.SearchSongs(ctx, query, limit)
			if err != nil {
				return err
			}
			if len(songs) == 0 {
				ui.Warn("No results for %q", query)
				return nil
			}

			if asJSON {
				return printJSON(songs)
			}

			// When the picker is about to be shown, the results are drawn
			// there. Printing them here as well would leave the whole list on
			// screen underneath the player for the rest of the session.
			if noPlay || !interactive() {
				printSongs(songs)
				return nil
			}

			choice, err := tui.Choose(songItems(songs), fmt.Sprintf("Results for %q", query))
			if err != nil || choice == nil {
				return err
			}

			// A result list is candidates, not a playlist. Picking one of
			// twenty tracks called "Mercy" means playing that one, so the
			// queue starts out holding it alone; --all asks for the whole
			// list, and / adds more while it plays.
			picked := choice.Index
			if picked < 0 || picked >= len(songs) {
				picked = 0
			}
			selected, startIndex := songs[picked:picked+1], 0
			if queueAll {
				selected, startIndex = songs, picked
			}
			return playSongs(ctx, session, selected, startIndex, stopAfter)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of results to fetch")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	cmd.Flags().BoolVar(&noPlay, "no-play", false, "list results without opening the player")
	cmd.Flags().BoolVar(&queueAll, "all", false, "queue every result instead of the one picked")
	cmd.Flags().DurationVar(&stopAfter, "stop-after", 0, "stop playback after this duration, e.g. 30s or 5m")

	return cmd
}
