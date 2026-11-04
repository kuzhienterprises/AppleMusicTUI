package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/artwork"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/lyrics"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/visualizer"
)

const (
	// spectrumInterval paces the bars. 40 ms is about 25 frames a second:
	// smooth for a spectrum, cheap for a terminal.
	spectrumInterval = 40 * time.Millisecond

	// statusInterval paces the line that says what the player is doing. It
	// needs a clock of its own, because the spectrum's tick stops when the
	// spectrum is hidden and the pulse has to keep moving while the engine
	// boots — which is exactly when there is nothing else to look at.
	statusInterval = 300 * time.Millisecond

	// Frame geometry. contentWidth is the space inside the port, so the
	// finished frame is contentWidth + 4 (padding) + 2 (border) wide. The
	// floor is the width at which the progress bar and header still fit, not
	// the width at which they look nice: the frame must never be wider than
	// the window it is drawn in.
	frameMargin     = 4
	minContentWidth = 26
	maxContentWidth = 72

	// Body geometry. Half blocks put two pixels in every cell, so a cover
	// this shape comes out square.
	artColumns = 20
	artRows    = 10
	artGap     = 2

	// spectrumRows is how many text rows the spectrum occupies. Two rows of
	// eighth blocks give sixteen steps, which reads as music rather than as
	// noise.
	spectrumRows = 2

	// Parts of the layout are dropped rather than squashed below these
	// content widths.
	artMinContent      = 54
	spectrumMinContent = 40

	// framePadding is the blank space the frame leaves on either side of its
	// content, in cells.
	framePadding = 2

	// radioAttempts is how many times a queue that cannot be extended is
	// asked to extend itself before the music is allowed to stop. Each
	// attempt is a model call, so a failure that repeats is not worth
	// paying for twice more than this.
	radioAttempts = 3

	// queueWindow is how many tracks the queue panel shows around the one
	// playing.
	queueWindow = 7

	// shutdownGrace is how long a quit waits for an engine that was still
	// starting to give up, so the browser it launched is not left running.
	shutdownGrace = 5 * time.Second

	// frameChrome is what the border and padding add to a card, in rows.
	frameChrome = 4
)

// Starter brings up a playback engine. The interface calls it once it is on
// screen, so the wait for a browser to boot is shown rather than sat through.
type Starter func(ctx context.Context) (player.Engine, error)

// pulseFrames is a point growing into a circle and settling back. Four steps
// read as growth rather than as a blink, and ending where it started leaves the
// cycle without a seam. Each one is a single cell wide, because the line it
// sits on is measured rather than counted.
var pulseFrames = []string{"·", "•", "●", "•"}

// Searcher looks up catalogue tracks by name. It is what the search panel
// calls, and it is passed in rather than reached for so the interface stays
// ignorant of how the catalogue is reached.
type Searcher func(ctx context.Context, query string) ([]player.Track, error)

// Lyricist looks up the timed lyrics for a track. It is passed in for the same
// reason the searcher is: the interface should not know where the words come
// from, or that some tracks have none.
type Lyricist func(ctx context.Context, track player.Track) (*lyrics.Doc, error)

// Extend asks for more tracks when the queue is nearly out. It is given what
// is already queued, and is expected to leave those out of the answer.
type Extend func(ctx context.Context, queued []player.Track) ([]player.Track, error)

// Options configure the player interface.
type Options struct {
	// Start brings the engine up once the interface is on screen.
	Start Starter
	// Search finds tracks to add to the queue. Without it the search panel
	// says so instead of offering a field that goes nowhere.
	Search Searcher
	// Lyrics finds the words for a track. Without it the panel says so
	// rather than opening empty.
	Lyrics Lyricist
	// Extend keeps the queue going once the last track is playing. Without
	// it the queue ends where it ends, which is what most commands want.
	Extend Extend
	// Tracks is the queue to begin with.
	Tracks []player.Track
	// StopAfter quits once playback has run this long.
	StopAfter time.Duration
}

type (
	statusMsg    player.Status
	statusErrMsg struct{ err error }
	stopTimerMsg struct{}
	bandsMsg     []float64

	// engineReadyMsg delivers a started engine to the interface.
	engineReadyMsg struct{ engine player.Engine }
	engineErrMsg   struct{ err error }

	// searchMsg carries catalogue results back from the search goroutine.
	searchMsg struct {
		query   string
		results []player.Track
		err     error
	}
	// enqueuedMsg confirms that a track reached the engine's queue.
	enqueuedMsg struct{ track player.Track }
	// enqueueErrMsg reports a track the engine would not take.
	enqueueErrMsg struct{ err error }

	// artMsg carries a decoded cover back from the download goroutine.
	artMsg struct {
		url string
		img image.Image
		err error
	}

	// lyricsMsg carries a track's words back from the fetch goroutine.
	lyricsMsg struct {
		trackID string
		doc     *lyrics.Doc
		err     error
	}

	// extendedMsg carries the next batch of the queue back from the
	// recommendation goroutine. The engine already has them by then.
	extendedMsg struct {
		tracks []player.Track
		err    error
	}

	// pulseMsg advances the animation on the status line.
	pulseMsg struct{}
)

// searchState is the add-to-queue panel. It is what makes the player usable on
// its own: without it, finding a song means quitting, and quitting ends the
// playback you were listening to.
type searchState struct {
	open    bool
	editing bool
	query   string
	results []player.Track
	// queued runs alongside results and marks what has already been added, so
	// several tracks can be picked in a row.
	queued    []bool
	choice    int
	searching bool
	adding    bool
	err       error
}

type playerModel struct {
	engine  player.Engine
	tracks  []player.Track
	status  player.Status
	monitor visualizer.Monitor
	bands   []float64

	// searcher is the catalogue lookup behind the search panel.
	searcher Searcher
	search   searchState

	// lyricist fetches the words behind the lyrics panel.
	lyricist Lyricist

	// extender asks for more tracks when the queue runs low. It is nil when
	// the queue is meant to end, which is most of the time.
	extender Extend
	// extending is true while a batch is on its way: the queue must not ask
	// twice for the same thing.
	extending bool
	// extendFails counts consecutive failures. A queue that cannot be
	// extended ends rather than asking again forever.
	extendFails int
	// radioErr is the last failure, shown instead of ending in silence.
	radioErr error

	// Lyrics panel state. The document is kept with the id of the track it
	// belongs to, so a queue that moved on never shows the previous song's
	// words under the current title.
	lyrics        *lyrics.Doc
	lyricsTrack   string
	lyricsLoading bool
	lyricsErr     error
	showLyrics    bool

	// start brings the engine up. It is nil once the engine is running, and
	// nil from the outset when one was handed over ready-made.
	start func() tea.Cmd

	// Cover state. art holds the rendered half-block rows, artURL is the
	// artwork already on screen (so a redraw never refetches), and tint is the
	// colour taken from the cover.
	art     []string
	artURL  string
	tint    string
	palette []lipgloss.Style

	// pulse counts status ticks. It is what makes the pulse grow and the dots
	// move, and it is deliberately a plain counter: the animation is the only
	// thing it drives, so nothing has to be unwound when it is not drawn.
	pulse int

	started      bool
	showQueue    bool
	showSpectrum bool
	showHelp     bool
	stopAfter    time.Duration
	width        int
	height       int
	err          error
}

func (m playerModel) Init() tea.Cmd {
	var commands []tea.Cmd

	switch {
	case m.start != nil:
		commands = append(commands, m.start())
	case m.engine != nil:
		commands = append(commands, refresh(m.engine))
	}

	if m.stopAfter > 0 {
		commands = append(commands, tea.Tick(m.stopAfter, func(time.Time) tea.Msg {
			return stopTimerMsg{}
		}))
	}
	if m.monitor != nil && m.showSpectrum {
		commands = append(commands, sampleSpectrum(m.monitor))
	}
	commands = append(commands, pulseTick())
	if command := m.artCommand(); command != nil {
		commands = append(commands, command)
	}

	return tea.Batch(commands...)
}

