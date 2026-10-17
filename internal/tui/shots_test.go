package tui

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/artwork"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
)

// shotArt is the URL the shot fixtures pretend to have fetched. The artwork
// package is never called from here: the cover is handed in as a decoded image,
// which is the same path a real download takes once it arrives.
const shotArt = "shot://cover"

// TestShotFrames writes the frames the README's images are made from. They are
// frames rather than screenshots because a full-screen interface cannot be
// drawn in a test, and because the artwork and the colours have to be chosen
// rather than inherited from whatever terminal happened to be open.
//
//	AM_SHOT=<dir> AM_SHOT_ART=<cover.jpg> go test ./internal/tui -run TestShotFrames -v
//
// AM_SHOT_MOTION=1 additionally writes the frames of the spectrum animation,
// numbered, for an encoder to pick up.
func TestShotFrames(t *testing.T) {
	dir := os.Getenv("AM_SHOT")
	if dir == "" {
		t.Skip("set AM_SHOT=<dir> to write the frames")
	}

	// A terminal that cannot do direct colour gets no colour at all, and this
	// one is not a terminal: it is a pipe that ends in a PNG.
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)

	cover := shotCover(t)

	write := func(name string, model playerModel) {
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(model.View()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Instant Crush, a third of the way in: a queue long enough to leave, and a
	// track long enough to have a position worth drawing.
	hero := shotModel(120, 34)
	hero.status.Index = 4
	hero.status.Position = 47
	hero.status.Duration = 337
	hero.pulse = 2
	if cover != nil {
		hero = withShotCover(hero, cover)
	}
	write("hero", hero)

	search := hero
	search.search = searchState{
		open:    true,
		query:   "get lucky",
		results: shotResults(),
		queued:  []bool{true, false, false, false},
		choice:  0,
	}
	write("search", search)

	queue := hero
	queue.showQueue = true
	queue.status.Index = 2
	queue.status.Position = 132
	queue.status.Duration = 544
	write("queue", queue)

	// The engine still coming up: the one moment where the interface is asking
	// for the user's patience.
	starting := shotModel(120, 34)
	starting.engine = nil
	starting.started = false
	starting.status = player.Status{Index: 0, Volume: 80}
	starting.pulse = 2
	if cover != nil {
		starting = withShotCover(starting, cover)
	}
	write("starting", starting)

	if os.Getenv("AM_SHOT_MOTION") == "" {
		return
	}

	// The animation: the spectrum moving, the clock ticking, the progress bar
	// creeping, the dots advancing on the status line.
	const frames = 72
	motion := hero
	motion.status.Position = 47
	for frame := 0; frame < frames; frame++ {
		motion.bands = shotBands(float64(frame) * 0.11)
		motion.pulse = frame / 3
		motion.status.Position = 47 + float64(frame)*0.04
		write(fmt.Sprintf("motion-%03d", frame), motion)
	}
}

// shotModel is a session with something playing, at a terminal size wide enough
// for the largest card the interface draws.
func shotModel(width, height int) playerModel {
	return playerModel{
		engine:       &stubEngine{},
		tracks:       shotTracks(),
		status:       player.Status{Index: 0, Position: 47, Duration: 337, Volume: 80},
		monitor:      fixedMonitor{bands: shotBands(0)},
		bands:        shotBands(0),
		palette:      defaultPalette,
		showSpectrum: true,
		started:      true,
		width:        width,
		height:       height,
	}
}

// shotTracks is an album played front to back, which is what makes the queue
// worth looking at.
func shotTracks() []player.Track {
	tracks := []struct {
		id, title, artist string
		seconds           int
	}{
		{"1", "Give Life Back to Music", "Daft Punk, Nile Rodgers", 274},
		{"2", "The Game of Love", "Daft Punk", 321},
		{"3", "Giorgio by Moroder", "Daft Punk, Giorgio Moroder", 544},
		{"4", "Within", "Daft Punk, Gonzales", 228},
		{"5", "Instant Crush", "Daft Punk, Julian Casablancas", 337},
		{"6", "Lose Yourself to Dance", "Daft Punk, Pharrell Williams, Nile Rodgers", 353},
		{"7", "Touch", "Daft Punk, Paul Williams, Nathan East", 498},
		{"8", "Get Lucky", "Daft Punk, Pharrell Williams, Nile Rodgers", 369},
		{"9", "Beyond", "Daft Punk, Paul Williams", 290},
		{"10", "Motherboard", "Daft Punk", 341},
		{"11", "Fragments of Time", "Daft Punk, Todd Edwards", 279},
		{"12", "Doin' It Right", "Daft Punk, Panda Bear", 251},
		{"13", "Contact", "Daft Punk, DJ Falcon", 381},
	}

	out := make([]player.Track, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, player.Track{
			ID:         track.id,
			Title:      track.title,
			Artist:     track.artist,
			Album:      "Random Access Memories",
			DurationMs: track.seconds * 1000,
			Artwork:    shotArt,
		})
	}
	return out
}

