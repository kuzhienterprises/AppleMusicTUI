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
}

// StartBrowserEngine launches the hidden browser, waits for Apple's web player
// to boot, and queues the given tracks. Playback does not begin until Play is
// called.
func StartBrowserEngine(ctx context.Context, tracks []Track, opts BrowserOptions) (*BrowserEngine, error) {
	if len(tracks) == 0 {
		return nil, errors.New("nothing to play")
	}

	browserPath, err := auth.FindBrowser()
	if err != nil {
		return nil, err
	}

	storefront := config.Load().Storefront
	if storefront == "" {
		storefront = "us"
	}

	port, err := freeTCPPort()
	if err != nil {
		return nil, err
	}

	window := []string{"--window-size=400,300", "--window-position=-32000,-32000"}
	if opts.ShowWindow {
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
		return nil, fmt.Errorf("starting the browser: %w", err)
	}

	engine := &BrowserEngine{cmd: cmd, exited: make(chan struct{}), tracks: tracks}
	go func() {
		_ = cmd.Wait()
		close(engine.exited)
	}()

	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := engine.connect(ctx, endpoint); err != nil {
		_ = engine.Close()
		return nil, err
	}
	if err := engine.queue(ctx); err != nil {
		_ = engine.Close()
		return nil, err
	}
	return engine, nil
}

// connect waits for the debugger, attaches to the Apple Music page and waits
// for MusicKit to become available.
func (e *BrowserEngine) connect(ctx context.Context, endpoint string) error {
	if err := waitForEndpoint(ctx, endpoint, browserReadyTimeout); err != nil {
		return err
	}

	wsURL, err := musicPageTarget(ctx, endpoint)
	if err != nil {
		return err
	}
	client, err := cdp.Dial(ctx, wsURL)
	if err != nil {
		return err
	}
	e.client = client

	// The window sits off-screen, and Chrome throttles occluded pages hard
	// enough to stall audio and interrupt the DRM handshake. Bringing the
	// page to the front and emulating focus keeps the renderer running at
	// full speed without showing anything.
	_ = client.Call(ctx, "Page.bringToFront", nil, nil)
	if err := client.Call(ctx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil); err != nil {
		ui.Debug("focus emulation unavailable: %v", err)
	}

	deadline := time.Now().Add(browserReadyTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := client.EvaluateBool(ctx, `typeof window.MusicKit !== 'undefined' && !!window.MusicKit.getInstance()`)
		if err == nil && ready {
			// MusicKit remembers its volume between sessions, and this
			// profile exists only for this CLI. Start at full volume so
			// playback is never mysteriously quiet.
			_ = client.Evaluate(ctx, `(() => {
  const mk = window.MusicKit.getInstance();
  if (mk) mk.volume = 1;
  return true;
})()`, nil)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("Apple's player did not finish loading in the browser")
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
    .then(() => { window.__am.ok = true; })
    .catch((err) => { window.__am.error = String((err && err.message) || err); });
  return true;
})()`, encoded)
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
func (e *BrowserEngine) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true

	if e.client != nil {
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