// pulseTick advances the status line's animation. It always re-arms: the pulse
// has to keep moving while the engine boots and the dots while a track plays,
// and a tick that costs one integer is not worth the bookkeeping of stopping
// and starting it around those states.
func pulseTick() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg {
		return pulseMsg{}
	})
}

// refresh polls the engine shortly from now. Polling gives both backends the
// same shape: the browser has no event stream to subscribe to, and mpv needs
// nothing more than this.
func refresh(engine player.Engine) tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		status, err := engine.Status(ctx)
		if err != nil {
			return statusErrMsg{err}
		}
		return statusMsg(status)
	})
}

// sampleSpectrum reads the newest frame from the monitor. This is a plain
// memory read, so it can run far faster than the engine poll above.
func sampleSpectrum(monitor visualizer.Monitor) tea.Cmd {
	return tea.Tick(spectrumInterval, func(time.Time) tea.Msg {
		return bandsMsg(monitor.Bands())
	})
}

// control runs an engine action off the update loop. While the engine is still
// coming up there is nothing to control, so the key is dropped rather than
// queued: by the time the engine existed, the moment would have passed.
func (m playerModel) control(action func(context.Context, player.Engine) error) tea.Cmd {
	engine := m.engine
	if engine == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := action(ctx, engine); err != nil {
			return statusErrMsg{err}
		}
		return nil
	}
}

// skip is control for the transport keys, with one addition: a queue of a
// single track has nowhere to skip to, so the engine is never asked. That
// matters because the engines disagree about what "next" means at the end of a
// one-item queue — mpv finishes the playlist and the player quits, which reads
// as a crash rather than as a skip.
func (m playerModel) skip(action func(context.Context, player.Engine) error) tea.Cmd {
	if len(m.tracks) <= 1 {
		return nil
	}
	return m.control(action)
}

// searchCommand runs a catalogue lookup off the update loop. Searching takes a
// network round trip, and the spectrum must keep moving while it happens.
func (m playerModel) searchCommand(query string) tea.Cmd {
	searcher := m.searcher
	if searcher == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		tracks, err := searcher(ctx, query)
		return searchMsg{query: query, results: tracks, err: err}
	}
}

// enqueueCommand adds one track to the engine's queue. The track is not added
// to the local list until the engine confirms it, so the queue on screen never
// claims something that is not really there.
func (m playerModel) enqueueCommand(track player.Track) tea.Cmd {
	engine := m.engine
	if engine == nil {
		// There is no queue to add to yet.
		return nil
	}

	return func() tea.Msg {
		// The browser route round-trips through Apple's own queue, which can
		// be slow when it has to resolve ids first.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		if err := engine.Enqueue(ctx, []player.Track{track}); err != nil {
			return enqueueErrMsg{err: err}
		}
		return enqueuedMsg{track: track}
	}
}

// radioCommand asks for the next batch of tracks and adds it to the queue.
//
// The batch is enqueued here rather than in the callback so the interface
// keeps one place where the queue grows, and the play call afterwards covers
// the case that matters most: the last track has already ended by the time the
// answer arrives, and MusicKit does not resume on its own once it has stopped
// at the end of a queue.
func (m playerModel) radioCommand() tea.Cmd {
	engine := m.engine
	extend := m.extender
	if engine == nil || extend == nil {
		return nil
	}

	queued := append([]player.Track(nil), m.tracks...)
	firstNew := len(m.tracks)
	return func() tea.Msg {
		// A model call plus a search per track: slower than the other
		// background work here, but it runs while the last track is playing.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		tracks, err := extend(ctx, queued)
		if err != nil {
			return extendedMsg{err: err}
		}
		if len(tracks) == 0 {
			return extendedMsg{err: errors.New("no more tracks came back")}
		}
		if err := engine.Enqueue(ctx, tracks); err != nil {
			return extendedMsg{err: err}
		}

		if status, err := engine.Status(ctx); err == nil && status.Finished {
			if err := engine.Play(ctx, firstNew); err != nil {
				return extendedMsg{tracks: tracks, err: err}
			}
		}
		return extendedMsg{tracks: tracks}
	}
}

// shouldExtend reports whether the queue is close enough to its end to ask for
// more: the last track of the queue is the one playing.
func (m playerModel) shouldExtend() bool {
	if m.extender == nil || m.extending || m.extendFails >= radioAttempts {
		return false
	}
	if len(m.tracks) == 0 {
		return false
	}
	return m.status.Index >= len(m.tracks)-1
}

// lyricsCommand fetches the words for the track on screen. It runs off the
// update loop for the same reason the cover does: the catalogue is a network
// away, and the progress bar must keep moving while it answers.
func (m playerModel) lyricsCommand() tea.Cmd {
	track := m.currentTrack()
	if m.lyricist == nil || track.ID == "" {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		doc, err := m.lyricist(ctx, track)
		return lyricsMsg{trackID: track.ID, doc: doc, err: err}
	}
}

// artCommand downloads the current track's cover unless it is already on
// screen. The download happens off the update loop, so a slow CDN cannot stall
// the interface.
func (m playerModel) artCommand() tea.Cmd {
	track := m.currentTrack()
	if track.Artwork == "" || track.Artwork == m.artURL {
		return nil
	}

	url := track.Artwork
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		cover, err := artwork.Load(ctx, url, artColumns)
		return artMsg{url: url, img: cover, err: err}
	}
}

