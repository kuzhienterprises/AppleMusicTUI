package recommend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
)

func TestBuildProfileTalliesTheHistory(t *testing.T) {
	plays := []apple.Song{
		{Name: "One", Artist: "Alpha & Guest", Genres: []string{"Pop", "Dance"}},
		{Name: "Two", Artist: "Alpha", Genres: []string{"Pop"}},
		{Name: "Three", Artist: "Beta", Genres: []string{"Indie"}},
		{Name: "One", Artist: "Alpha & Guest", Genres: []string{"Pop", "Dance"}},
	}

	profile := BuildProfile(plays, []string{"Late Night"})

	// The repeat is dropped from the prompt but not from the tallies.
	if len(profile.Recent) != 3 || profile.Recent[0].Name != "One" {
		t.Errorf("the recent list lost its order: %+v", profile.Recent)
	}
	if len(profile.Artists) != 2 || profile.Artists[0].Name != "Alpha" || profile.Artists[0].Times != 3 {
		t.Errorf("artists = %+v, want Alpha three times first", profile.Artists)
	}
	if len(profile.Genres) != 3 || profile.Genres[0].Name != "Pop" || profile.Genres[0].Times != 3 {
		t.Errorf("genres = %+v, want Pop three times first", profile.Genres)
	}
	if len(profile.Playlists) != 1 || profile.Playlists[0] != "Late Night" {
		t.Errorf("playlists = %+v", profile.Playlists)
	}
}

// The prompt is the product of this package, so it is checked the way a layout
// would be: the things the model needs to know have to be in it.
func TestPromptCarriesTheHistory(t *testing.T) {
	profile := BuildProfile([]apple.Song{
		{Name: "Karma Police", Artist: "Radiohead", Album: "OK Computer", Genres: []string{"Alternative"}},
		{Name: "Sweater Weather", Artist: "The Neighbourhood"},
	}, []string{"Gym", "Late Night"})

	system, user := prompt(profile, 8)

	if !strings.Contains(user, "Karma Police | Radiohead | OK Computer") {
		t.Errorf("the recent plays are missing:\n%s", user)
	}
	if !strings.Contains(user, "Sweater Weather | The Neighbourhood\n") {
		t.Errorf("a track without an album grew a separator:\n%s", user)
	}
	if !strings.Contains(user, "Radiohead (1)") {
		t.Errorf("the artist tally is missing:\n%s", user)
	}
	if !strings.Contains(user, "Alternative (1)") {
		t.Errorf("the genre tally is missing:\n%s", user)
	}
	if !strings.Contains(user, "- Gym") || !strings.Contains(user, "- Late Night") {
		t.Errorf("the playlists are missing:\n%s", user)
	}
	if !strings.Contains(user, "Suggest 8 tracks.") {
		t.Errorf("the count is missing:\n%s", user)
	}
	if !strings.Contains(system, "Never repeat a track from the recent list") {
		t.Errorf("the system prompt does not forbid repeats:\n%s", system)
	}
}

// fakeModel answers with whatever the test wants, which is how the parsing and
// the tidying up around it are checked without a network.
type fakeModel struct {
	answer string
	err    error
	called bool
}

func (f *fakeModel) JSON(context.Context, string, map[string]any, string, string) ([]byte, error) {
	f.called = true
	return []byte(f.answer), f.err
}

func TestAskTidiesTheAnswer(t *testing.T) {
	model := &fakeModel{answer: `{"suggestions":[
		{"title":"  Karma Police  ","artist":" Radiohead ","why":" the obvious one "},
		{"title":"Karma Police","artist":"Radiohead","why":"duplicate"},
		{"title":"","artist":"Nobody","why":"empty title"},
		{"title":"Sweater Weather","artist":"The Neighbourhood","why":""}
	]}`}

	suggestions, err := Ask(context.Background(), model, Profile{}, 10)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !model.called {
		t.Fatal("the model was never asked")
	}
	if len(suggestions) != 2 {
		t.Fatalf("got %d suggestions, want 2: %+v", len(suggestions), suggestions)
	}
	if suggestions[0].Title != "Karma Police" || suggestions[0].Artist != "Radiohead" {
		t.Errorf("the suggestion was not trimmed: %+v", suggestions[0])
	}
	if suggestions[0].Why != "the obvious one" {
		t.Errorf("why = %q", suggestions[0].Why)
	}
}

func TestAskCapsTheAnswer(t *testing.T) {
	model := &fakeModel{answer: `{"suggestions":[
		{"title":"A","artist":"X","why":""},
		{"title":"B","artist":"X","why":""},
		{"title":"C","artist":"X","why":""}
	]}`}

	suggestions, err := Ask(context.Background(), model, Profile{}, 2)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(suggestions) != 2 {
		t.Errorf("got %d suggestions, want 2", len(suggestions))
	}
}

func TestAskWithoutAnythingUsable(t *testing.T) {
	model := &fakeModel{answer: `{"suggestions":[{"title":"","artist":"","why":""}]}`}

	if _, err := Ask(context.Background(), model, Profile{}, 5); err == nil {
		t.Fatal("an empty answer was accepted")
	}
}

