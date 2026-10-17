package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
)

// fixedMonitor stands in for the spectrum source so the layout can be checked
// without an audio device.
type fixedMonitor struct{ bands []float64 }

func (f fixedMonitor) Bands() []float64 { return f.bands }
func (f fixedMonitor) Live() bool       { return true }
func (f fixedMonitor) Close() error     { return nil }

func testBands() []float64 {
	// A shape that reads like music: energy leaning low, wobbling as it goes,
	// and never actually hitting zero the way a pure tone would.
	bands := make([]float64, 32)
	for i := range bands {
		position := float64(i) / float64(len(bands)-1)
		energy := math.Exp(-1.8 * position)
		wobble := 0.72 + 0.28*math.Sin(float64(i)*0.8)
		bands[i] = energy * wobble
	}
	return bands
}

func fullBands() []float64 {
	bands := make([]float64, 32)
	for i := range bands {
		bands[i] = 1
	}
	return bands
}

func testModel(width, height int) playerModel {
	bands := testBands()
	return playerModel{
		engine: &stubEngine{},
		tracks: []player.Track{
			{
				Title:      "Varnajaalam",
				Artist:     "Vishnu Vijay, Vinayak Sasikumar, K S Harisankar & Poornima Kannan",
				Album:      "Bethlehem Kudumba Unit (Original Motion Picture Soundtrack)",
				DurationMs: 203000,
			},
			{Title: "Karma Police", Artist: "Radiohead", Album: "OK Computer", DurationMs: 264000},
		},
		status:       player.Status{Index: 0, Position: 23, Duration: 203, Volume: 100},
		monitor:      fixedMonitor{bands: bands},
		bands:        bands,
		palette:      defaultPalette,
		showSpectrum: true,
		width:        width,
		height:       height,
	}
}

func viewLines(view string) []string { return strings.Split(view, "\n") }

// Every line has to fit the window, or the card wraps and the centring falls
// apart in a way that looks like corruption rather than a layout bug. It also
// has to fit vertically: a frame taller than the terminal scrolls the
// alternate screen and drags its own border out of view.
func TestPlayerViewFitsTheWindow(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{120, 40}, {100, 30}, {80, 24}, {70, 20}, {60, 18}, {52, 20}, {44, 16}, {40, 14},
	} {
		for _, overlay := range []string{"", "queue", "help", "search", "starting", "playing"} {
			model := testModel(size.width, size.height)
			switch overlay {
			case "queue":
				model.showQueue = true
			case "help":
				model.showHelp = true
			case "search":
				model = searchingModel(size.width, size.height)
			case "starting":
				// The line that carries the pulse is drawn at every width,
				// because it is the only sign of life while the engine boots.
				model.engine = nil
			case "playing":
				model.started = true
			}

			view := model.View()
			height := len(viewLines(view))
			if height > size.height {
				t.Errorf("%dx%d (%s): the frame is %d rows tall",
					size.width, size.height, overlay, height)
			}
			for number, line := range viewLines(view) {
				if got := lipgloss.Width(line); got > size.width {
					t.Errorf("%dx%d (%s): line %d is %d cells wide: %q",
						size.width, size.height, overlay, number+1, got, line)
				}
			}
		}
	}
}

// Picking one search result now queues just that result, so a one-track queue
// is the ordinary way into the player. The up-next line has to say there is
// nothing after this one — and point at the key that changes that — while still
// fitting the frame at the widths where it has the least room. The old wording
// ("nothing, this is the last track") was wider than the narrowest card and put
// the border out of place.
func TestTheLastTrackPointsAtSearchAndFitsTheFrame(t *testing.T) {
	for _, width := range []int{40, 44, 52, 60, 80, 100} {
		model := testModel(width, 24)
		model.tracks = model.tracks[:1]

		for number, line := range viewLines(model.View()) {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("%d wide: line %d is %d cells: %q", width, number+1, got, line)
			}
		}

		if content := clampInt(width-frameMargin*2-6, minContentWidth, maxContentWidth); content <= minContentWidth {
			line := model.upNextLine(content)
			if !strings.Contains(line, "/") {
				t.Errorf("%d wide: the last-track line does not mention /: %q", width, line)
			}
		}
	}

	// The line is actually reached by the renderer, not just by the helper.
	single := testModel(100, 30)
	single.tracks = single.tracks[:1]
	if view := single.View(); !strings.Contains(view, "nothing yet") {
		t.Error("the up-next line is not drawn for a one-track queue")
	}
}