func (m playerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// A window that grew or shrank leaves the old frame's borders behind
		// if the renderer only repaints the rows the new card happens to use.
		// Clear once on resize so the first frame at the new size is clean.
		return m, tea.ClearScreen

	case stopTimerMsg:
		return m, tea.Quit

	case engineReadyMsg:
		m.engine = msg.engine
		m.start = nil
		return m, refresh(m.engine)

	case engineErrMsg:
		m.err = msg.err
		return m, tea.Quit

	case searchMsg:
		// A result set that arrives after the query moved on is stale.
		if msg.query != strings.TrimSpace(m.search.query) {
			return m, nil
		}
		m.search.searching = false
		m.search.editing = false
		m.search.err = msg.err
		m.search.results = msg.results
		m.search.queued = make([]bool, len(msg.results))
		m.search.choice = 0
		return m, nil

	case enqueuedMsg:
		m.search.adding = false
		m.tracks = append(m.tracks, msg.track)
		for i, track := range m.search.results {
			if track.ID == msg.track.ID && i < len(m.search.queued) {
				m.search.queued[i] = true
				break
			}
		}
		return m, nil

	case enqueueErrMsg:
		m.search.adding = false
		m.search.err = msg.err
		return m, nil

	case statusErrMsg:
		m.err = msg.err
		return m, tea.Quit

	case statusMsg:
		m.status = player.Status(msg)
		if m.status.Duration > 0 || m.status.Position > 0 || !m.status.Paused {
			m.started = true
		}
		if m.started && m.status.Finished {
			// A queue that can still be extended is not over: wait for the
			// next batch instead of quitting under it.
			if !m.shouldExtend() && !m.extending {
				return m, tea.Quit
			}
		}

		// The cover on screen belongs to whichever track was playing a moment
		// ago; drop it so the slot empties instead of showing the wrong album.
		if m.currentTrack().Artwork != m.artURL {
			m.art = nil
		}

		// The words follow the music. A track that changed while the panel is
		// open needs its own lyrics, and one request per track is enough: a
		// track without lyrics must not be asked for again on every poll.
		commands := []tea.Cmd{refresh(m.engine), m.artCommand()}
		if m.showLyrics && m.lyricist != nil && !m.lyricsLoading {
			if track := m.currentTrack(); track.ID != "" && track.ID != m.lyricsTrack {
				m.lyricsLoading = true
				m.lyricsErr = nil
				commands = append(commands, m.lyricsCommand())
			}
		}
		// Ask for more as the last track of the queue starts, so the batch
		// is ready before the music stops rather than after it.
		if m.shouldExtend() {
			m.extending = true
			commands = append(commands, m.radioCommand())
		}
		return m, tea.Batch(commands...)

	case extendedMsg:
		m.extending = false
		if msg.err != nil {
			m.extendFails++
			m.radioErr = msg.err
			return m, nil
		}
		m.extendFails = 0
		m.radioErr = nil
		m.tracks = append(m.tracks, msg.tracks...)
		return m, nil

	case lyricsMsg:
		// Words for a track that has since moved on are stale, and showing
		// them under the new title would be worse than showing nothing.
		if msg.trackID != m.currentTrack().ID {
			return m, nil
		}
		m.lyricsLoading = false
		m.lyricsTrack = msg.trackID
		m.lyricsErr = msg.err
		m.lyrics = msg.doc
		return m, nil

	case bandsMsg:
		m.bands = msg
		if m.monitor != nil && m.showSpectrum {
			return m, sampleSpectrum(m.monitor)
		}
		return m, nil

	case pulseMsg:
		m.pulse++
		return m, pulseTick()

	case artMsg:
		// A cover that arrives late is only useful if it is still the track on
		// screen.
		if msg.url != m.currentTrack().Artwork {
			return m, nil
		}

		m.artURL = msg.url
		if msg.err != nil || msg.img == nil {
			return m, nil
		}

		m.art = renderArt(msg.img, artColumns, artRows)
		if tint := artwork.Dominant(msg.img); tint != "" {
			m.tint = tint
			m.palette = buildPalette(gradientPink, tint, paletteSteps)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m playerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The search panel owns the keyboard while it is open. Half of its keys
	// are letters the transport also answers to, and typing "n" into a search
	// box must not skip a track.
	if m.search.open {
		return m.handleSearchKey(msg)
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		// Esc closes an overlay before it quits, so one stray press does not
		// throw away the session.
		if m.showHelp || m.showQueue {
			m.showHelp, m.showQueue = false, false
			return m, nil
		}
		return m, tea.Quit

	case "q":
		return m, tea.Quit

	case "?":
		m.showHelp = !m.showHelp
		return m, nil

	case "/":
		m.showHelp, m.showQueue = false, false
		m.search = searchState{open: true, editing: true}
		if m.searcher == nil {
			m.search.err = fmt.Errorf("catalogue search is not available here")
		}
		return m, nil

	case " ":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.TogglePause(ctx)
		})

	case "right", "l":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.Seek(ctx, 5)
		})
	case "left", "h":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.Seek(ctx, -5)
		})
	case "shift+right":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.Seek(ctx, 30)
		})
	case "shift+left":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.Seek(ctx, -30)
		})

	case "up":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.AdjustVolume(ctx, 5)
		})
	case "down":
		return m, m.control(func(ctx context.Context, engine player.Engine) error {
			return engine.AdjustVolume(ctx, -5)
		})

	// A queue of one is the normal state after picking a single search
	// result. Skipping then has nowhere to go, and the engines disagree
	// about what it means: mpv ends the playlist and the player exits,
	// which reads as a crash. The up-next line already says where to add
	// more, so leave the engine alone.
	case "n":
		return m, m.skip(func(ctx context.Context, engine player.Engine) error {
			return engine.Next(ctx)
		})
	case "p":
		return m, m.skip(func(ctx context.Context, engine player.Engine) error {
			return engine.Previous(ctx)
		})

	case "a":
		m.showQueue = !m.showQueue
		m.showHelp = false

	case "y":
		m.showLyrics = !m.showLyrics
		m.showHelp = false
		// The lyrics panel changes the card's height, and on some terminals
		// the rows the old panel occupied survive the repaint. Clear once so
		// opening or closing the words never leaves half a frame behind.
		clear := tea.ClearScreen
		if !m.showLyrics {
			return m, clear
		}
		if m.lyricist == nil {
			m.lyricsErr = fmt.Errorf("lyrics are not available here")
			return m, clear
		}
		// Opening the panel is also how a failed lookup is retried: the track
		// has not changed, but the reason for the empty panel may have.
		if track := m.currentTrack(); track.ID != m.lyricsTrack || m.lyricsErr != nil {
			m.lyricsLoading = true
			m.lyricsErr = nil
			return m, tea.Batch(clear, m.lyricsCommand())
		}
		return m, clear

	case "v":
		m.showSpectrum = !m.showSpectrum
		if m.showSpectrum && m.monitor != nil {
			return m, sampleSpectrum(m.monitor)
		}
	}
	return m, nil
}

// handleSearchKey drives the search panel, which has two phases: typing a
// query, and choosing from what came back. Enter moves between them.
func (m playerModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		// The panel closes on the first press, so the second one still quits.
		m.search = searchState{}
		return m, nil

	case "enter":
		return m.confirmSearch()

	case "backspace":
		if m.search.editing {
			m.search.query = trimLastRune(m.search.query)
		}
		return m, nil

	case "up":
		if !m.search.editing {
			m.search.choice = maxInt(m.search.choice-1, 0)
		}
		return m, nil

	case "down":
		if !m.search.editing {
			m.search.choice = minInt(m.search.choice+1, maxInt(len(m.search.results)-1, 0))
		}
		return m, nil

	case "home":
		if !m.search.editing {
			m.search.choice = 0
		}
		return m, nil

	case "end":
		if !m.search.editing {
			m.search.choice = maxInt(len(m.search.results)-1, 0)
		}
		return m, nil

	case "pgup", "pgdown", "tab":
		return m, nil
	}

	// Anything else is text. Typing while a result list is on screen means a
	// new search, which is what the eye already expects from a search box.
	if msg.Type == tea.KeyRunes || msg.String() == " " {
		m.search.editing = true
		if msg.Type == tea.KeyRunes {
			m.search.query += string(msg.Runes)
		} else {
			m.search.query += " "
		}
		return m, nil
	}
	return m, nil
}

// confirmSearch is Enter in the search panel: run the query if the query is
// being typed, and add the highlighted result if it is not.
func (m playerModel) confirmSearch() (tea.Model, tea.Cmd) {
	if m.search.editing {
		query := strings.TrimSpace(m.search.query)
		if query == "" || m.searcher == nil || m.search.searching {
			return m, nil
		}

		m.search.searching = true
		m.search.err = nil
		return m, m.searchCommand(query)
	}

	if m.search.adding || len(m.search.results) == 0 {
		return m, nil
	}

	choice := clampInt(m.search.choice, 0, len(m.search.results)-1)
	if choice < len(m.search.queued) && m.search.queued[choice] {
		return m, nil
	}

	command := m.enqueueCommand(m.search.results[choice])
	if command == nil {
		return m, nil
	}
	m.search.adding = true
	m.search.err = nil
	return m, command
}

// trimLastRune drops the final character, one rune at a time so a multi-byte
// character is not left half erased.
func trimLastRune(text string) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return text
	}
	return string(runes[:len(runes)-1])
}

// layout describes what a card draws, so a window that is too small for the
// full thing can give parts of it up in a sensible order.
type layout struct {
	// separators are the blank lines between blocks.
	separators bool
	// cover is the album art, which costs ten rows.
	cover bool
	// footer is the hint strip, the only purely decorative row.
	footer bool
	// state is the line that reports what the transport is doing. It is worth
	// a row, but not a row that the keys would have been using.
	state bool
}

// layouts are tried in order until one fits. A frame taller than the terminal
// scrolls the alternate screen and drags its own top border out of view, so
// something has to give, and it should be the spacing before the cover, the
// cover before the transport report, and all of it before the controls.
var layouts = []layout{
	{separators: true, cover: true, footer: true, state: true},
	{separators: false, cover: true, footer: true, state: true},
	{separators: false, cover: false, footer: true, state: true},
	{separators: false, cover: false, footer: true, state: false},
	{separators: false, cover: false, footer: false, state: false},
}

