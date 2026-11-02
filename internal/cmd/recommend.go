package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/ai"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/recommend"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// defaultModel is the cheapest current model, which is enough for picking
// tracks from a list of names: the work is taste, not reasoning.
const defaultModel = "gpt-6-luna"

// recommendWindow is how much of the recent-played history is read. Apple
// serves 30 a request, so 60 is two requests: enough of a picture without
// turning the command into a crawl.
const recommendWindow = 60

func newRecommendCommand() *cobra.Command {
	var (
		count     int
		model     string
		window    int
		printOnly bool
		asJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Ask a model what to play next, and play it",
		Long: `Reads your recently played history from Apple Music, asks an OpenAI model for
tracks you have not just played, looks each one up in the catalogue, and plays
the ones that exist.

The recommendations come from your own listening: what Apple has recorded
across your devices, plus the names of your playlists. Nothing about you is
sent anywhere except a summary of that history, and only to the model you
configured.

The key is read from OPENAI_API_KEY, or from ` + "`am config set openaiKey`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			if count < 1 || count > 25 {
				return errors.New("--count must be between 1 and 25")
			}
			if window < 1 {
				window = recommendWindow
			}
			if window > 90 {
				window = 90 // three pages is as far back as the history goes usefully
			}

			key := openAIKey()
			if key == "" {
				return fmt.Errorf("no OpenAI key: %s", missingKeyHint())
			}
			if model == "" {
				model = strings.TrimSpace(config.Load().OpenAIModel)
			}
			if model == "" {
				model = defaultModel
			}

			session, err := auth.Connect(ctx, true)
			if err != nil {
				return err
			}

			ui.Step("Reading your recent plays")
			plays, err := session.Client.RecentPlays(ctx, window)
			if err != nil {
				return err
			}
			if len(plays) == 0 {
				ui.Warn("No recent plays on this account: recommending from your playlists alone.")
			}

			profile := recommend.BuildProfile(plays, playlistNames(ctx, session))

			ui.Step("Asking %s for %d tracks", model, count)
			suggestions, err := recommend.Ask(ctx, ai.NewClient(key, model), profile, count)
			if err != nil {
				return err
			}

			ui.Step("Looking them up in the catalogue")
			picks, missed, err := recommend.Resolve(ctx, func(ctx context.Context, query string) ([]apple.Song, error) {
				return session.Client.SearchSongs(ctx, query, 5)
			}, suggestions, count)
			if err != nil {
				return err
			}

			for _, suggestion := range missed {
				ui.Warn("Could not find %q by %s in the catalogue.", suggestion.Title, suggestion.Artist)
			}
			if len(picks) == 0 && !asJSON {
				return errors.New("none of the suggestions could be found in the catalogue")
			}

			switch {
			case asJSON:
				return printJSON(struct {
					Model  string           `json:"model"`
					Tracks []printableTrack `json:"tracks"`
					Missed []string         `json:"missed,omitempty"`
				}{Model: model, Tracks: printable(picks), Missed: labels(missed)})
			case printOnly, !interactive():
				printRecommendations(picks)
				return nil
			}

			songs := make([]apple.Song, 0, len(picks))
			for _, pick := range picks {
				songs = append(songs, pick.Song)
			}
			return playSongs(ctx, session, songs, 0, 0)
		},
	}

	cmd.Flags().IntVar(&count, "count", 10, "how many tracks to recommend")
	cmd.Flags().StringVar(&model, "model", "", "OpenAI model to ask (default "+defaultModel+")")
	cmd.Flags().IntVar(&window, "window", recommendWindow, "how many recent plays to read, up to 90")
	cmd.Flags().BoolVar(&printOnly, "print", false, "list the tracks instead of playing them")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the tracks as JSON")

	return cmd
}

// playlistNames reads the names of the library playlists. Playlist names are a
// cheap, surprisingly direct statement of taste ("Malayalam 90s", "Gym"), and
// a failure to read them is not worth failing the command over.
func playlistNames(ctx context.Context, session *auth.Session) []string {
	playlists, err := session.Client.LibraryPlaylists(ctx)
	if err != nil {
		ui.Debug("could not read the playlists: %v", err)
		return nil
	}
	names := make([]string, 0, len(playlists))
	for _, playlist := range playlists {
		if playlist.Name != "" {
			names = append(names, playlist.Name)
		}
	}
	return names
}

// printRecommendations lists what would play, reason and all. It is the same
// list the queue would be built from, so a preview never promises a track the
// catalogue did not confirm.
func printRecommendations(picks []recommend.Pick) {
	for i, pick := range picks {
		song := pick.Song
		fmt.Printf("%2d. %s\n", i+1, song.Label())
		fmt.Printf("    %s - %s\n", song.Album, song.Duration())
		if pick.Why != "" {
			fmt.Printf("    %s\n", pick.Why)
		}
	}
}

// printableTrack is a recommendation as machine-readable output: enough to
// script around, without the artwork URLs and the rest of the Song struct.
type printableTrack struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	ID     string `json:"id"`
	Why    string `json:"why,omitempty"`
}

func printable(picks []recommend.Pick) []printableTrack {
	tracks := make([]printableTrack, 0, len(picks))
	for _, pick := range picks {
		tracks = append(tracks, printableTrack{
			Title:  pick.Song.Name,
			Artist: pick.Song.Artist,
			Album:  pick.Song.Album,
			ID:     pick.Song.CatalogID,
			Why:    pick.Why,
		})
	}
	return tracks
}

func labels(suggestions []recommend.Suggestion) []string {
	out := make([]string, 0, len(suggestions))
	for _, suggestion := range suggestions {
		out = append(out, suggestion.Title+" - "+suggestion.Artist)
	}
	return out
}
