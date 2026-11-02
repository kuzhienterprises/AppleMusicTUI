// Package recommend turns a listening history into a short list of tracks to
// play next.
//
// The model does the suggesting and the catalogue decides what exists: every
// answer is searched for before it is played, so a plausible-looking title the
// model invented costs a lookup rather than a queue entry that never starts.
package recommend

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
)

// Suggestion is one track the model proposed, before the catalogue has agreed
// that it exists. Why is the model's one-line reason, shown next to the track
// so a recommendation can be judged before it is played.
type Suggestion struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Why    string `json:"why"`
}

// Count is one entry in a tally, used for the artists and genres the prompt
// carries.
type Count struct {
	Name  string
	Times int
}

// Profile is what the model is told about the listener. It is a summary rather
// than the history itself: names, albums and tallies say more per token than a
// hundred rows of timestamps.
type Profile struct {
	Recent    []apple.Song
	Artists   []Count
	Genres    []Count
	Playlists []string
	// Queued is what the player already has lined up. It matters when the
	// queue is being extended rather than filled: the model must not hand
	// back a track that is about to play anyway.
	Queued []string
}

// Searcher looks a suggestion up in the catalogue. It is passed in so the
// recommender does not care how the catalogue is reached.
type Searcher func(ctx context.Context, query string) ([]apple.Song, error)

// BuildProfile tallies the history. The recent list keeps its order, because
// newest-first is the strongest signal in it: what somebody played an hour ago
// says more than what they played last month.
//
// The list is deduplicated for the prompt while the tallies are not: a track
// played three times in the window is three data points about the artist, but
// three identical lines would only spend tokens saying it once.
func BuildProfile(plays []apple.Song, playlists []string) Profile {
	profile := Profile{
		Playlists: playlists,
	}

	recent := make([]apple.Song, 0, len(plays))
	seen := map[string]bool{}
	artists := map[string]int{}
	genres := map[string]int{}
	for _, song := range plays {
		if name := firstArtist(song.Artist); name != "" {
			artists[name]++
		}
		for _, genre := range song.Genres {
			// "Music" is Apple's catch-all rather than a genre, and it
			// matches everything.
			if genre != "" && !strings.EqualFold(genre, "Music") {
				genres[genre]++
			}
		}

		key := normalise(song.Name) + "\x00" + normalise(song.Artist)
		if seen[key] {
			continue
		}
		seen[key] = true
		recent = append(recent, song)
	}

	profile.Recent = recent
	profile.Artists = tally(artists)
	profile.Genres = tally(genres)
	return profile
}

// tally sorts a count map into the order the prompt wants it: most played
// first, ties broken by name so the same history always builds the same
// prompt.
func tally(counts map[string]int) []Count {
	tallied := make([]Count, 0, len(counts))
	for name, times := range counts {
		tallied = append(tallied, Count{Name: name, Times: times})
	}
	sort.Slice(tallied, func(i, j int) bool {
		if tallied[i].Times != tallied[j].Times {
			return tallied[i].Times > tallied[j].Times
		}
		return tallied[i].Name < tallied[j].Name
	})
	return tallied
}

// LanguageModel is the part of the OpenAI client the recommender uses. It is
// an interface so the tests can answer without a network, and so a different
// provider could be dropped in without touching the prompt.
type LanguageModel interface {
	JSON(ctx context.Context, name string, schema map[string]any, system, user string) ([]byte, error)
}