func (m playerModel) View() string {
	if m.err != nil {
		return m.centre("\n" + errorStyle.Render(m.err.Error()) + "\n")
	}
	if len(m.tracks) == 0 {
		return ""
	}

	width := m.width
	if width <= 0 {
		width = 80
	}

	// The window is not always wider than the frame wants to be, so the
	// content shrinks with it until it hits the floor.
	content := clampInt(width-frameMargin*2-6, minContentWidth, maxContentWidth)

	for _, l := range layouts {
		card := m.card(content, l)
		if m.height <= 0 || fitsHeight(card, m.height) {
			return m.centre(card)
		}
	}

	// Even the leanest card is too tall. Draw it anyway, but never let the
	// string itself scroll the alternate screen: a frame taller than the
	// window leaves the previous one above it and turns every tick into a
	// duplicate.
	card := m.card(content, layouts[len(layouts)-1])
	if m.height > 0 {
		card = trimToHeight(card, m.height)
	}
	return m.centre(card)
}

// trimToHeight cuts a rendered block down to the terminal height.
func trimToHeight(block string, height int) string {
	if height <= 0 {
		return block
	}
	lines := strings.Split(block, "\n")
	if len(lines) <= height {
		return block
	}
	return strings.Join(lines[:height], "\n")
}

// fitsHeight reports whether a rendered block leaves room inside a window of
// the given height.
func fitsHeight(block string, height int) bool {
	return strings.Count(block, "\n")+1 <= height
}

// centre drops the card in the middle of the window, which is what keeps the
// player from hugging the top-left corner.
//
// The padding is worked out here rather than by lipgloss.Place for the same
// reason the frame is drawn here: lipgloss pads to the window width by its own
// measure, and a line that it thinks is 120 cells wide can be 127 by the
// terminal's, which wraps and leaves the previous frame behind.
func (m playerModel) centre(card string) string {
	if m.width <= 0 || m.height <= 0 {
		return card
	}
	return placeIn(card, m.width, m.height)
}

// placeIn centres a block in a window, measuring with cellWidth.
//
// The padding is worked out here rather than by lipgloss.Place because
// lipgloss pads to the window width by its own measure, and a line it thinks
// is 120 cells wide can be 127 by the terminal's: the terminal wraps it and
// leaves the previous frame behind on every repaint.
func placeIn(block string, width, height int) string {
	lines := strings.Split(block, "\n")
	blockWidth := 0
	for _, line := range lines {
		blockWidth = maxInt(blockWidth, cellWidth(line))
	}

	left := maxInt((width-blockWidth)/2, 0)
	top := maxInt((height-len(lines))/2, 0)

	out := make([]string, 0, height)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	padding := strings.Repeat(" ", left)
	for _, line := range lines {
		out = append(out, padRight(padding+line, width))
	}
	// The area below the card is cleared the same way the area beside it is:
	// a shorter frame that only pads sideways leaves the previous frame's
	// bottom rows on screen.
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out[:minInt(len(out), height)], "\n")
}

func (m playerModel) card(content int, l layout) string {
	// The key list is a mode of its own. The hint footer would only repeat
	// what is already on screen, so it is left off, and the progress row
	// follows it when there is not enough room for both.
	if m.showHelp {
		return m.helpCard(content, l)
	}

	var lines []string
	gap := func() {
		if l.separators {
			lines = append(lines, "")
		}
	}

	lines = append(lines, m.headerLine(content))
	gap()
	lines = append(lines, m.bodyLines(content, l)...)
	gap()
	lines = append(lines, m.progressLine(content))

	if m.showQueue {
		gap()
		lines = append(lines, m.queueLines(content)...)
	} else {
		gap()
		lines = append(lines, m.upNextLine(content))
	}
	gap()

	if notice := m.noticeLine(content); notice != "" {
		lines = append(lines, notice)
		gap()
	}

	if l.state {
		if state := m.stateLine(content); state != "" {
			lines = append(lines, state)
			gap()
		}
	}

	if l.footer {
		lines = append(lines, m.footerLines(content)...)
	}

	return m.frame(lines, content)
}

// helpCard draws the key list. The budget is worked out against the thinnest
// version of the list — no separators, no progress row — so the tiers in
// layouts get the last word on what has to go, and the list is only cut back
// once nothing else can be given up.
func (m playerModel) helpCard(content int, l layout) string {
	lines := []string{m.headerLine(content)}

	budget := len(helpHints) + 1
	if m.height > 0 {
		budget = clampInt(m.height-frameChrome-1, 0, len(helpHints)+1)
	}

	if l.separators {
		lines = append(lines, "")
	}
	lines = append(lines, m.helpLines(content, budget)...)
	if l.separators {
		lines = append(lines, "", m.progressLine(content))
	}
	return m.frame(lines, content)
}

// frame draws the border and padding around prepared content lines.
//
// The border is drawn here rather than handed to lipgloss because lipgloss
// measures text with runewidth, which puts a Tamil or Telugu lyric about 15%
// narrower than the terminal draws it. The border would be placed inside the
// words, and the terminal, laying the line out its own way, would wrap it and
// leave the previous frame behind on every repaint. One measure, cellWidth(),
// builds the whole card.
func (m playerModel) frame(lines []string, content int) string {
	border := lipgloss.RoundedBorder()
	inner := content + framePadding*2
	blank := frameEdge(border.Left) + strings.Repeat(" ", inner) + frameEdge(border.Right)

	out := make([]string, 0, len(lines)+4)
	out = append(out, frameEdge(border.TopLeft+strings.Repeat(border.Top, inner)+border.TopRight))
	out = append(out, blank)
	for _, line := range lines {
		out = append(out, frameEdge(border.Left+"  ")+fitLine(line, content)+frameEdge("  "+border.Right))
	}
	out = append(out, blank)
	out = append(out, frameEdge(border.BottomLeft+strings.Repeat(border.Bottom, inner)+border.BottomRight))
	return strings.Join(out, "\n")
}

// frameEdge colours the border, which is the one part of the card that is
// always plain text.
func frameEdge(text string) string {
	return frameEdgeStyle.Render(text)
}

// fitLine pads or cuts a line to exactly width cells, measured by cellWidth(), so
// every row of the card ends at the same column.
func fitLine(line string, width int) string {
	if slack := width - cellWidth(line); slack > 0 {
		return line + strings.Repeat(" ", slack)
	}
	if cellWidth(line) == width {
		return line
	}

	// Too wide. ansi.Truncate measures with the table that under-counts these
	// lines, so its answer is checked again rather than trusted; if it still
	// does not fit, the text is cut by grapheme cluster and loses its styling,
	// which is better than a border that moves.
	cut := ansi.Truncate(line, width, "")
	if fitted := cellWidth(cut); fitted <= width {
		return cut + strings.Repeat(" ", width-fitted)
	}
	chunks := splitByWidth(ansi.Strip(line), width)
	return chunks[0] + strings.Repeat(" ", width-displayWidth(chunks[0]))
}

// headerLine is the status strip: the wordmark on the left and the things
// worth glancing at on the right.
func (m playerModel) headerLine(width int) string {
	volume := m.volumeWidget()
	counter := helpStyle.Render(m.counter())
	mark := headerStyle.Render("♫ Apple Music")

	// Narrow windows give things up in order of how little they are missed:
	// first the track counter, then the long wordmark, which shortens to the
	// note. The volume is the last thing to go, because it is the one piece
	// of state here that is not visible anywhere else.
	for _, arrangement := range [][2]string{
		{mark, counter},
		{mark, ""},
		{headerStyle.Render("♫"), counter},
		{headerStyle.Render("♫"), ""},
	} {
		if line, ok := spread(arrangement[0], arrangement[1], volume, width); ok {
			return line
		}
	}
	return clip(mark, width)
}

