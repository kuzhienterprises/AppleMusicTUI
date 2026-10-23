package player

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/cdp"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

const queueTimeout = 45 * time.Second

// browserReadyTimeout covers Chrome opening its debugger and the Apple Music
// page booting. Both are slow on a cold start: the first run has to unpack a
// browser profile, fetch Apple's player bundle and start a software decoder,
// and on a slow or busy machine that has been measured at a full minute. The
// wait is not silent — the player shows a pulsing "Initializing" line the whole
// time — so it is better to wait than to give up on a boot that is merely slow.
//
// AM_BROWSER_TIMEOUT raises the ceiling for machines slower than that:
//
//	AM_BROWSER_TIMEOUT=3m am play "..."
var browserReadyTimeout = func() time.Duration {
	if v := os.Getenv("AM_BROWSER_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}()

// BootTimings is how long the browser took to come up, phase by phase.
//
// It exists because "the browser is slow" is not a diagnosis. The phases are
// very different problems: a slow process spawn is a disk or antivirus
// problem, a slow page load is Apple's site bundle, and a slow MusicKit is a
// stalled script. Only the measurements say which one it was.
type BootTimings struct {
	// Process is from spawning the browser to its debugger answering.
	Process time.Duration
	// Target is from the debugger answering to the Apple Music page existing.
	Target time.Duration
	// MusicKit is from the page existing to MusicKit being usable.
	MusicKit time.Duration
	// Queue is how long handing the tracks to MusicKit took.
	Queue time.Duration
	// FirstPlay is from the launch to the first sound.
	FirstPlay time.Duration
	// DOMReady and PageLoad are the page's own numbers, as the browser
	// measured them: when the HTML had been parsed, and when the load event
	// fired.
	DOMReady time.Duration
	PageLoad time.Duration
}

// String renders the timings as one line, slowest phase first.
func (b BootTimings) String() string {
	return fmt.Sprintf("browser boot: process %s, page target %s, MusicKit %s, queue %s, first sound %s (page: DOM %s, load %s)",
		b.Process.Round(100*time.Millisecond),
		b.Target.Round(100*time.Millisecond),
		b.MusicKit.Round(100*time.Millisecond),
		b.Queue.Round(100*time.Millisecond),
		b.FirstPlay.Round(100*time.Millisecond),
		b.DOMReady.Round(100*time.Millisecond),
		b.PageLoad.Round(100*time.Millisecond))
}

// BrowserEngine plays full-length tracks by driving Apple's own web player
// inside a browser window that is parked off-screen.
//
// Full-length audio is DRM encrypted: Apple hands the audio key to a licensed
// Content Decryption Module, and the CDM only exists inside a browser. Rather
// than decrypt the stream ourselves, we let the browser own playback and steer
// it through the DevTools protocol, using the MusicKit object the web player
// already exposes.
type BrowserEngine struct {
	cmd    *exec.Cmd
	exited chan struct{}
	client *cdp.Client

	// headless records how this browser was launched, so a boot failure can
	// say whether the mode itself is the likely cause.
	headless bool

	// timings holds how long each phase of the boot took.
	timings BootTimings

	// launchedAt is when the browser process was spawned, which is the zero
	// point every boot measurement is counted from.
	launchedAt time.Time

	// ownsBrowser is false when the browser belongs to a daemon: the engine
	// may drive it, but it must never close it.
	ownsBrowser bool

	// startIndex is the queue position playback begins at.
	startIndex int

	warned bool
	closed bool

	// mu guards tracks, which grows while playback continues: additions come
	// from the interface's goroutine while the status poll reads the same
	// slice from another.
	mu     sync.Mutex
	tracks []Track
}

// BrowserOptions configure how the hidden browser is launched.
type BrowserOptions struct {
	// ShowWindow keeps the window on screen. The default is to park it far
	// off-screen so playback stays invisible.
	ShowWindow bool
	// Headless runs Chrome's new headless mode, which draws nothing at all:
	// there is no window, not even one placed off-screen. It is opt-in rather
	// than the default, because headless depends on the Chrome build for both
	// its audio output and its DRM path, while an off-screen window uses the
	// ordinary renderer, audio and Widevine pipelines. `am play --headless`
	// selects it.
	Headless bool
	// Port is the debugging port to use. Zero picks a free one; a daemon
	// fixes its port so that clients can find the browser again.
	Port int
	// NoDaemon keeps the engine from using or starting a player daemon, and
	// makes it start a browser of its own every time.
	NoDaemon bool
	// StartIndex is the queue position playback begins at. It matters for
	// one thing: the media pipeline is only warmed up early when the first
	// track is the one that is going to play.
	StartIndex int
}

// browserLaunch is a browser that has been asked to start.
type browserLaunch struct {
	cmd      *exec.Cmd
	endpoint string
}

// launchBrowser starts the browser on Apple's player page. It waits for
// nothing: the daemon and the engine want different things from "ready", and
// both of them have to measure it themselves.
func launchBrowser(opts BrowserOptions) (browserLaunch, error) {
	browserPath, err := auth.FindBrowser()
	if err != nil {
		return browserLaunch{}, err
	}

	storefront := config.Load().Storefront
	if storefront == "" {
		storefront = "us"
	}

	port := opts.Port
	if port == 0 {
		port, err = freeTCPPort()
		if err != nil {
			return browserLaunch{}, err
		}
	}

	// How the window is hidden. The off-screen window is the default because
	// it uses the ordinary renderer, audio and DRM paths; --headless=new
	// draws nothing at all, including no window to park.
	window := []string{"--window-size=400,300", "--window-position=-32000,-32000"}
	switch {
	case opts.Headless:
		window = []string{"--headless=new"}
	case opts.ShowWindow:
		window = []string{"--window-size=900,700", "--window-position=0,0"}
	}

	args := append([]string{
		// The profile created by `am login` already holds the session, so
		// the web player is signed in as soon as it loads.
		"--user-data-dir=" + config.ProfileDir,
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		// Playback starts without a click, and Chrome blocks that by
		// default.
		"--autoplay-policy=no-user-gesture-required",
		"--disable-features=Translate,MediaRouter",
	}, window...)
	args = append(args, fmt.Sprintf("https://music.apple.com/%s/browse", storefront))

	cmd := exec.Command(browserPath, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return browserLaunch{}, fmt.Errorf("starting the browser: %w", err)
	}
	return browserLaunch{cmd: cmd, endpoint: "http://127.0.0.1:" + strconv.Itoa(port)}, nil
}

// attached is a player page that is ready to be driven.
type attached struct {
	client *cdp.Client
	// pageURL is the page's debugger URL, which a daemon publishes so that
	// clients can connect without looking for the page first.
	pageURL string
	timings BootTimings
}

// attachPage finds the player page, connects to it and waits until MusicKit
// is usable.
func attachPage(ctx context.Context, endpoint string, launched time.Time) (attached, error) {
	var out attached

	if err := waitForEndpoint(ctx, endpoint, browserReadyTimeout); err != nil {
		return out, err
	}
	out.timings.Process = time.Since(launched)

	pageURL, err := musicPageTarget(ctx, endpoint)
	if err != nil {
		return out, err
	}
	out.timings.Target = time.Since(launched) - out.timings.Process

	client, err := cdp.Dial(ctx, pageURL)
	if err != nil {
		return out, err
	}
	waited, err := waitForMusicKit(ctx, client, launched)
	if err != nil {
		_ = client.Close()
		return out, err
	}
	out.client = client
	out.pageURL = pageURL
	out.timings.MusicKit = waited - out.timings.Process - out.timings.Target
	return out, nil
}

// attachKnownPage connects to a page whose debugger URL is already known,
// which is what a client does when it attaches to a daemon.
func attachKnownPage(ctx context.Context, pageURL string, launched time.Time) (attached, error) {
	var out attached

	client, err := cdp.Dial(ctx, pageURL)
	if err != nil {
		return out, err
	}
	waited, err := waitForMusicKit(ctx, client, launched)
	if err != nil {
		_ = client.Close()
		return out, err
	}
	out.client = client
	out.pageURL = pageURL
	out.timings.MusicKit = waited
	return out, nil
}

// waitForMusicKit waits until MusicKit is usable in a page that is already
// connected, and reports how long that took.
func waitForMusicKit(ctx context.Context, client *cdp.Client, launched time.Time) (time.Duration, error) {
	deadline := time.Now().Add(browserReadyTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		ready, err := client.EvaluateBool(ctx, `typeof window.MusicKit !== 'undefined' && !!window.MusicKit.getInstance()`)
		if err == nil && ready {
			return time.Since(launched), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, errors.New("Apple's player did not finish loading in the browser")
}

// StartBrowserEngine launches the hidden browser, waits for Apple's web player
// to boot, and queues the given tracks. Playback does not begin until Play is
// called.
func StartBrowserEngine(ctx context.Context, tracks []Track, opts BrowserOptions) (*BrowserEngine, error) {
	if len(tracks) == 0 {
		return nil, errors.New("nothing to play")
	}

	// A warm player is worth a great deal: the browser is already running
	// with Apple's page loaded, so a queue can be handed over immediately
	// instead of paying the whole boot again.
	if !opts.NoDaemon {
		if client, state, ok := attachDaemon(ctx); ok {
			return newAttachedEngine(ctx, client, state, tracks, opts)
		}
		// Nothing is running, so start one. It costs what starting a
		// browser here would have cost, and it keeps the player warm for
		// every command after this one.
		if _, err := spawnDaemon(ctx, opts); err != nil {
			ui.Debug("no background player: %v", err)
		} else if client, state, ok := attachDaemon(ctx); ok {
			return newAttachedEngine(ctx, client, state, tracks, opts)
		}
	}

	launched := time.Now()
	launch, err := launchBrowser(opts)
	if err != nil {
		return nil, err
	}

	engine := &BrowserEngine{
		cmd:         launch.cmd,
		exited:      make(chan struct{}),
		tracks:      tracks,
		headless:    opts.Headless,
		launchedAt:  launched,
		ownsBrowser: true,
		startIndex:  opts.StartIndex,
	}
	go func() {
		_ = launch.cmd.Wait()
		close(engine.exited)
	}()

	if err := engine.connect(ctx, launch.endpoint, launched); err != nil {
		_ = engine.Close()
		return nil, err
	}
	if err := engine.queue(ctx); err != nil {
		_ = engine.Close()
		return nil, err
	}
	engine.timings.Queue = time.Since(launched) - engine.timings.Process - engine.timings.Target - engine.timings.MusicKit
	ui.Debug("%s", engine.timings)
	return engine, nil
}

// newAttachedEngine builds an engine around a daemon's player page. The
// browser belongs to the daemon, so this engine must never close it: it
// pauses the player and lets go of it when it is done.
func newAttachedEngine(ctx context.Context, client *cdp.Client, state DaemonState, tracks []Track, opts BrowserOptions) (*BrowserEngine, error) {
	launched := time.Now()
	engine := &BrowserEngine{
		client:     client,
		exited:     make(chan struct{}),
		tracks:     tracks,
		headless:   opts.Headless,
		launchedAt: launched,
		startIndex: opts.StartIndex,
	}
	engine.prepare(ctx)
	engine.timings.MusicKit = time.Since(launched)
	if err := engine.queue(ctx); err != nil {
		_ = engine.Close()
		return nil, err
	}
	engine.timings.Queue = time.Since(launched) - engine.timings.MusicKit
	ui.Debug("attached to the warm player in %s", time.Since(launched).Round(100*time.Millisecond))
	return engine, nil
}

// headlessHint explains a boot failure that headless mode is known to cause.
// DRM inside headless Chrome needs version 112 or newer, and asking an older
// build for it fails before anything reaches the screen, so the error is the
// only place the reason can appear.
func (e *BrowserEngine) headlessHint(err error) error {
	if !e.headless {
		return err
	}
	return fmt.Errorf("%w: --headless needs Chrome or Edge 112 or newer, and without the flag the window is parked off-screen instead", err)
}

// connect waits for the debugger, attaches to the Apple Music page and waits
// for MusicKit to become available.
func (e *BrowserEngine) connect(ctx context.Context, endpoint string, launched time.Time) error {
	page, err := attachPage(ctx, endpoint, launched)
	if err != nil {
		return e.headlessHint(err)
	}
	e.client = page.client
	e.timings = page.timings
	e.prepare(ctx)
	return nil
}

// prepare does what a player needs once its page is there: keeping the
// renderer awake, and making sure the volume is not a surprise.
func (e *BrowserEngine) prepare(ctx context.Context) {
	// An off-screen window is an occluded page, and Chrome throttles occluded
	// pages hard enough to stall audio and interrupt the DRM handshake.
	// Bringing the page to the front and emulating focus keeps the renderer
	// running at full speed without showing anything. A headless page is
	// visible to Chrome already, so both calls are no-ops there; they stay so
	// there is one code path rather than two.
	_ = e.client.Call(ctx, "Page.bringToFront", nil, nil)
	if err := e.client.Call(ctx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil); err != nil {
		ui.Debug("focus emulation unavailable: %v", err)
	}

	// MusicKit remembers its volume between sessions, and this profile
	// exists only for this CLI. Start at full volume so playback is never
	// mysteriously quiet.
	_ = e.client.Evaluate(ctx, `(() => {
  const mk = window.MusicKit.getInstance();
  if (mk) mk.volume = 1;
  return true;
})()`, nil)

	e.readPageTimings(ctx)
}

// readPageTimings asks the page how long its own load took. The numbers come
// from the browser rather than from this process, so they separate "the
// network was slow" from "the page was heavy".
func (e *BrowserEngine) readPageTimings(ctx context.Context) {
	var raw struct {
		DOMReady float64 `json:"domReady"`
		PageLoad float64 `json:"pageLoad"`
	}
	const script = `(() => {
  try {
    const nav = performance.getEntriesByType && performance.getEntriesByType('navigation')[0];
    if (nav) {
      return {
        domReady: nav.domContentLoadedEventEnd || 0,
        pageLoad: nav.loadEventEnd || 0,
      };
    }
    const t = performance.timing;
    return {
      domReady: (t.domContentLoadedEventEnd || 0) - t.navigationStart,
      pageLoad: (t.loadEventEnd || 0) - t.navigationStart,
    };
  } catch (err) {
    return { domReady: 0, pageLoad: 0 };
  }
})()`
	if err := e.client.Evaluate(ctx, script, &raw); err != nil {
		ui.Debug("could not read the page's own timings: %v", err)
		return
	}
	e.timings.DOMReady = time.Duration(raw.DOMReady * float64(time.Millisecond))
	e.timings.PageLoad = time.Duration(raw.PageLoad * float64(time.Millisecond))
}

// queueOutcome reads back whatever queue operation ran last. Every MusicKit
// queue call is asynchronous, so each one is confirmed by watching what the
// queue reports rather than by trusting the call itself.
const queueOutcome = `(() => {
  const mk = window.MusicKit.getInstance();
  const state = window.__am || {};
  return {
    ok: !!state.ok,
    error: state.error || '',
    length: (mk && mk.queue && mk.queue.length) || 0,
  };
})()`

// errQueueTimeout means Apple accepted a queue change but never applied it.
var errQueueTimeout = errors.New("the queue did not change in time")

// awaitQueue waits for the queue to reach a given length. Resolving and
// applying are two different things to MusicKit, and only the second one is
// visible to the user.
func (e *BrowserEngine) awaitQueue(ctx context.Context, expected int) error {
	deadline := time.Now().Add(queueTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}

		var state struct {
			OK     bool   `json:"ok"`
			Error  string `json:"error"`
			Length int    `json:"length"`
		}
		// A poll can fail on a busy machine — the browser is decoding audio and
		// loading a page, and one slow reply is not a queue failure. Keep
		// waiting; the deadline above is what decides, and it ends in a queue
		// error the interface knows how to report rather than a dead session.
		if err := e.client.Evaluate(ctx, queueOutcome, &state); err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if state.Error != "" {
			return errors.New(state.Error)
		}
		if state.OK && state.Length >= expected {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errQueueTimeout
}

// queue hands the whole playlist to MusicKit. Apple resolves the song ids
// against the storefront and builds its own queue, which is what makes
// skipToNextItem and neighbours work.
func (e *BrowserEngine) queue(ctx context.Context) error {
	ids := make([]string, 0, e.trackCount())
	e.mu.Lock()
	for _, track := range e.tracks {
		ids = append(ids, track.ID)
	}
	e.mu.Unlock()

	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}

	script := fmt.Sprintf(`(() => {
  const mk = window.MusicKit.getInstance();
  window.__am = { ok: false, error: null };
  mk.setQueue({ songs: %s })
    .then(() => {
      window.__am.ok = true;
      // Start playing the moment the queue lands, rather than waiting for
      // this process to notice and tell the page to play. The licence
      // request and the decoder warm-up then overlap with the confirmation
      // round trip instead of following it.
      if (%v) { try { mk.play(); } catch (err) {} }
    })
    .catch((err) => { window.__am.error = String((err && err.message) || err); });
  return true;
})()`, encoded, e.startIndex == 0)
	if err := e.client.Evaluate(ctx, script, nil); err != nil {
		return fmt.Errorf("queueing tracks in the browser: %w", err)
	}
	if err := e.awaitQueue(ctx, len(ids)); err != nil {
		return fmt.Errorf("Apple Music refused the queue: %w", err)
	}
	return nil
}

// Enqueue adds tracks to the end of the queue, which is "add to the queue" in
// the sense the interface means it: what is playing, and the order of what is
// already waiting, are both left alone.
//
// playLater is the documented call for that. Older builds of MusicKit only
// expose the queue object's own append, so that is the fallback rather than a
// failure.
func (e *BrowserEngine) Enqueue(ctx context.Context, tracks []Track) error {
	if len(tracks) == 0 {
		return nil
	}

	ids := make([]string, 0, len(tracks))
	for _, track := range tracks {
		ids = append(ids, track.ID)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}

	expected := e.trackCount() + len(ids)

	script := fmt.Sprintf(`(() => {
  const mk = window.MusicKit.getInstance();
  window.__am = { ok: false, error: null };
  const append = (typeof mk.playLater === 'function')
    ? (options) => mk.playLater(options)
    : (options) => { mk.queue.append(options); return Promise.resolve(); };
  append({ songs: %s })
    .then(() => { window.__am.ok = true; })
    .catch((err) => { window.__am.error = String((err && err.message) || err); });
  return true;
})()`, encoded)
	if err := e.client.Evaluate(ctx, script, nil); err != nil {
		return fmt.Errorf("adding to the browser queue: %w", err)
	}
	if err := e.awaitQueue(ctx, expected); err != nil {
		return fmt.Errorf("Apple Music refused the addition: %w", err)
	}

	e.mu.Lock()
	e.tracks = append(e.tracks, tracks...)
	e.mu.Unlock()
	return nil
}

// trackCount is the length of the queue, including anything added since
// playback began.
func (e *BrowserEngine) trackCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.tracks)
}

// trackDuration is the catalogue length of a queue entry in milliseconds,
// which is what the preview check below compares against.
func (e *BrowserEngine) trackDuration(index int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if index < 0 || index >= len(e.tracks) {
		return 0
	}
	return e.tracks[index].DurationMs
}

// Play moves to a queue position and starts playback. The queue is walked with
// skipToNextItem, which the web player exposes directly.
//
// MusicKit sometimes accepts play() before its media pipeline is ready and then
// never starts, so playback is confirmed and retried a few times.
func (e *BrowserEngine) Play(ctx context.Context, index int) error {
	if index < 0 || index >= e.trackCount() {
		index = 0
	}

	script := fmt.Sprintf(`(async () => {
  const mk = window.MusicKit.getInstance();
  const target = %d;
  // MusicKit reports -1 until an item is actually selected.
  const raw = () => (mk.queue && typeof mk.queue.position === 'number') ? mk.queue.position : 0;
  const position = () => { const p = raw(); return p < 0 ? 0 : p; };
  let guard = 0;
  while (position() < target && guard++ < 300) {
    mk.skipToNextItem();
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  await mk.play();
  return true;
})()`, index)

	for attempt := 0; attempt < 4; attempt++ {
		if err := e.client.Evaluate(ctx, script, nil); err != nil {
			return err
		}
		started, err := e.waitForPlayback(ctx, 4*time.Second)
		if err != nil {
			return err
		}
		if started {
			e.timings.FirstPlay = time.Since(e.launchedAt)
			ui.Debug("%s", e.timings)
			return nil
		}
		ui.Debug("playback has not started yet; retrying")
	}
	return errors.New("Apple's player never started playback")
}

// waitForPlayback reports whether audio is actually running.
func (e *BrowserEngine) waitForPlayback(ctx context.Context, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := e.Status(ctx)
		if err != nil {
			return false, err
		}
		if !status.Paused || status.Position > 0 {
			return true, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false, nil
}

// TogglePause pauses or resumes.
func (e *BrowserEngine) TogglePause(ctx context.Context) error {
	const script = `(() => {
  const mk = window.MusicKit.getInstance();
  if (!mk) return false;
  if (mk.isPlaying) {
    mk.pause();
  } else {
    mk.play();
  }
  return true;
})()`
	return e.client.Evaluate(ctx, script, nil)
}

// Seek moves the playhead by a relative number of seconds.
func (e *BrowserEngine) Seek(ctx context.Context, deltaSeconds float64) error {
	script := fmt.Sprintf(`(() => {
  const mk = window.MusicKit.getInstance();
  if (!mk) return false;
  const target = Math.max(0, (mk.currentPlaybackTime || 0) + (%f));
  mk.seekToTime(target);
  return true;
})()`, deltaSeconds)
	return e.client.Evaluate(ctx, script, nil)
}

// Next skips to the following queue item.
func (e *BrowserEngine) Next(ctx context.Context) error {
	const script = `(() => {
  const mk = window.MusicKit.getInstance();
  if (mk) mk.skipToNextItem();
  return true;
})()`
	return e.client.Evaluate(ctx, script, nil)
}

// Previous skips back a queue item.
func (e *BrowserEngine) Previous(ctx context.Context) error {
	const script = `(() => {
  const mk = window.MusicKit.getInstance();
  if (mk) mk.skipToPreviousItem();
  return true;
})()`
	return e.client.Evaluate(ctx, script, nil)
}

// AdjustVolume changes MusicKit's volume by a relative percentage.
func (e *BrowserEngine) AdjustVolume(ctx context.Context, deltaPercent float64) error {
	script := fmt.Sprintf(`(() => {
  const mk = window.MusicKit.getInstance();
  if (!mk) return false;
  const current = (typeof mk.volume === 'number') ? mk.volume : 1;
  mk.volume = Math.min(1, Math.max(0, current + (%f) / 100));
  return true;
})()`, deltaPercent)
	return e.client.Evaluate(ctx, script, nil)
}

// Status reads the web player's playback state.
func (e *BrowserEngine) Status(ctx context.Context) (Status, error) {
	var raw struct {
		Index    int     `json:"index"`
		Position float64 `json:"position"`
		Duration float64 `json:"duration"`
		Playing  bool    `json:"playing"`
		Volume   float64 `json:"volume"`
	}

	const script = `(() => {
  const mk = window.MusicKit && window.MusicKit.getInstance();
  if (!mk) return { index: 0, position: 0, duration: 0, playing: false, volume: 100 };
  const queue = mk.queue;
  return {
    index: (queue && typeof queue.position === 'number') ? queue.position : 0,
    position: mk.currentPlaybackTime || 0,
    duration: mk.currentPlaybackDuration || 0,
    playing: !!mk.isPlaying,
    volume: Math.round(((typeof mk.volume === 'number') ? mk.volume : 1) * 100),
  };
})()`

	if err := e.client.Evaluate(ctx, script, &raw); err != nil {
		// A browser that has gone away is indistinguishable from a queue
		// that has run out, as far as the interface is concerned.
		select {
		case <-e.exited:
			return Status{Finished: true}, nil
		default:
		}
		return Status{}, err
	}

	index := raw.Index
	if index < 0 {
		// MusicKit reports -1 until an item is selected.
		index = 0
	}

	status := Status{
		Index:    index,
		Position: raw.Position,
		Duration: raw.Duration,
		Paused:   !raw.Playing,
		Volume:   raw.Volume,
	}

	// A full-length track is minutes long; a preview is capped near 90
	// seconds. Comparing the two tells us whether the session is still good
	// or Apple quietly downgraded us.
	// A full-length track is minutes long; a preview is capped near 90
	// seconds. Comparing the two tells us whether the session is still good
	// or Apple quietly downgraded us.
	if full := e.trackDuration(index); full > 120000 && raw.Duration > 0 && raw.Duration < 120 {
		status.PreviewFallback = true
	}

	if raw.Duration > 0 && raw.Position >= raw.Duration-0.75 && !raw.Playing {
		status.Finished = true
	}
	return status, nil
}

// Close shuts the browser down, taking its renderer processes with it.
//
// A browser belonging to a daemon is left running: the engine pauses the
// player and lets go of it, and the daemon keeps the page warm for the next
// command.
func (e *BrowserEngine) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true

	if e.client != nil {
		if !e.ownsBrowser {
			// Stop the music, but keep the page.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = e.client.Evaluate(ctx, `(() => {
  const mk = window.MusicKit && window.MusicKit.getInstance();
  if (mk && mk.isPlaying) mk.pause();
  return true;
})()`, nil)
			cancel()
			_ = e.client.Close()
			releaseDaemon()
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = e.client.Call(ctx, "Browser.close", nil, nil)
		cancel()
		_ = e.client.Close()
	}

	select {
	case <-e.exited:
	case <-time.After(3 * time.Second):
		if e.cmd.Process != nil {
			killProcessTree(e.cmd.Process.Pid)
		}
		select {
		case <-e.exited:
		case <-time.After(3 * time.Second):
		}
	}
	return nil
}

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitForEndpoint(ctx context.Context, endpoint string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := client.Get(endpoint + "/json/version")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.New("the browser did not open its debugger in time")
}

// musicPageTarget finds the debugger URL of the Apple Music page, falling back
// to whatever page is available.
func musicPageTarget(ctx context.Context, endpoint string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	fallback := ""

	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		music, other, err := pageTargets(client, endpoint)
		if err == nil {
			if music != "" {
				return music, nil
			}
			if fallback == "" {
				fallback = other
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("the browser never opened the Apple Music page")
}

func pageTargets(client *http.Client, endpoint string) (music, other string, err error) {
	response, err := client.Get(endpoint + "/json/list")
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()

	var targets []struct {
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return "", "", err
	}

	for _, target := range targets {
		if target.Type != "page" || target.WebSocketDebuggerURL == "" {
			continue
		}
		if strings.Contains(target.URL, "music.apple.com") {
			return target.WebSocketDebuggerURL, "", nil
		}
		if other == "" {
			other = target.WebSocketDebuggerURL
		}
	}
	return "", other, nil
}

// killProcessTree ends the browser and every renderer it started.
func killProcessTree(pid int) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
		return
	}
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}