// With one track in the queue, n and p have nowhere to go. Asking the engine
// anyway is not harmless: mpv reaches the end of the playlist and the player
// quits, so a skipped track looks like a crash. The keys have to reach nothing.
func TestSkippingAOneTrackQueueIsANoOp(t *testing.T) {
	engine := &stubEngine{}
	model := testModel(100, 30)
	model.engine = engine
	model.tracks = model.tracks[:1]

	for _, key := range []string{"n", "p"} {
		_, cmd := model.handleKey(press(key))
		if cmd == nil {
			continue
		}

		// Run it anyway, so the engine count below still means something.
		t.Errorf("%q produced work with one track queued", key)
		cmd()
	}
	if engine.nexts != 0 {
		t.Errorf("the engine was asked to skip %d times", engine.nexts)
	}

	// With somewhere to go, the key reaches the engine again.
	model.tracks = append(model.tracks, player.Track{Title: "Karma Police"})
	_, cmd := model.handleKey(press("n"))
	if cmd == nil {
		t.Fatal("expected n to reach the engine once the queue is longer")
	}
	cmd()
	if engine.nexts != 1 {
		t.Errorf("nexts = %d after one skip, want 1", engine.nexts)
	}
}

// The card has to be a frame: a border on every side, and the same width on
// every line.
func TestPlayerCardIsAFrame(t *testing.T) {
	view := viewLines(testModel(100, 30).View())

	content := 0
	for i, line := range view {
		if strings.ContainsAny(line, "╭╰") {
			content = i
			break
		}
	}
	if !strings.Contains(view[content], "╭") || !strings.Contains(view[content], "╮") {
		t.Fatalf("the first frame line is not a top border: %q", view[content])
	}

	// Every line from the top border to the bottom border must be the same
	// width, or the frame is ragged.
	width := lipgloss.Width(view[content])
	for i := content; i < len(view); i++ {
		if strings.TrimSpace(view[i]) == "" {
			continue
		}
		if got := lipgloss.Width(view[i]); got != width {
			t.Errorf("frame line %d is %d cells, want %d: %q", i, got, width, view[i])
		}
		if strings.ContainsAny(view[i], "╰") {
			break
		}
	}
}

// The complaint this fixes: the player used to sit in the top-left corner.
func TestPlayerViewIsCentred(t *testing.T) {
	const width, height = 100, 30
	view := viewLines(testModel(width, height).View())

	blankAbove := 0
	for _, line := range view {
		if strings.TrimSpace(line) == "" {
			blankAbove++
			continue
		}
		break
	}
	if blankAbove == 0 {
		t.Error("expected blank lines above the card, so it is vertically centred")
	}

	firstContent := 0
	for i, line := range view {
		if strings.TrimSpace(line) != "" {
			firstContent = i
			break
		}
	}
	indent := len(view[firstContent]) - len(strings.TrimLeft(view[firstContent], " "))
	if indent < 5 {
		t.Errorf("expected the card to be indented from the left, got %d spaces", indent)
	}
}

// The spectrum is the centrepiece, so it gets two rows and fills the column.
func TestSpectrumRowsFillTheWidth(t *testing.T) {
	model := testModel(100, 30)

	for _, width := range []int{20, 32, 40, 50} {
		model.bands = fullBands()

		rows := model.spectrumLines(width)
		if len(rows) != spectrumRows {
			t.Fatalf("width %d: got %d rows, want %d", width, len(rows), spectrumRows)
		}
		for row, line := range rows {
			if got := lipgloss.Width(line); got != width {
				t.Errorf("width %d row %d: %d cells, want %d", width, row, got, width)
			}
			if line != strings.Repeat("█", width) {
				t.Errorf("width %d row %d: full bands should be solid", width, row)
			}
		}

		// Silence draws nothing at all, rather than a row of nubs.
		model.bands = make([]float64, 32)
		for row, line := range model.spectrumLines(width) {
			if strings.TrimSpace(line) != "" {
				t.Errorf("width %d row %d: silence should be blank, got %q", width, row, line)
			}
		}
	}
}

// Two rows are only worth it if they stack into one tall bar.
func TestSpectrumStacksRowsIntoOneBar(t *testing.T) {
	model := testModel(100, 30)
	model.bands = make([]float64, 32)
	for i := range model.bands {
		model.bands[i] = 0.5 // exactly half of the sixteen steps
	}

	rows := model.spectrumLines(16)
	if bottom := rows[spectrumRows-1]; bottom != strings.Repeat("█", 16) {
		t.Errorf("bottom row = %q, want full blocks at half level", bottom)
	}
	if top := rows[0]; strings.TrimSpace(top) != "" {
		t.Errorf("top row = %q, want empty at half level", top)
	}

	// Three quarters should spill into the top row.
	for i := range model.bands {
		model.bands[i] = 0.75
	}
	if top := model.spectrumLines(16)[0]; strings.TrimSpace(top) == "" {
		t.Error("expected the top row to be part of the bar above half level")
	}
}