// spread puts left and right at opposite ends of a line, with an optional
// middle item tucked against the right if there is room. It reports whether
// the arrangement fits at all.
func spread(left, middle, right string, width int) (string, bool) {
	leftWidth, rightWidth := cellWidth(left), cellWidth(right)
	if leftWidth+1+rightWidth > width {
		return "", false
	}

	if middle != "" && leftWidth+1+cellWidth(middle)+3+rightWidth <= width {
		trailing := middle + strings.Repeat(" ", 3) + right
		return left + strings.Repeat(" ", width-leftWidth-cellWidth(trailing)) + trailing, true
	}
	return left + strings.Repeat(" ", width-leftWidth-rightWidth) + right, true
}

func (m playerModel) counter() string {
	if len(m.tracks) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", m.index()+1, len(m.tracks))
}

// volumeWidget is the header's mini volume bar: a half disc, the filled part,
// a knob, whatever is left, and the percentage.
func (m playerModel) volumeWidget() string {
	const cells = 8

	level := clampInt(int(m.status.Volume+0.5), 0, 100)
	filled := clampInt(level*cells/100, 0, cells)

	return dimStyle.Render("◖") +
		m.accentStyle().Render(strings.Repeat("━", filled)) +
		m.accentStyle().Bold(true).Render("○") +
		dimStyle.Render(strings.Repeat("─", cells-filled)) +
		helpStyle.Render(fmt.Sprintf(" %3d%%", level))
}

// bodyLines is the two-column middle of the card: cover on the left, and the
// track details with the spectrum on the right.
func (m playerModel) bodyLines(content int, l layout) []string {
	column := m.bodyColumn(content, l.cover, m.bodyRows(l))

	if !l.cover || !m.hasArt(content) {
		return column
	}

	art := m.art
	if len(art) == 0 {
		art = artPlaceholder(artColumns, artRows)
	}

	// Centre the text against the height of the cover so the columns read as
	// one block rather than as two things that happen to be side by side.
	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		strings.Join(art, "\n"),
		strings.Repeat(" ", artGap),
		strings.Join(centreVertically(column, artRows), "\n"),
	)
	return strings.Split(body, "\n")
}

// bodyColumn is the right-hand column: the track details normally, and the
// search panel while one is open. Swapping it in place of the details is what
// keeps the card the same size, so opening the search never shunts the frame
// sideways.
func (m playerModel) bodyColumn(content int, cover bool, rows int) []string {
	width := m.textWidth(content, cover)
	if m.search.open {
		return m.searchLines(width, rows)
	}
	if m.showLyrics {
		return m.lyricsLines(width, rows)
	}
	return m.metaLines(width)
}

// bodyRows is the height the body panel is drawn against. The cover's height
// is the natural target, but a short window wins: the panel is worth less than
// the frame staying on screen, and a list that runs off the bottom of the
// terminal drags the whole card with it. The layout matters here because the
// separator, state and footer rows are part of the same budget.
func (m playerModel) bodyRows(l layout) int {
	rows := artRows
	if m.height > 0 {
		reserved := 1 // header
		reserved += 1 // progress
		reserved += 1 // up next
		if l.state {
			reserved++
		}
		if l.footer {
			reserved += 2
		}
		if l.separators {
			reserved += 4
		}
		rows = minInt(rows, maxInt(m.height-frameChrome-reserved, 1))
	}
	return rows
}

// searchLines is the search panel. What is playing stays at the top, because
// searching is something done while listening rather than instead of it.
func (m playerModel) searchLines(width, rows int) []string {
	lines := []string{
		dimStyle.Render("♪") + " " + valueStyle.Render(clip(m.currentTrack().Label(), maxInt(width-2, 1))),
		"",
		m.queryLine(width),
	}

	// The error takes the line under the query rather than replacing the
	// results: a track that failed to add is worth knowing about, but it is no
	// reason to lose the list you were working through.
	if m.search.err != nil {
		lines = append(lines, errorStyle.Render(clip(m.search.err.Error(), width)))
	} else {
		lines = append(lines, "")
	}

	lines = append(lines, m.resultLines(width, maxInt(rows-len(lines), 0))...)
	return lines
}

// queryLine is the text field: the prompt, what has been typed, and what the
// panel is doing with it.
func (m playerModel) queryLine(width int) string {
	prompt := cursorStyle.Render("❯") + " "

	query := m.search.query
	if m.search.editing {
		query += "▌"
	}
	query = clip(query, maxInt(width-cellWidth(prompt), 1))

	field := prompt + valueStyle.Render(query)
	used := cellWidth(prompt) + cellWidth(query)

	hint := ""
	switch {
	case m.search.searching:
		hint = helpStyle.Render("searching…")
	case m.search.adding:
		hint = helpStyle.Render("adding…")
	case m.search.editing:
		hint = helpStyle.Render("enter to search")
	case len(m.search.results) == 1:
		hint = helpStyle.Render("1 result")
	case len(m.search.results) > 1:
		hint = helpStyle.Render(itoa(len(m.search.results)) + " results")
	}

	if hint != "" && used+2+cellWidth(hint) <= width {
		return field + strings.Repeat(" ", width-used-cellWidth(hint)) + hint
	}
	return field
}

// resultLines draws the slice of results the column has room for, scrolled so
// the highlighted one is always on screen. Anything left over is counted
// rather than silently dropped, so a short list is never mistaken for the
// whole answer.
func (m playerModel) resultLines(width, budget int) []string {
	if budget <= 0 {
		return nil
	}
	if len(m.search.results) == 0 {
		if !m.search.editing && !m.search.searching && m.search.err == nil {
			return []string{helpStyle.Render("Nothing found")}
		}
		return nil
	}

	total := len(m.search.results)
	visible := budget
	if total > budget {
		// The last row belongs to the count, so the list scrolls instead of
		// growing a tail the arrow keys cannot reach.
		visible = budget - 1
	}
	if visible <= 0 {
		return []string{m.resultCount(total, 0, 0)}
	}

	first := scrollStart(m.search.choice, total, visible)
	last := minInt(first+visible, total)

	lines := make([]string, 0, budget)
	for i := first; i < last; i++ {
		lines = append(lines, m.resultLine(i, m.search.results[i], width))
	}
	if last-first < total {
		lines = append(lines, m.resultCount(total, first, last))
	}
	return lines
}

// scrollStart is the first result to draw: a window of size rows that holds
// the highlight and stays inside the list.
func scrollStart(choice, total, size int) int {
	if size <= 0 || total <= size {
		return 0
	}
	first := choice - size/2
	if first < 0 {
		first = 0
	}
	if first > total-size {
		first = total - size
	}
	return first
}

// resultCount says how much of the list is off screen, and which way.
func (m playerModel) resultCount(total, first, last int) string {
	above := first
	below := total - last

	var text string
	switch {
	case above > 0 && below > 0:
		text = "… " + itoa(above) + " above, " + itoa(below) + " below"
	case above > 0:
		text = "… " + itoa(above) + " above"
	default:
		text = "… " + itoa(below) + " more"
	}
	return helpStyle.Render(text)
}

// resultLine is one result: the selection arrow, a tick for what has already
// been queued, the track, and its album when there is room for it. The album
// earns its place here, because one song title often shows up several times
// over in the results.
func (m playerModel) resultLine(index int, track player.Track, width int) string {
	selected := index == m.search.choice

	mark := "  "
	if selected {
		mark = cursorStyle.Render("▸ ")
	}
	tick := "  "
	if index < len(m.search.queued) && m.search.queued[index] {
		tick = helpStyle.Render("✓ ")
	}

	label := clip(track.Label(), maxInt(width-4, 1))
	style := valueStyle
	if selected {
		style = selectedItem
	}

	line := mark + tick + style.Render(label)
	if slack := width - (4 + cellWidth(label)); slack >= 16 && track.Album != "" {
		album := clip(track.Album, slack-2)
		line += strings.Repeat(" ", slack-cellWidth(album)) + albumStyle.Render(album)
	}
	return line
}

func (m playerModel) hasArt(content int) bool { return content >= artMinContent }

// textWidth is how much room the right column has once the cover has taken its
// share.
func (m playerModel) textWidth(content int, cover bool) int {
	if !cover || !m.hasArt(content) {
		return content
	}
	return content - artColumns - artGap
}