func TestAskReportsTheModelsFailure(t *testing.T) {
	model := &fakeModel{err: errors.New("no key")}

	if _, err := Ask(context.Background(), model, Profile{}, 5); err == nil {
		t.Fatal("the model's error was swallowed")
	}
}

// resolver is a catalogue that knows a handful of tracks, and answers the way
// a search does: the best guess first, plus the neighbours.
func resolver(library ...apple.Song) Searcher {
	return func(_ context.Context, query string) ([]apple.Song, error) {
		var matches []apple.Song
		for _, song := range library {
			if strings.Contains(strings.ToLower(song.Name+" "+song.Artist), strings.ToLower(strings.SplitN(query, " ", 2)[0])) {
				matches = append(matches, song)
			}
		}
		return matches, nil
	}
}

func TestResolvePicksTheRightRecording(t *testing.T) {
	search := resolver(
		apple.Song{ID: "cover", Name: "Hallelujah", Artist: "Some Choir"},
		apple.Song{ID: "real", Name: "Hallelujah", Artist: "Jeff Buckley", Album: "Grace"},
	)

	picks, missed, err := Resolve(context.Background(), search, []Suggestion{
		{Title: "Hallelujah", Artist: "Jeff Buckley", Why: "the one that aches"},
	}, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(missed) != 0 {
		t.Errorf("missed = %+v", missed)
	}
	if len(picks) != 1 || picks[0].Song.ID != "real" {
		t.Fatalf("picks = %+v, want the Jeff Buckley recording", picks)
	}
	if picks[0].Why != "the one that aches" {
		t.Errorf("the reason was lost: %+v", picks[0])
	}
}

// The artist is what tells a cover from the recording that was meant, so a
// track by the wrong artist is a miss rather than a hit.
func TestResolveNeedsTheArtist(t *testing.T) {
	search := resolver(apple.Song{ID: "cover", Name: "Hallelujah", Artist: "Some Choir"})

	picks, missed, err := Resolve(context.Background(), search, []Suggestion{
		{Title: "Hallelujah", Artist: "Jeff Buckley"},
	}, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(picks) != 0 {
		t.Fatalf("picks = %+v, want none", picks)
	}
	if len(missed) != 1 {
		t.Fatalf("missed = %+v, want the suggestion", missed)
	}
}

func TestResolveDropsRepeats(t *testing.T) {
	search := resolver(apple.Song{ID: "one", Name: "Karma Police", Artist: "Radiohead"})

	picks, _, err := Resolve(context.Background(), search, []Suggestion{
		{Title: "Karma Police", Artist: "Radiohead"},
		{Title: "Karma Police", Artist: "Radiohead"},
	}, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(picks) != 1 {
		t.Errorf("got %d picks, want 1", len(picks))
	}
}

func TestResolveHonoursTheLimit(t *testing.T) {
	search := resolver(
		apple.Song{ID: "one", Name: "Karma Police", Artist: "Radiohead"},
		apple.Song{ID: "two", Name: "Sweater Weather", Artist: "The Neighbourhood"},
	)

	picks, _, err := Resolve(context.Background(), search, []Suggestion{
		{Title: "Karma Police", Artist: "Radiohead"},
		{Title: "Sweater Weather", Artist: "The Neighbourhood"},
	}, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(picks) != 1 {
		t.Errorf("got %d picks, want 1", len(picks))
	}
}

func TestFirstArtist(t *testing.T) {
	cases := map[string]string{
		"Radiohead":                           "Radiohead",
		"Lady Gaga & Bruno Mars":              "Lady Gaga",
		"Deepthi Suresh, Anirudh Ravichander": "Deepthi Suresh",
		"Calvin Harris feat. Dua Lipa":        "Calvin Harris",
		"Simon & Garfunkel":                   "Simon",
		"Earth, Wind & Fire":                  "Earth",
		"":                                    "",
	}
	for credit, want := range cases {
		if got := firstArtist(credit); got != want {
			t.Errorf("firstArtist(%q) = %q, want %q", credit, got, want)
		}
	}
}

func TestTitleScoreIgnoresCaseAndPunctuation(t *testing.T) {
	if score := titleScore("Don't Stop Me Now", "Dont Stop Me Now (Remastered)"); score == 0 {
		t.Error("a remastered title did not match")
	}
	if score := titleScore("Karma Police", "Karma Police"); score != 2 {
		t.Errorf("an exact title scored %d, want 2", score)
	}
	if score := titleScore("Karma Police", "Paranoid Android"); score != 0 {
		t.Errorf("different titles scored %d, want 0", score)
	}
}

// "Music" is Apple's catch-all genre and matches everything, so it says
// nothing and is left out of the tally.
func TestBuildProfileIgnoresTheCatchAllGenre(t *testing.T) {
	profile := BuildProfile([]apple.Song{
		{Name: "One", Artist: "X", Genres: []string{"Music", "Pop"}},
	}, nil)

	if len(profile.Genres) != 1 || profile.Genres[0].Name != "Pop" {
		t.Errorf("genres = %+v, want only Pop", profile.Genres)
	}
}