func TestSpectrumAppearsInTheView(t *testing.T) {
	model := testModel(100, 30)
	model.bands = fullBands()

	// At a 100 column window the card is at its cap, so the right column is
	// maxContentWidth - artColumns - artGap wide.
	column := maxContentWidth - artColumns - artGap
	if !strings.Contains(model.View(), strings.Repeat("█", column)) {
		t.Errorf("expected a full-width spectrum row of %d cells inside the card", column)
	}

	model.showSpectrum = false
	if strings.Contains(model.View(), "█") {
		t.Error("expected the spectrum to disappear when toggled off")
	}
}

// Narrow windows drop the cover, then the spectrum, rather than squashing.
func TestNarrowWindowsDropTheCoverThenTheSpectrum(t *testing.T) {
	model := testModel(80, 24)

	if !model.hasArt(66) {
		t.Error("a 66 cell card should still show the cover")
	}
	if model.hasArt(46) {
		t.Error("a 46 cell card should drop the cover")
	}
	if !model.spectrumVisible(50) {
		t.Error("a 50 cell card should still show the spectrum")
	}
	if model.spectrumVisible(30) {
		t.Error("a 30 cell card should drop the spectrum")
	}

	// Whatever it drops, the result still has to fit.
	for _, width := range []int{40, 46, 52, 60} {
		view := testModel(width, 20).View()
		for number, line := range viewLines(view) {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("%d cols: line %d is %d cells", width, number+1, got)
			}
		}
	}
}

// Apple joins a credit into one string; the player shortens it back down.
func TestSummarizeArtistsFoldsLongCredits(t *testing.T) {
	for _, test := range []struct{ credit, want string }{
		{"Radiohead", "Radiohead"},
		{"", ""},
		{"Vishnu Vijay & Vinayak Sasikumar", "Vishnu Vijay & Vinayak Sasikumar"},
		{
			"Vishnu Vijay, Vinayak Sasikumar, K S Harisankar & Poornima Kannan",
			"Vishnu Vijay +3",
		},
	} {
		if got := summarizeArtists(test.credit); got != test.want {
			t.Errorf("summarizeArtists(%q) = %q, want %q", test.credit, got, test.want)
		}
	}
}

func TestGradientRampsBetweenItsEnds(t *testing.T) {
	if got := lerpHex("#000000", "#ffffff", 0); got != "#000000" {
		t.Errorf("start of the ramp = %s, want #000000", got)
	}
	if got := lerpHex("#000000", "#ffffff", 1); got != "#ffffff" {
		t.Errorf("end of the ramp = %s, want #ffffff", got)
	}
	if got := lerpHex("#000000", "#ffffff", 0.5); got != "#808080" {
		t.Errorf("middle of the ramp = %s, want #808080", got)
	}
	if len(defaultPalette) != paletteSteps {
		t.Errorf("default palette has %d steps, want %d", len(defaultPalette), paletteSteps)
	}
}

// Half blocks only buy resolution if the two pixels land in the right halves:
// the upper pixel as the foreground of the glyph and the lower one as the
// background. Swapping them would still look like a picture, just upside down.
func TestArtCellSplitsThePixels(t *testing.T) {
	cell := artCell(color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255})

	if got := cell.GetForeground(); got != lipgloss.Color("#ff0000") {
		t.Errorf("foreground = %v, want the upper pixel's colour", got)
	}
	if got := cell.GetBackground(); got != lipgloss.Color("#0000ff") {
		t.Errorf("background = %v, want the lower pixel's colour", got)
	}
}

// The renderer has to pair pixels up, not repeat a row.
func TestRenderArtPairsRowsIntoOneLine(t *testing.T) {
	cover := image.NewRGBA(image.Rect(0, 0, 2, 2))
	cover.Set(0, 0, color.RGBA{R: 255, A: 255})
	cover.Set(0, 1, color.RGBA{B: 255, A: 255})

	rows := renderArt(cover, 2, 1)
	if len(rows) != 1 {
		t.Fatalf("got %d rows for a 2x2 cover drawn over 1 row, want 1", len(rows))
	}
	if !strings.Contains(rows[0], "▀") {
		t.Errorf("expected a half-block glyph, got %q", rows[0])
	}
	if got := lipgloss.Width(rows[0]); got != 2 {
		t.Errorf("row is %d cells wide, want 2", got)
	}
}

// The header strip is laid out to the cell, at every width it can be asked for.
func TestHeaderStripFillsTheContentWidth(t *testing.T) {
	model := testModel(100, 30)

	for width := minContentWidth; width <= maxContentWidth; width++ {
		if got := lipgloss.Width(model.headerLine(width)); got != width {
			t.Errorf("headerLine(%d) is %d cells wide", width, got)
		}
	}

	// Even at the floor, the volume survives: it is the only place that state
	// is shown.
	if line := model.headerLine(minContentWidth); !strings.Contains(line, "100%") {
		t.Errorf("narrow header dropped the volume: %q", line)
	}
}