func (m playerModel) metaLines(width int) []string {
	track := m.currentTrack()

	// Folding the credit is for making it fit, not a house style, so a credit
	// that already fits is left exactly as Apple wrote it. "Earth, Wind &
	// Fire" is one band, and splitting it on the commas would say "Earth +2".
	artist := track.Artist
	if cellWidth(artist) > width {
		artist = summarizeArtists(artist)
	}

	lines := []string{
		titleStyle.Render(clip(track.Title, width)),
		artistStyle.Render(clip(artist, width)),
	}
	if track.Album != "" {
		lines = append(lines, albumStyle.Render(clip(track.Album, width)))
	}

	if m.spectrumVisible(width) {
		lines = append(lines, "")
		lines = append(lines, m.spectrumLines(width)...)
	}
	return lines
}

// lyricsLines draws the words for the track on screen, keeping the line being
// sung in the middle of the panel so the eye never has to chase it.
//
// The words take the metadata column's place, so the title and the credit stay
// above them: once the words are moving, that is the only thing still saying
// what is playing. A card too short for both gets the words, because that is
// what the panel was opened for.
func (m playerModel) lyricsLines(width, rows int) []string {
	if rows <= 0 {
		return nil
	}
	// Four rows is the title, the credit, the gap under them and one line of
	// words. Below that the words get the panel to themselves.
	if rows < 4 {
		return m.lyricsBody(width, rows)
	}

	track := m.currentTrack()
	artist := track.Artist
	if cellWidth(artist) > width {
		artist = summarizeArtists(artist)
	}
	lines := []string{
		titleStyle.Render(centreIn(track.Title, width)),
		artistStyle.Render(centreIn(artist, width)),
		"",
	}
	return append(lines, m.lyricsBody(width, rows-len(lines))...)
}

// lyricsBody is the words themselves, or the reason there are none.
func (m playerModel) lyricsBody(width, rows int) []string {
	if rows <= 0 {
		return nil
	}

	if m.lyrics == nil || len(m.lyrics.Lines) == 0 {
		switch {
		case m.lyricist == nil:
			return m.lyricsNote(width, "Lyrics are not available here.")
		case m.lyricsLoading:
			return m.lyricsNote(width, "Looking up the words...")
		case m.lyricsErr != nil && !errors.Is(m.lyricsErr, lyrics.ErrNone):
			// A track with no lyrics is an answer; a failed lookup is not,
			// and saying "no lyrics" for it would be a lie that survives
			// until the panel is reopened.
			return m.lyricsNote(width, "The lyrics could not be loaded.")
		default:
			return m.lyricsNote(width, "No lyrics for this track.")
		}
	}

	// Words without timings cannot be followed, and neither can a timed song
	// that has not started: in both cases nothing is highlighted and the panel
	// starts at the top, which is where the first line is.
	active := -1
	if m.lyrics.Synced {
		active = m.lyrics.LineAt(m.playhead())
	}

	// Wrapping happens before the window is chosen: a line that needs two rows
	// has to be counted as two, or the window lands in the middle of a line.
	var panel []string
	activeRow := 0
	for index, line := range m.lyrics.Lines {
		wrapped := wrapLyric(line.Text, width)
		if index == active {
			activeRow = len(panel)
		}
		for _, part := range wrapped {
			if index == active {
				panel = append(panel, m.accentStyle().Bold(true).Render(centreIn(part, width)))
				continue
			}
			panel = append(panel, helpStyle.Render(centreIn(part, width)))
		}
	}

	// The sung line sits just above the middle, which leaves room for what is
	// coming and keeps the words steady rather than jumping on every update.
	top := clampInt(activeRow-(rows-1)/2, 0, maxInt(len(panel)-rows, 0))
	return panel[top:minInt(top+rows, len(panel))]
}

// lyricsNote is the panel with nothing to show, said plainly.
func (m playerModel) lyricsNote(width int, note string) []string {
	return []string{helpStyle.Render(centreIn(note, width))}
}

// playhead is the engine's position as a duration. The status carries seconds
// because that is what both engines report; lyrics need the finer form.
func (m playerModel) playhead() time.Duration {
	if m.status.Position <= 0 {
		return 0
	}
	return time.Duration(m.status.Position * float64(time.Second))
}