// Ask has the model suggest tracks for the profile.
func Ask(ctx context.Context, model LanguageModel, profile Profile, count int) ([]Suggestion, error) {
	if count <= 0 {
		count = 10
	}

	system, user := prompt(profile, count)
	raw, err := model.JSON(ctx, "suggestions", schema(), system, user)
	if err != nil {
		return nil, err
	}

	var reply struct {
		Suggestions []Suggestion `json:"suggestions"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("decoding the suggestions: %w", err)
	}

	// A suggestion without a title or an artist cannot be looked up, and one
	// that repeats another would only cost a second search.
	suggestions := make([]Suggestion, 0, len(reply.Suggestions))
	seen := map[string]bool{}
	for _, suggestion := range reply.Suggestions {
		suggestion.Title = strings.TrimSpace(suggestion.Title)
		suggestion.Artist = strings.TrimSpace(suggestion.Artist)
		suggestion.Why = strings.TrimSpace(suggestion.Why)
		if suggestion.Title == "" || suggestion.Artist == "" {
			continue
		}
		key := strings.ToLower(suggestion.Title + "\x00" + suggestion.Artist)
		if seen[key] {
			continue
		}
		seen[key] = true
		suggestions = append(suggestions, suggestion)
		if len(suggestions) == count {
			break
		}
	}
	if len(suggestions) == 0 {
		return nil, fmt.Errorf("the model suggested nothing that can be looked up")
	}
	return suggestions, nil
}

// Pick is a suggestion the catalogue agreed exists, with the reason it was
// made. Keeping the two together is what lets the list say why a track is
// there without matching titles back up afterwards.
type Pick struct {
	Song apple.Song
	Why  string
}

// Resolve looks every suggestion up in the catalogue. Suggestions that cannot
// be found are returned as missed rather than dropped, because "I could not
// find this" is worth saying: it is the difference between a bug and taste.
func Resolve(ctx context.Context, search Searcher, suggestions []Suggestion, limit int) ([]Pick, []Suggestion, error) {
	var (
		picks  []Pick
		missed []Suggestion
		seen   = map[string]bool{}
	)

	for _, suggestion := range suggestions {
		if limit > 0 && len(picks) >= limit {
			break
		}

		results, err := search(ctx, suggestion.Title+" "+firstArtist(suggestion.Artist))
		if err != nil {
			return nil, nil, err
		}

		best, bestScore := apple.Song{}, 0
		for _, candidate := range results {
			if score := match(suggestion, candidate); score > bestScore {
				best, bestScore = candidate, score
			}
		}
		if bestScore == 0 {
			missed = append(missed, suggestion)
			continue
		}

		id := best.CatalogID
		if id == "" {
			id = best.ID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		picks = append(picks, Pick{Song: best, Why: suggestion.Why})
	}
	return picks, missed, nil
}

// match rates a catalogue result against a suggestion. The artist is required:
// a title on its own cannot tell a cover from the recording that was meant, and
// playing the wrong artist is worse than saying the track was not found.
func match(suggestion Suggestion, song apple.Song) int {
	if !artistMatches(suggestion.Artist, song.Artist) {
		return 0
	}
	return titleScore(suggestion.Title, song.Name)
}

func titleScore(want, got string) int {
	want, got = normalise(want), normalise(got)
	switch {
	case want == "" || got == "":
		return 0
	case want == got:
		return 2
	case strings.Contains(got, want), strings.Contains(want, got):
		return 1
	default:
		return 0
	}
}

func artistMatches(want, got string) bool {
	want, got = normalise(firstArtist(want)), normalise(firstArtist(got))
	return want != "" && got != "" && (strings.Contains(want, got) || strings.Contains(got, want))
}

// firstArtist keeps the first name of a credit. Apple joins artists with
// commas and ampersands, and the model may or may not include the guests, so
// only the name that identifies the recording is compared.
func firstArtist(credit string) string {
	cut := strings.IndexAny(credit, ",&/")
	if featured := strings.Index(strings.ToLower(credit), " feat"); featured >= 0 && (cut < 0 || featured < cut) {
		cut = featured
	}
	if cut >= 0 {
		credit = credit[:cut]
	}
	return strings.TrimSpace(credit)
}

// normalise lowercases and drops punctuation, so "Don't Stop Me Now" and
// "Dont Stop Me Now" are the same title to the matcher. Apostrophes are
// dropped rather than turned into spaces, because a contraction would
// otherwise split into two words the catalogue's spelling does not have.
func normalise(text string) string {
	var builder strings.Builder
	space := false
	for _, char := range strings.ToLower(text) {
		switch {
		case char == '\'' || char == '\u2019':
			// Dropped: "don't" and "dont" are the same title.
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			if space && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			space = false
			builder.WriteRune(char)
		default:
			space = true
		}
	}
	return builder.String()
}

// schema is the shape the answer must take. strict is on in the client, so
// every property is required and nothing else is allowed.
func schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"suggestions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":  map[string]any{"type": "string"},
						"artist": map[string]any{"type": "string"},
						"why":    map[string]any{"type": "string"},
					},
					"required":             []string{"title", "artist", "why"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"suggestions"},
		"additionalProperties": false,
	}
}

// prompt writes the two messages the model sees.
func prompt(profile Profile, count int) (system, user string) {
	system = strings.Join([]string{
		"You are choosing the music one person plays next.",
		"",
		"You are given the tracks they have played most recently, newest first, and",
		"the names of their playlists. Suggest recordings they have not just played:",
		"mostly close to what they already listen to, with a few that widen it.",
		"",
		"Only real, released recordings that exist on Apple Music. Give the primary",
		"artist's name. Never repeat a track from the recent list. One short sentence",
		"of reason per track, under fifteen words, no marketing language.",
	}, "\n")

	var lines []string
	lines = append(lines, "Recently played, newest first:")
	for _, song := range profile.Recent {
		line := "- " + song.Name + " | " + song.Artist
		if song.Album != "" {
			line += " | " + song.Album
		}
		lines = append(lines, line)
	}

	if len(profile.Artists) > 0 {
		lines = append(lines, "", "Most played artists:")
		for _, artist := range profile.Artists[:minInt(len(profile.Artists), 10)] {
			lines = append(lines, fmt.Sprintf("- %s (%d)", artist.Name, artist.Times))
		}
	}
	if len(profile.Genres) > 0 {
		lines = append(lines, "", "Genres:")
		for _, genre := range profile.Genres[:minInt(len(profile.Genres), 10)] {
			lines = append(lines, fmt.Sprintf("- %s (%d)", genre.Name, genre.Times))
		}
	}
	if len(profile.Playlists) > 0 {
		lines = append(lines, "", "Playlists:")
		for _, playlist := range profile.Playlists[:minInt(len(profile.Playlists), 20)] {
			lines = append(lines, "- "+playlist)
		}
	}
	if len(profile.Queued) > 0 {
		// The tail, not the head: in a long session the tracks that just
		// played are the ones most likely to be suggested again.
		queued := profile.Queued
		if len(queued) > 40 {
			queued = queued[len(queued)-40:]
		}
		lines = append(lines, "", "Already queued, do not suggest:")
		for _, track := range queued {
			lines = append(lines, "- "+track)
		}
	}

	lines = append(lines, "", fmt.Sprintf("Suggest %d tracks.", count))
	user = strings.Join(lines, "\n")
	return system, user
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