// A footer that fills the first line and strands one hint on a line of its own
// reads like a bug, so the wrap evens the lines out.
func TestWrapHintsEvensTheLinesOut(t *testing.T) {
	hints := []string{"space play", "←→ seek", "↑↓ vol", "n/p track"}

	// Wide enough for one line, so one line it is.
	if got := wrapHints(hints, 60); len(got) != 1 {
		t.Errorf("wrapHints(60) made %d lines, want 1: %q", len(got), got)
	}

	// Too wide for one line: split it down the middle rather than leaving a
	// lone hint on the second line.
	lines := wrapHints(hints, 38)
	if len(lines) != 2 {
		t.Fatalf("wrapHints(38) made %d lines, want 2: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "space play") || !strings.Contains(lines[0], "←→ seek") {
		t.Errorf("first line = %q, want the first pair of hints", lines[0])
	}
	if !strings.Contains(lines[1], "↑↓ vol") || !strings.Contains(lines[1], "n/p track") {
		t.Errorf("second line = %q, want the second pair of hints", lines[1])
	}

	// Nothing may be dropped, whatever the width.
	for _, width := range []int{26, 30, 34, 40, 72} {
		joined := strings.Join(wrapHints(hints, width), " ")
		for _, hint := range hints {
			if !strings.Contains(joined, hint) {
				t.Errorf("wrapHints(%d) lost %q", width, hint)
			}
		}
	}

	if got := wrapHints(nil, 40); got != nil {
		t.Errorf("wrapHints(nil) = %q, want nothing", got)
	}
}

// A cover arriving from the download goroutine has to reach the model, and a
// cover for a track that is no longer playing must not.
func TestArtMessageReachesTheModel(t *testing.T) {
	model := testModel(100, 30)
	model.tracks[0].Artwork = "https://example.test/cover/{w}x{h}bb.jpg"

	cover := image.NewRGBA(image.Rect(0, 0, artColumns, artRows*2))
	for y := 0; y < artRows*2; y++ {
		for x := 0; x < artColumns; x++ {
			cover.Set(x, y, color.RGBA{R: uint8(12 * x), G: 40, B: 200, A: 255})
		}
	}

	updated, _ := model.Update(artMsg{url: model.tracks[0].Artwork, img: cover})
	loaded, ok := updated.(playerModel)
	if !ok {
		t.Fatal("expected a player model")
	}

	if len(loaded.art) != artRows {
		t.Errorf("art has %d rows, want %d", len(loaded.art), artRows)
	}
	if loaded.artURL != model.tracks[0].Artwork {
		t.Errorf("artURL = %q, want the track's template", loaded.artURL)
	}
	if loaded.tint == "" {
		t.Error("expected the cover to supply an accent colour")
	}
	if len(loaded.palette) != paletteSteps {
		t.Errorf("palette has %d steps, want %d", len(loaded.palette), paletteSteps)
	}
	if height := len(viewLines(loaded.View())); height > 30 {
		t.Errorf("the frame grew to %d rows once a cover was loaded", height)
	}

	// A late cover for a track that is no longer on screen changes nothing.
	stale, _ := model.Update(artMsg{url: "https://example.test/other/{w}x{h}bb.jpg", img: cover})
	if got := stale.(playerModel).artURL; got != "" {
		t.Errorf("a cover for another track was accepted: artURL = %q", got)
	}
}

// A credit that already fits is left exactly as Apple wrote it: folding is for
// making things fit, not a house style, and "Earth, Wind & Fire" is one band.
func TestShortCreditsAreLeftAlone(t *testing.T) {
	model := testModel(100, 30)
	model.tracks[0].Artist = "Earth, Wind & Fire"
	model.tracks[0].Title = "September"
	model.tracks[0].Album = "The Best of Earth, Wind & Fire Vol. 1"

	if view := model.View(); !strings.Contains(view, "Earth, Wind & Fire") {
		t.Error("a credit that fits was folded anyway")
	}
}

// The cover is drawn with half blocks, two pixels per cell.
func TestRenderArtUsesHalfBlocks(t *testing.T) {
	rows := artPlaceholder(artColumns, artRows)
	if len(rows) != artRows {
		t.Fatalf("placeholder has %d rows, want %d", len(rows), artRows)
	}
	for _, row := range rows {
		if got := lipgloss.Width(row); got != artColumns {
			t.Fatalf("placeholder row is %d cells, want %d", got, artColumns)
		}
	}
}

// Wide characters are two cells each; clipping by rune count would push the
// card off centre.
func TestClipRespectsDisplayWidth(t *testing.T) {
	for _, text := range []string{"東京タワーはとても高い", "golden hour", "日本語 mixed with latin"} {
		for width := 3; width <= 20; width++ {
			if got := lipgloss.Width(clip(text, width)); got > width {
				t.Errorf("clip(%q, %d) = %d cells wide", text, width, got)
			}
		}
	}
}

// The spectrum runs on its own clock, separate from the engine poll. If the
// chain ever stops re-arming, the bars would freeze after a single frame.
func TestSpectrumTickKeepsGoing(t *testing.T) {
	model := testModel(100, 30)

	if model.Init() == nil {
		t.Error("expected Init to schedule work")
	}

	updated, cmd := model.Update(bandsMsg(testBands()))
	if cmd == nil {
		t.Fatal("expected another frame to be scheduled")
	}
	drawn, ok := updated.(playerModel)
	if !ok || len(drawn.bands) != 32 {
		t.Fatal("expected the frame to reach the model")
	}

	// Hiding the spectrum should stop the work, not merely hide the output.
	drawn.showSpectrum = false
	if _, cmd := drawn.Update(bandsMsg(testBands())); cmd != nil {
		t.Error("expected no further frames while the spectrum is hidden")
	}

	// And v brings it back.
	restored, cmd := drawn.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if cmd == nil {
		t.Error("expected v to restart the frames")
	}
	if shown, ok := restored.(playerModel); !ok || !shown.showSpectrum {
		t.Error("expected v to switch the spectrum back on")
	}
}

// Esc is the natural way out of an overlay, and it must not quit the player by
// accident.
func TestEscClosesOverlaysBeforeQuitting(t *testing.T) {
	model := testModel(80, 24)
	model.showHelp = true

	closed, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("expected esc to close the help panel without quitting")
	}
	if left, ok := closed.(playerModel); !ok || left.showHelp {
		t.Error("expected the help panel to be closed")
	}

	// With nothing open, esc quits.
	model.showHelp = false
	if _, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEsc}); cmd == nil {
		t.Error("expected esc to quit when no overlay is open")
	}
}