// wrapLyric breaks one lyric line to the width of the column. Lyrics are
// written one line at a time, and the column beside the cover is about fifty
// cells, so most of them have to be broken somewhere.
func wrapLyric(text string, width int) []string {
	width = maxInt(width, 1)

	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		// A Tamil or Telugu lyric can be one long unspaced run. Splitting it
		// by grapheme cluster is what keeps the line inside the column; the
		// old clip() cut the words off at the first few glyphs and let the
		// terminal wrap the overflow into the frame.
		if displayWidth(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			chunks := splitByWidth(word, width)
			lines = append(lines, chunks[:len(chunks)-1]...)
			current = chunks[len(chunks)-1]
			continue
		}
		switch {
		case current == "":
			current = word
		case displayWidth(current)+1+displayWidth(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// splitByWidth cuts an unspaced run into terminal-width chunks without
// breaking a grapheme cluster (a base character plus its combining marks).
func splitByWidth(text string, width int) []string {
	width = maxInt(width, 1)

	var lines []string
	var current strings.Builder
	lineWidth := 0
	state := -1

	for len(text) > 0 {
		cluster, rest, clusterWidth, newState := uniseg.FirstGraphemeClusterInString(text, state)
		if current.Len() > 0 && lineWidth+clusterWidth > width {
			lines = append(lines, current.String())
			current.Reset()
			lineWidth = 0
		}
		current.WriteString(cluster)
		lineWidth += clusterWidth
		text = rest
		state = newState
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// displayWidth measures plain text the way the terminal lays out grapheme
// clusters. lipgloss.Width is right for ANSI-styled ASCII, but Indic lyrics
// combine marks in ways that need the grapheme-aware measurement.
func displayWidth(text string) int {
	return uniseg.StringWidth(text)
}

// cellWidth measures a line the way the terminal lays it out: grapheme
// clusters, with ANSI escapes ignored. It is the measure the whole card is
// built with, and it is deliberately not lipgloss.Width, which counts runes
// with runewidth: a Tamil lyric comes out about 15% narrower there than the
// terminal draws it, so the frame would be placed inside the words and the
// wrapped line would leave the previous frame on screen.
func cellWidth(text string) int {
	return uniseg.StringWidth(ansi.Strip(text))
}

// centreIn pads text to the width of the column. Centring is what makes the
// words read as a block of lyrics rather than as ragged metadata.
func centreIn(text string, width int) string {
	padding := width - displayWidth(text)
	if padding <= 0 {
		return clip(text, width)
	}
	left := padding / 2
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", padding-left)
}

func (m playerModel) spectrumVisible(width int) bool {
	return m.showSpectrum && len(m.bands) > 0 && width >= spectrumMinContent
}

// spectrumLines draws the spectrum over two rows. Each column's level picks a
// step between 0 and spectrumRows*8, and the bottom row draws the low steps
// while the row above stacks on top of them.
func (m playerModel) spectrumLines(width int) []string {
	if width <= 0 {
		return nil
	}

	steps := spectrumRows * len(spectrumBlocks)
	rows := make([]strings.Builder, spectrumRows)

	for column := 0; column < width; column++ {
		level := m.bandAt(column, width)
		filled := clampInt(int(level*float64(steps)+0.5), 0, steps)
		style := m.columnStyle(column, width)

		for row := 0; row < spectrumRows; row++ {
			cell := clampInt(filled-(spectrumRows-1-row)*len(spectrumBlocks), 0, len(spectrumBlocks))
			if cell == 0 {
				rows[row].WriteString(" ")
				continue
			}
			rows[row].WriteString(style.Render(string(spectrumBlocks[cell-1])))
		}
	}

	out := make([]string, spectrumRows)
	for row := range rows {
		out[row] = rows[row].String()
	}
	return out
}

// columnStyle picks the gradient step for a column. The analyser produces far
// fewer bands than there are columns, so the colour ramp is what carries the
// sense of width.
func (m playerModel) columnStyle(column, width int) lipgloss.Style {
	palette := m.palette
	if len(palette) == 0 {
		palette = defaultPalette
	}
	if width <= 0 {
		return palette[0]
	}
	return palette[clampInt(column*len(palette)/width, 0, len(palette)-1)]
}

func (m playerModel) bandAt(column, width int) float64 {
	bands := m.bands
	if len(bands) == 0 || width <= 0 {
		return 0
	}
	if width <= len(bands) {
		return bands[column*len(bands)/width]
	}

	position := float64(column) * float64(len(bands)-1) / float64(width-1)
	low := int(position)
	high := minInt(low+1, len(bands)-1)
	weight := position - float64(low)

	return bands[low]*(1-weight) + bands[high]*weight
}

// progressLine puts the transport control where the eye already is: the state
// at the left, then the elapsed time, the bar, and the total.
func (m playerModel) progressLine(content int) string {
	duration := m.duration(m.currentTrack())

	icon, style := "▶", m.accentStyle()
	switch {
	case m.engine == nil:
		// Nothing to press yet, and a filled play arrow would say otherwise.
		icon, style = "◌", dimStyle
	case m.status.Paused:
		icon = "⏸"
	}

	elapsed := clock(m.status.Position)
	total := clock(duration)

	// icon + 2 + elapsed + 2 + bar + 2 + total
	barWidth := content - cellWidth(icon) - 4 - cellWidth(elapsed) - 2 - cellWidth(total)
	barWidth = maxInt(barWidth, 8)

	return style.Render(icon) + "  " +
		valueStyle.Render(elapsed) + "  " +
		m.bar(m.status.Position, duration, barWidth) + "  " +
		helpStyle.Render(total)
}

// bar is the progress bar: a heavy filled run, a knob at the playhead, and a
// light remainder.
func (m playerModel) bar(position, duration float64, width int) string {
	ratio := 0.0
	if duration > 0 {
		ratio = position / duration
	}
	switch {
	case ratio < 0 || ratio != ratio: // negative or NaN
		ratio = 0
	case ratio > 1:
		ratio = 1
	}

	width = maxInt(width, 2)
	filled := clampInt(int(ratio*float64(width-1)+0.5), 0, width-1)

	return m.accentStyle().Render(strings.Repeat("━", filled)) +
		m.accentStyle().Bold(true).Render("●") +
		dimStyle.Render(strings.Repeat("─", width-filled-1))
}

// upNextLine says what follows the current track. On the last track it says so
// and points at /, which is the way to change that now that picking a search
// result queues only the track that was picked.
func (m playerModel) upNextLine(content int) string {
	label := dimStyle.Render("Up next") + "  "

	if index := m.index(); index+1 < len(m.tracks) {
		prefix := label + dimStyle.Render("›") + "  "
		room := maxInt(content-cellWidth(prefix), 1)
		return prefix + valueStyle.Render(clip(m.tracks[index+1].Label(), room))
	}

	room := maxInt(content-cellWidth(label), 1)
	return label + helpStyle.Render(clip("nothing yet — / adds more", room))
}

func (m playerModel) queueLines(content int) []string {
	current := m.index()
	start := clampInt(current-queueWindow/2, 0, maxInt(0, len(m.tracks)-queueWindow))
	end := minInt(start+queueWindow, len(m.tracks))

	lines := []string{dimStyle.Render(fmt.Sprintf("Queue  %d of %d", current+1, len(m.tracks)))}
	for i := start; i < end; i++ {
		label := clip(m.tracks[i].Label(), content-2)
		if i == current {
			lines = append(lines, cursorStyle.Render("▸ ")+selectedItem.Render(label))
			continue
		}
		lines = append(lines, "  "+helpStyle.Render(label))
	}
	return lines
}

var helpHints = [][2]string{
	{"space", "pause or resume"},
	{"← →", "seek 5 seconds"},
	{"⇧ ← →", "seek 30 seconds"},
	{"↑ ↓", "volume up or down"},
	{"n / p", "next or previous track"},
	{"a", "show or hide the queue"},
	{"/", "search and add to the queue"},
	{"y", "show or hide the lyrics"},
	{"v", "show or hide the spectrum"},
	{"esc / q", "close a panel, or quit"},
}

// helpLines draws the key list, stopping when the window cannot hold it all.
// Stopping is said out loud rather than done quietly, so a missing key is
// never mistaken for a key that does not exist.
func (m playerModel) helpLines(content, budget int) []string {
	if budget <= 0 {
		return nil
	}

	lines := []string{dimStyle.Render("Keys")}
	capacity := budget - len(lines)

	shown := len(helpHints)
	truncated := false
	if shown > capacity {
		shown = maxInt(capacity-1, 0)
		truncated = true
	}

	for _, hint := range helpHints[:shown] {
		lines = append(lines, "  "+
			keyStyle.Render(padRight(hint[0], 7))+" "+
			labelStyle.Render(clip(hint[1], content-12)))
	}
	if truncated {
		lines = append(lines, helpStyle.Render(fmt.Sprintf("… %d more", len(helpHints)-shown)))
	}
	return lines
}

// noticeLine is the one-line place for things the user needs to be told: what
// the engine is doing, and anything that needs acting on.
func (m playerModel) noticeLine(content int) string {
	if m.engine == nil {
		// A cold start spends twenty seconds booting a browser, and nothing
		// else on screen moves while it does. So the pulse is drawn even on the
		// narrowest card: a still picture of a working app is what a hang looks
		// like, and this is the one moment where the two cannot be told apart.
		head := m.accentStyle().Render(m.pulseFrame()) + "  " + valueStyle.Render("Initializing")
		if room := content - cellWidth(head); room > 4 {
			return head + helpStyle.Render(clip(" — booting a browser", room))
		}
		return head
	}

	if m.status.PreviewFallback {
		return errorStyle.Render(clip("preview only - run `am login` to restore full-length playback", content))
	}
	// The queue could not be extended. Saying so is the difference between
	// music that stopped and music that stopped for a reason.
	if m.radioErr != nil {
		return errorStyle.Render(clip("could not add more tracks: "+m.radioErr.Error(), content))
	}
	if m.monitor != nil && !m.monitor.Live() {
		return helpStyle.Render("spectrum is a stand-in: system audio could not be read")
	}
	return ""
}

// stateLine reports what the transport is doing. "Playing" carries dots that
// move while the track does, so a line that has stopped moving means the engine
// has stopped answering — which is worth being able to see, and is not visible
// anywhere else. It is drawn only by the layouts with a row to spare, because a
// stalled engine is not worth the keys that would let you do something about it.
func (m playerModel) stateLine(content int) string {
	switch {
	case m.engine == nil, m.status.Finished:
		// The notice line is already saying that the engine is coming up, and
		// a finished queue is on its way out of the interface.
		return ""
	case m.status.Paused:
		return helpStyle.Render("Paused")
	case m.started:
		// The dots are the thing being measured: "Playing" is fixed, and on
		// anything wider than the narrowest card there is room for all three.
		dots := clampInt(content-cellWidth("Playing"), 1, m.pulse%3+1)
		return valueStyle.Render("Playing") + m.accentStyle().Render(strings.Repeat(".", dots))
	}
	return ""
}

// pulseFrame is the point growing into a circle on the status line.
func (m playerModel) pulseFrame() string {
	return pulseFrames[clampInt(m.pulse%len(pulseFrames), 0, len(pulseFrames)-1)]
}

// footerHints is the hint footer, grouped into the lines it is drawn on, so a
// narrow card never drops a single stray hint onto a line of its own.
var footerHints = [][][2]string{
	{{"space", "play"}, {"←→", "seek"}, {"↑↓", "vol"}, {"n/p", "track"}},
	{{"a", "queue"}, {"/", "search"}, {"y", "lyrics"}, {"v", "viz"}, {"?", "help"}, {"q", "quit"}},
}

// narrowFooterHints is the same footer without the spectrum key. The spectrum
// is not drawn on a card that narrow, so pressing the key would change
// nothing, and the line it costs is the line that decides whether the footer
// fits at all.
var narrowFooterHints = [][][2]string{
	{{"space", "play"}, {"←→", "seek"}, {"↑↓", "vol"}, {"n/p", "track"}},
	{{"a", "queue"}, {"/", "search"}, {"y", "lyrics"}, {"?", "help"}, {"q", "quit"}},
}

// The search panel draws its own hints, because the transport keys are exactly
// the ones that stop responding while it is open.
var (
	searchTypingHints = [][][2]string{
		{{"enter", "search"}, {"esc", "close"}},
	}
	searchResultHints = [][][2]string{
		{{"↑↓", "choose"}, {"enter", "add to queue"}, {"/", "edit"}, {"esc", "close"}},
	}
)

func (m playerModel) footerLines(content int) []string {
	groups := footerHints
	switch {
	case m.search.editing:
		groups = searchTypingHints
	case m.search.open:
		groups = searchResultHints
	case content < spectrumMinContent:
		groups = narrowFooterHints
	}

	var lines []string

	for _, group := range groups {
		hints := make([]string, len(group))
		for i, hint := range group {
			hints[i] = keyStyle.Render(hint[0]) + " " + labelStyle.Render(hint[1])
		}
		lines = append(lines, wrapHints(hints, content)...)
	}
	return lines
}

// wrapHints lays the hints out on as few lines as fit, evening the lines out
// as it goes. A greedy fill would pack the first line to the brim and strand
// the last hint on a line of its own, which reads like a mistake.
func wrapHints(hints []string, width int) []string {
	const separator = 3

	if len(hints) == 0 {
		return nil
	}

	// cost[i] is the best total slack for the first i hints, and breakAt[i]
	// is where the last of those lines begins. Squaring the slack is what
	// evens the lines out: one very short line costs more than several
	// medium ones.
	const oversized = 1 << 24

	cost := make([]int, len(hints)+1)
	breakAt := make([]int, len(hints)+1)

	for i := 1; i <= len(hints); i++ {
		cost[i] = oversized * len(hints)

		for start := 1; start <= i; start++ {
			lineWidth := 0
			for k := start - 1; k < i; k++ {
				if k > start-1 {
					lineWidth += separator
				}
				lineWidth += cellWidth(hints[k])
			}

			candidate := cost[start-1]
			if slack := width - lineWidth; slack >= 0 {
				candidate += slack * slack
			} else {
				// A hint wider than the line still has to be drawn, but such
				// a layout must never win over one that fits.
				candidate += oversized + (-slack)*(-slack)
			}

			if candidate < cost[i] {
				cost[i] = candidate
				breakAt[i] = start - 1
			}
		}
	}

	var lines []string
	for end := len(hints); end > 0; end = breakAt[end] {
		lines = append(lines, strings.Join(hints[breakAt[end]:end], strings.Repeat(" ", separator)))
	}
	// The walk above builds the lines back to front.
	reverseStrings(lines)
	return lines
}

func reverseStrings(items []string) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

// padLines makes every line exactly width cells. That is what lets the frame
// draw a straight right edge.
func padLines(lines []string, width int) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		padding := width - cellWidth(line)
		if padding <= 0 {
			out[i] = line
			continue
		}
		out[i] = line + strings.Repeat(" ", padding)
	}
	return out
}

func (m playerModel) index() int {
	if len(m.tracks) == 0 {
		return 0
	}
	return clampInt(m.status.Index, 0, len(m.tracks)-1)
}

func (m playerModel) currentTrack() player.Track {
	if len(m.tracks) == 0 {
		return player.Track{}
	}
	return m.tracks[m.index()]
}

// duration prefers the engine's answer and falls back to the track's own
// metadata while the engine is still starting up.
func (m playerModel) duration(track player.Track) float64 {
	duration := m.status.Duration
	if duration <= 0 {
		duration = float64(track.DurationMs) / 1000
	}
	if duration <= 0 {
		// The shortest thing we ever play is a 30 second preview.
		duration = 30
	}
	return duration
}

// accentStyle is the player's highlight colour: Apple's accent until a cover
// supplies a tone of its own.
func (m playerModel) accentStyle() lipgloss.Style {
	if m.tint != "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.tint))
	}
	return lipgloss.NewStyle().Foreground(accent)
}

// summarizeArtists shortens Apple's joined artist string. A credit like
// "Vishnu Vijay, Vinayak Sasikumar, K S Harisankar & Poornima Kannan" becomes
// "Vishnu Vijay +3", which fits on one line and still names someone.
func summarizeArtists(artists string) string {
	trimmed := strings.TrimSpace(artists)
	if trimmed == "" {
		return ""
	}

	parts := splitArtists(trimmed)
	if len(parts) <= 2 {
		return strings.Join(parts, " & ")
	}
	return parts[0] + " +" + itoa(len(parts)-1)
}

// splitArtists reverses Apple's joining, which uses commas and ampersands.
func splitArtists(joined string) []string {
	var parts []string
	for _, chunk := range strings.Split(joined, ",") {
		for _, piece := range strings.Split(chunk, " & ") {
			if trimmed := strings.TrimSpace(piece); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	return parts
}

// centreVertically pads a block out to height, splitting the slack between top
// and bottom.
func centreVertically(lines []string, height int) []string {
	if len(lines) >= height {
		return lines
	}

	top := (height - len(lines)) / 2
	out := make([]string, 0, height)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// Play runs the interactive player until the user quits or playback ends.
//
// The engine is brought up by the interface rather than before it, so choosing
// a track leads straight to the player and the wait happens on screen. That
// makes the interface the owner of the engine's lifetime, including the case
// where the user quits while it is still starting.
func Play(opts Options) error {
	if len(opts.Tracks) == 0 {
		return fmt.Errorf("nothing to play")
	}

	// The spectrum follows the system's output device, so it works for both
	// engines and reports nothing when nothing is playing.
	monitor := visualizer.Start()
	defer monitor.Close()

	// The engine travels back over a buffered channel as well as through the
	// interface, which is what lets a start that is still in flight be waited
	// for on the way out instead of being left behind as an orphan process.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan player.Engine, 1)

	model := playerModel{
		tracks:       opts.Tracks,
		searcher:     opts.Search,
		lyricist:     opts.Lyrics,
		extender:     opts.Extend,
		stopAfter:    opts.StopAfter,
		monitor:      monitor,
		bands:        make([]float64, visualizer.BandCount),
		palette:      defaultPalette,
		showSpectrum: true,
		// The engine reports the real level on the first poll; until then the
		// header should not claim the volume is zero.
		status: player.Status{Volume: 100},
		width:  80,
		height: 24,
	}

	if opts.Start != nil {
		model.start = func() tea.Cmd {
			return func() tea.Msg {
				engine, err := opts.Start(ctx)
				ready <- engine
				if err != nil {
					return engineErrMsg{err}
				}
				return engineReadyMsg{engine}
			}
		}
	}

	program := tea.NewProgram(model, tea.WithAltScreen())
	final, err := program.Run()

	if model.start != nil {
		shutdownEngine(ready, cancel, shutdownGrace)
	}

	if err != nil {
		return err
	}
	if result, ok := final.(playerModel); ok && result.err != nil {
		return result.err
	}
	return nil
}

// shutdownEngine closes whatever the start produced, waiting for an attempt
// that is still in flight. A quit during startup must not leave a browser
// running with nothing attached to it, and it must not hang a quit on a start
// that is not coming back either.
func shutdownEngine(ready <-chan player.Engine, cancel context.CancelFunc, grace time.Duration) {
	cancel()

	select {
	case engine := <-ready:
		if engine != nil {
			_ = engine.Close()
		}
	case <-time.After(grace):
	}
}