// shotResults is what a search looks like beside what is playing: near
// duplicates, one of them already queued.
func shotResults() []player.Track {
	return []player.Track{
		{ID: "r1", Title: "Get Lucky (feat. Pharrell Williams & Nile Rodgers)", Artist: "Daft Punk", Album: "Random Access Memories", DurationMs: 369000},
		{ID: "r2", Title: "Get Lucky (Radio Edit)", Artist: "Daft Punk, Pharrell Williams", Album: "Get Lucky (Radio Edit) - Single", DurationMs: 248000},
		{ID: "r3", Title: "Get Lucky", Artist: "The Baseballs", Album: "Hit Me Baby...", DurationMs: 209000},
		{ID: "r4", Title: "Get Lucky (Daft Punk Remix)", Artist: "Daft Punk", Album: "Get Lucky - Single", DurationMs: 340000},
		{ID: "r5", Title: "Get Lucky (Karaoke Version)", Artist: "High School Karaoke", Album: "Karaoke Hits", DurationMs: 369000},
	}
}

// shotCover downloads the cover AM_SHOT_ART points at, through the same call the
// interface makes, box filter and all, so the picture in the shot is the one the
// interface would really have drawn. Without it the frames are written without a
// cover, which is still a valid state to look at.
func shotCover(t *testing.T) image.Image {
	template := os.Getenv("AM_SHOT_ART")
	if template == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cover, err := artwork.Load(ctx, template, artColumns)
	if err != nil {
		t.Fatalf("loading the cover: %v", err)
	}
	return cover
}

// withShotCover runs the frame through the same message a real download sends,
// so the cover, the accent colour and the gradient are the ones the interface
// would really have drawn.
func withShotCover(model playerModel, cover image.Image) playerModel {
	updated, _ := model.Update(artMsg{url: model.currentTrack().Artwork, img: cover})
	return updated.(playerModel)
}

// shotBands is a spectrum with something in the low end, a shelf of energy
// moving through the middle, and a wobble on top, so consecutive frames differ.
func shotBands(phase float64) []float64 {
	bands := make([]float64, 32)
	for i := range bands {
		position := float64(i) / float64(len(bands)-1)

		energy := 0.88 * math.Exp(-2.0*position)
		shelf := 0.24 * math.Exp(-math.Pow((position-0.30-0.06*math.Sin(phase))*4.0, 2))
		wobble := 0.78 + 0.22*math.Sin(float64(i)*0.85+phase*1.7)
		bass := 0.20 * math.Exp(-math.Pow(position*6.0, 2)) * (0.5 + 0.5*math.Sin(phase*2.3))

		level := (energy + shelf) * wobble
		level += bass
		if level < 0 {
			level = 0
		}
		if level > 1 {
			level = 1
		}
		bands[i] = level
	}
	return bands
}