// press builds the key event a terminal sends for printable text.
func press(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

// searchResults are deliberately awkward: results are where long titles and
// repeated album names turn up, and the panel has to clip them rather than
// widen the card.
func searchResults() []player.Track {
	return []player.Track{
		{ID: "1", Title: "Mercy (feat. Big Sean, Pusha T & 2 Chainz)", Artist: "Kanye West", Album: "Kanye West Presents Good Music Cruel Summer"},
		{ID: "2", Title: "Mercy", Artist: "Badshah", Album: "ONE (Original Never Ends)"},
		{ID: "3", Title: "Mercy (Acoustic)", Artist: "Shawn Mendes", Album: "Illuminate (Deluxe)"},
		{ID: "4", Title: "Mercy (Workout Mix 150 BPM)", Artist: "Power Music Workout", Album: "Workout Motivation 2017"},
	}
}

func searchingModel(width, height int) playerModel {
	model := testModel(width, height)
	model.search = searchState{
		open:    true,
		query:   "mercy",
		results: searchResults(),
		queued:  make([]bool, len(searchResults())),
	}
	return model
}

// Searching from inside the player is the whole point of the panel: the queue
// grows without quitting, and quitting is what would end the playback being
// listened to.
func TestSearchPanelFindsAndQueues(t *testing.T) {
	engine := &stubEngine{}
	model := testModel(100, 30)
	model.engine = engine
	model.searcher = func(_ context.Context, query string) ([]player.Track, error) {
		if query != "mercy" {
			t.Errorf("searched for %q, want %q", query, "mercy")
		}
		return searchResults()[:2], nil
	}

	// "/" opens the panel in typing mode.
	opened, _ := model.handleKey(press("/"))
	model = opened.(playerModel)
	if !model.search.open || !model.search.editing {
		t.Fatal("expected / to open the search panel in typing mode")
	}

	// Letters are letters in here. Out in the player, "n" skips a track and
	// "q" quits; in the search box they have to be words.
	typed, cmd := model.handleKey(press("nq"))
	if cmd != nil {
		t.Error("expected typing to do nothing but type")
	}
	model = typed.(playerModel)
	if model.search.query != "nq" {
		t.Errorf("query = %q, want %q", model.search.query, "nq")
	}
	if engine.nexts != 0 {
		t.Error("typing in the search box reached the transport")
	}

	// Backspace takes it back out again, one character at a time.
	for i := 0; i < 2; i++ {
		erased, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
		model = erased.(playerModel)
	}
	if model.search.query != "" {
		t.Fatalf("query = %q after two backspaces, want it empty", model.search.query)
	}

	typed, _ = model.handleKey(press("mercy"))
	model = typed.(playerModel)

	// Enter runs the query.
	searched, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = searched.(playerModel)
	if cmd == nil {
		t.Fatal("expected enter to start the search")
	}
	message, ok := cmd().(searchMsg)
	if !ok || message.err != nil || len(message.results) != 2 {
		t.Fatalf("searchMsg = %+v, want two results", message)
	}

	// The results land in the model and the panel moves to choosing.
	loaded, _ := model.Update(message)
	model = loaded.(playerModel)
	if len(model.search.results) != 2 || model.search.editing {
		t.Fatalf("got %d results with editing=%v, want two results and no editing",
			len(model.search.results), model.search.editing)
	}

	// Down chooses the second one, and enter adds it.
	moved, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	model = moved.(playerModel)
	if model.search.choice != 1 {
		t.Fatalf("choice = %d, want 1", model.search.choice)
	}

	added, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = added.(playerModel)
	if cmd == nil {
		t.Fatal("expected enter to add the chosen track")
	}
	enqueued, ok := cmd().(enqueuedMsg)
	if !ok {
		t.Fatal("expected the engine call to report an enqueued track")
	}

	// The engine has it, and the queue on screen keeps up with it.
	if len(engine.added) != 1 || engine.added[0].ID != "2" {
		t.Fatalf("the engine was given %+v, want the second result", engine.added)
	}

	grown, _ := model.Update(enqueued)
	model = grown.(playerModel)
	if len(model.tracks) != 3 {
		t.Errorf("the queue is %d tracks, want 3", len(model.tracks))
	}
	if !model.search.queued[1] {
		t.Error("expected the added result to be ticked")
	}

	// Esc closes the panel and leaves playback alone.
	closed, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("expected esc to close the panel without quitting")
	}
	if panel := closed.(playerModel).search; panel.open {
		t.Error("expected the search panel to be closed")
	}
}

// The panel has to say what it is doing: the query, the results, what is
// already queued, and which keys now apply.
func TestSearchPanelDrawsItsState(t *testing.T) {
	model := searchingModel(100, 30)
	model.search.queued[1] = true

	view := model.View()
	for _, want := range []string{"mercy", "Badshah", "✓", "add to queue"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected the panel to show %q", want)
		}
	}
	if strings.Contains(view, "seek") {
		t.Error("expected the transport hints to give way while searching")
	}

	// While the query is still being typed the footer says so instead.
	model.search.editing = true
	if view := model.View(); !strings.Contains(view, "enter to search") {
		t.Error("expected the typing hints while the query is being typed")
	}
}

// The panel is swapped into the column the details were drawn in, so opening it
// does not resize the card. A frame that jumped on every search would be its
// own kind of noise.
func TestSearchPanelDoesNotResizeTheCard(t *testing.T) {
	model := testModel(100, 30)

	before := viewLines(model.View())
	after := viewLines(searchingModel(100, 30).View())

	if len(before) != len(after) {
		t.Errorf("the card is %d rows normally and %d rows while searching", len(before), len(after))
	}
	if len(before) == 0 || lipgloss.Width(before[0]) != lipgloss.Width(after[0]) {
		t.Errorf("the card is %d cells wide normally and %d while searching",
			lipgloss.Width(before[0]), lipgloss.Width(after[0]))
	}
}

// A result set that arrives after the query moved on belongs to a search the
// user has already replaced.
func TestStaleSearchResultsAreDropped(t *testing.T) {
	model := testModel(80, 24)
	model.search = searchState{open: true, editing: true, query: "mercy", searching: true}

	updated, _ := model.Update(searchMsg{query: "dancing", results: searchResults()})
	if got := updated.(playerModel).search.results; len(got) != 0 {
		t.Errorf("expected the stale result set to be dropped, got %d results", len(got))
	}
}

// A track the engine will not take must not appear in the queue anyway.
func TestAFailedEnqueueLeavesTheQueueAlone(t *testing.T) {
	model := searchingModel(100, 30)

	updated, _ := model.Update(enqueueErrMsg{err: errors.New("NOT_FOUND")})
	left := updated.(playerModel)

	if len(left.tracks) != 2 {
		t.Errorf("the queue is %d tracks, want the original 2", len(left.tracks))
	}
	if left.search.adding {
		t.Error("expected the panel to stop reporting an addition in progress")
	}
	if left.search.err == nil {
		t.Error("expected the failure to be reported")
	}
	if !strings.Contains(left.View(), "NOT_FOUND") {
		t.Error("expected the failure on screen")
	}
}

// stubEngine stands in for a playback engine so the program can be run
// headlessly, with no browser, no mpv and no audio device.
type stubEngine struct {
	status player.Status
	closed bool
	// added records what Enqueue was given, so the search panel's effect on
	// the queue can be checked.
	added []player.Track
	// nexts counts transport calls, which is how a test proves that typing
	// into the search box never reached the transport.
	nexts int
}

func (s *stubEngine) Play(context.Context, int) error             { return nil }
func (s *stubEngine) TogglePause(context.Context) error           { return nil }
func (s *stubEngine) Seek(context.Context, float64) error         { return nil }
func (s *stubEngine) Next(context.Context) error                  { s.nexts++; return nil }
func (s *stubEngine) Previous(context.Context) error              { return nil }
func (s *stubEngine) AdjustVolume(context.Context, float64) error { return nil }
func (s *stubEngine) Close() error                                { s.closed = true; return nil }
func (s *stubEngine) Enqueue(_ context.Context, tracks []player.Track) error {
	s.added = append(s.added, tracks...)
	return nil
}
func (s *stubEngine) Status(context.Context) (player.Status, error) {
	return s.status, nil
}

// The wait for the engine belongs on screen: choosing a track should land in
// the player, not in a blank pause with no way to tell "starting" from
// "broken".
func TestTheInterfaceWaitsForTheEngineOnScreen(t *testing.T) {
	model := testModel(100, 30)
	model.engine = nil
	model.start = func() tea.Cmd {
		return func() tea.Msg { return engineReadyMsg{engine: &stubEngine{}} }
	}

	if model.Init() == nil {
		t.Error("expected Init to schedule the engine start")
	}

	// Nothing to control yet, and the player says what it is waiting for.
	if cmd := model.control(func(context.Context, player.Engine) error { return nil }); cmd != nil {
		t.Error("expected control keys to be inert while the engine starts")
	}
	if notice := model.noticeLine(72); !strings.Contains(notice, "Initializing") {
		t.Errorf("noticeLine = %q, want the starting notice", notice)
	}
	if !strings.Contains(model.View(), "◌") {
		t.Error("expected the waiting glyph in place of the transport icon")
	}

	// The engine arriving starts the polling and clears the notice.
	updated, cmd := model.Update(engineReadyMsg{engine: &stubEngine{}})
	loaded, ok := updated.(playerModel)
	if !ok {
		t.Fatal("expected a player model")
	}
	if loaded.engine == nil {
		t.Error("expected the engine to be installed")
	}
	if loaded.start != nil {
		t.Error("expected the lazy start to be cleared")
	}
	if cmd == nil {
		t.Error("expected status polling to begin")
	}
	if notice := loaded.noticeLine(72); notice != "" {
		t.Errorf("noticeLine = %q, want nothing once the engine is up", notice)
	}
	if !strings.Contains(loaded.View(), "▶") {
		t.Error("expected the transport glyph back once the engine is up")
	}
}

// The status line is the only thing that moves while the engine boots, so the
// pulse has to actually advance, and every frame has to be a single cell wide:
// the line it sits on is measured rather than counted.
func TestThePulseGrowsFromAPointIntoACircle(t *testing.T) {
	model := testModel(80, 24)
	model.engine = nil

	want := []string{"·", "•", "●", "•"}
	for frame, shape := range want {
		model.pulse = frame
		if got := model.pulseFrame(); got != shape {
			t.Errorf("pulse %d = %q, want %q", frame, got, shape)
		}
		if width := lipgloss.Width(shape); width != 1 {
			t.Errorf("pulse frame %q is %d cells wide", shape, width)
		}
	}

	// The cycle wraps rather than running off the end of the frames.
	model.pulse = len(want)
	if got := model.pulseFrame(); got != want[0] {
		t.Errorf("pulse %d = %q, want the first frame again", model.pulse, got)
	}

	// The tick is what moves it, and it has to re-arm or it moves once.
	updated, cmd := model.Update(pulseMsg{})
	advanced, ok := updated.(playerModel)
	if !ok || advanced.pulse != len(want)+1 {
		t.Fatalf("pulse after one tick = %d, want %d", advanced.pulse, len(want)+1)
	}
	if cmd == nil {
		t.Error("expected the status line to schedule its next frame")
	}
}

// "Playing" carries dots that move while the track does. A status line that has
// stopped moving is how a stalled engine looks, and there is nowhere else on
// screen that would show it.
func TestTheStatusLineAnimatesWhilePlaying(t *testing.T) {
	model := testModel(80, 24)
	model.engine = &stubEngine{}
	model.started = true

	for pulse, want := range []string{"Playing.", "Playing..", "Playing...", "Playing."} {
		model.pulse = pulse
		if line := model.stateLine(72); !strings.Contains(line, want) {
			t.Errorf("pulse %d = %q, want it to contain %q", pulse, line, want)
		}
	}

	// A paused track has nothing to animate.
	model.status.Paused = true
	if line := model.stateLine(72); !strings.Contains(line, "Paused") || strings.Contains(line, ".") {
		t.Errorf("paused line = %q, want a still Paused", line)
	}
	model.status.Paused = false

	// Nothing is claimed before the engine has reported playback beginning.
	model.started = false
	if line := model.stateLine(72); line != "" {
		t.Errorf("status line = %q, want nothing before playback starts", line)
	}
}

// A short terminal gives the transport report up before it gives the keys up:
// "Playing..." is worth a row, but not a row that the keys were using.
func TestAShortCardKeepsTheKeysAndDropsTheTransportReport(t *testing.T) {
	roomy := testModel(40, 16)
	roomy.started = true

	if view := roomy.View(); !strings.Contains(view, "Playing") {
		t.Fatal("expected the transport report where the card has a row to spare")
	}

	cramped := testModel(40, 14)
	cramped.started = true

	view := cramped.View()
	if strings.Contains(view, "Playing") {
		t.Error("expected the transport report to be the first thing given up")
	}
	if !strings.Contains(view, "space play") {
		t.Error("expected the keys to survive the transport report")
	}
}

func TestAFailedEngineStartEndsTheInterface(t *testing.T) {
	model := testModel(100, 30)
	model.engine = nil

	updated, cmd := model.Update(engineErrMsg{err: errors.New("no browser")})
	if cmd == nil {
		t.Fatal("expected a quit")
	}
	if failed, ok := updated.(playerModel); !ok || failed.err == nil {
		t.Error("expected the error to be kept so the caller can return it")
	}
}

// A quit during startup must not leave a browser running with nothing attached
// to it.
func TestShutdownClosesAnEngineThatAlreadyArrived(t *testing.T) {
	engine := &stubEngine{}
	ready := make(chan player.Engine, 1)
	ready <- engine

	shutdownEngine(ready, func() {}, time.Second)

	if !engine.closed {
		t.Error("expected the engine to be closed on the way out")
	}
}

func TestShutdownWaitsForAStartStillInFlight(t *testing.T) {
	engine := &stubEngine{}
	ready := make(chan player.Engine, 1)

	go func() {
		time.Sleep(150 * time.Millisecond)
		ready <- engine
	}()

	done := make(chan struct{})
	go func() {
		shutdownEngine(ready, func() {}, 5*time.Second)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("returned before the engine arrived, which would orphan it")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown never returned")
	}

	if !engine.closed {
		t.Error("expected the late engine to be closed")
	}
}

// A start that ignores its cancellation must not hang the quit.
func TestShutdownGivesUpOnAStartThatNeverArrives(t *testing.T) {
	ready := make(chan player.Engine, 1)

	started := time.Now()
	shutdownEngine(ready, func() {}, 60*time.Millisecond)

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("shutdown waited %s on a start that never arrived", elapsed)
	}
}

// The interface normally only runs against a real terminal, so nothing else
// in the suite proves that the event loop, the view and the teardown actually
// work together. This runs the real program with a scripted keyboard.
func TestProgramRunsHeadless(t *testing.T) {
	model := testModel(80, 24)
	model.engine = &stubEngine{status: player.Status{Duration: 200, Volume: 80}}

	// "q" quits, so this must return rather than block.
	program := tea.NewProgram(model,
		tea.WithInput(strings.NewReader("q")),
		tea.WithOutput(io.Discard),
		tea.WithAltScreen(),
	)

	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program run: %v", err)
		}
	case <-time.After(10 * time.Second):
		program.Kill()
		t.Fatal("the interface did not exit when asked to quit")
	}
}

// AM_DUMP=1 go test ./internal/tui -run Dump -v shows the real frame, which is
// the only way to eyeball a full-screen interface without a terminal.
func TestDumpFrame(t *testing.T) {
	if os.Getenv("AM_DUMP") == "" {
		t.Skip("set AM_DUMP=1 to print the rendered frames")
	}

	for _, size := range []struct{ width, height int }{{100, 30}, {80, 26}, {64, 16}, {52, 20}} {
		model := testModel(size.width, size.height)
		fmt.Printf("\n=== %dx%d ===\n%s\n", size.width, size.height, model.View())

		model.showQueue = true
		fmt.Printf("\n=== %dx%d with the queue open ===\n%s\n", size.width, size.height, model.View())

		model.showQueue = false
		model.showHelp = true
		fmt.Printf("\n=== %dx%d with help open ===\n%s\n", size.width, size.height, model.View())

		model = searchingModel(size.width, size.height)
		model.search.queued[1] = true
		fmt.Printf("\n=== %dx%d with search open ===\n%s\n", size.width, size.height, model.View())

		model.search.editing = true
		fmt.Printf("\n=== %dx%d while typing a search ===\n%s\n", size.width, size.height, model.View())
	}
}
