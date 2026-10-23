package player

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/cdp"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// A daemon is a browser that outlives the command that started it.
//
// Everything expensive about starting playback happens before a single note
// plays, and almost none of it depends on what you asked to play: the browser
// has to start, Apple's page has to load, MusicKit has to be created, and the
// decryption module has to be negotiated. Paying all of that on every run is
// why the first track used to take twenty seconds.
//
// The daemon does that work once. The browser stays alive with the player page
// loaded, and a client attaches to the running page, hands over a queue, and
// the first sound arrives in a fraction of the time. The daemon keeps the page
// warm for the next client as well, so the cost is paid once per machine
// rather than once per command.
//
// The browser is still Apple's own player, still doing its own DRM: nothing
// here changes what plays, only how long you wait for it.
const (
	// daemonIdleTimeout is how long a daemon keeps the browser alive after
	// the last client let go of it.
	daemonIdleTimeout = 15 * time.Minute

	// daemonAttachTimeout is how long a client waits for a daemon that is
	// still booting its player page.
	daemonAttachTimeout = 90 * time.Second

	// daemonLockTimeout is how long a client's hold on the player is
	// honoured. It only matters when a client was killed without releasing
	// the lock, which would otherwise block the player forever.
	daemonLockTimeout = 30 * time.Minute
)

// DaemonState is the handshake between a daemon and the clients that find it.
// It is a small file so that a client can attach to a daemon it did not
// start.
type DaemonState struct {
	// PID is the daemon process.
	PID int `json:"pid"`
	// Port is the browser's debugging port.
	Port int `json:"port"`
	// PageURL is the player page's debugger URL. A client that knows it can
	// connect straight to the page instead of searching the browser for it.
	PageURL string `json:"pageUrl,omitempty"`
	// Started is when the daemon launched the browser.
	Started time.Time `json:"started"`
	// Ready is true once the player page has loaded and MusicKit is usable.
	Ready bool `json:"ready"`
	// ClientPID is the process currently driving the player, if any.
	ClientPID int `json:"clientPid,omitempty"`
	// ClientSince is when that client took the player.
	ClientSince time.Time `json:"clientSince,omitempty"`
	// LastUsed is when a client last let go of the player.
	LastUsed time.Time `json:"lastUsed"`
	// Stop asks a running daemon to shut down.
	Stop bool `json:"stop,omitempty"`
}

// readDaemonState reads the state file. A missing or unreadable file means
// there is no daemon.
func readDaemonState() (DaemonState, bool) {
	data, err := os.ReadFile(config.DaemonFile)
	if err != nil {
		return DaemonState{}, false
	}
	var state DaemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return DaemonState{}, false
	}
	return state, true
}

// writeDaemonState publishes the state file.
func writeDaemonState(state DaemonState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(config.DaemonFile, append(data, '\n'), 0o600)
}

// DaemonStatus describes the player daemon for `am doctor`. It reports
// whether a warm player is waiting to be used.
func DaemonStatus() (string, bool) {
	state, ok := readDaemonState()
	if !ok || !state.Ready {
		return "not running", false
	}
	return fmt.Sprintf("running and warm (pid %d, port %d)", state.PID, state.Port), true
}

// RunDaemon keeps a browser alive with Apple's player page loaded, until the
// context is cancelled, the state file asks it to stop, or it goes idle.
//
// The browser is launched with the same profile `am login` uses, so the player
// is signed in as soon as the page loads.
func RunDaemon(ctx context.Context, opts BrowserOptions) error {
	port, err := freeTCPPort()
	if err != nil {
		return err
	}
	opts.Port = port

	launch, err := launchBrowser(opts)
	if err != nil {
		return err
	}
	launched := time.Now()

	state := DaemonState{PID: os.Getpid(), Port: port, Started: launched}
	if err := writeDaemonState(state); err != nil {
		killProcessTree(launch.cmd.Process.Pid)
		return err
	}
	defer func() {
		_ = os.Remove(config.DaemonFile)
		killProcessTree(launch.cmd.Process.Pid)
	}()

	page, err := attachPage(ctx, launch.endpoint, launched)
	if err != nil {
		return err
	}
	_ = page.client.Close()

	state.Ready = true
	state.PageURL = page.pageURL
	state.LastUsed = time.Now()
	if err := writeDaemonState(state); err != nil {
		return err
	}
	ui.OK("The player is warm. Any am command will now attach to it.")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, ok := readDaemonState()
			if !ok {
				// The state file is how a daemon is asked to go
				// away, and removing it is one way to ask.
				return nil
			}
			if current.Stop {
				return nil
			}
			if current.PID != state.PID {
				// Another daemon took the file over.
				return nil
			}
			if current.ClientPID == 0 && time.Since(current.LastUsed) > daemonIdleTimeout {
				return nil
			}
		}
	}
}

// StopDaemon asks a running daemon to shut down.
func StopDaemon() error {
	state, ok := readDaemonState()
	if !ok {
		return errors.New("no player daemon is running")
	}
	state.Stop = true
	return writeDaemonState(state)
}

// attachDaemon finds a running daemon and connects to its player page. It
// reports false when there is no daemon worth attaching to, which is not an
// error: the caller falls back to starting a browser of its own.
func attachDaemon(ctx context.Context) (*cdp.Client, DaemonState, bool) {
	state, ok := readDaemonState()
	if !ok || !state.Ready || state.Stop {
		return nil, state, false
	}

	// A hold on the player is honoured unless the client holding it is long
	// gone, in which case it is a leftover from a crash.
	if state.ClientPID != 0 && state.ClientPID != os.Getpid() &&
		time.Since(state.ClientSince) < daemonLockTimeout {
		return nil, state, false
	}

	endpoint := "http://127.0.0.1:" + strconv.Itoa(state.Port)
	if !debuggerAnswers(endpoint) {
		return nil, state, false
	}

	// The page is already loaded, so this returns at once. The wait is short
	// because a daemon that has lost its page is not worth attaching to.
	attachCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	launched := time.Now()
	var page attached
	var err error
	if state.PageURL != "" {
		// The daemon published the page's debugger URL, so there is no
		// need to go looking for it.
		page, err = attachKnownPage(attachCtx, state.PageURL, launched)
	} else {
		page, err = attachPage(attachCtx, endpoint, launched)
	}
	if err != nil {
		return nil, state, false
	}

	state.ClientPID = os.Getpid()
	state.ClientSince = time.Now()
	if err := writeDaemonState(state); err != nil {
		_ = page.client.Close()
		return nil, state, false
	}
	return page.client, state, true
}

// releaseDaemon hands the player back to the daemon: the hold is dropped and
// the last use is recorded, which is what keeps the daemon alive for the next
// command rather than idling out.
func releaseDaemon() {
	current, ok := readDaemonState()
	if !ok {
		return
	}
	if current.ClientPID == os.Getpid() {
		current.ClientPID = 0
		current.ClientSince = time.Time{}
	}
	current.LastUsed = time.Now()
	_ = writeDaemonState(current)
}

// WarmDaemon starts a daemon in the background without waiting for it, so
// that the next command finds a player that is already warm. It is how the
// first song of a session avoids waiting for a browser.
func WarmDaemon(opts BrowserOptions) {
	go func() {
		if _, err := spawnDaemon(context.Background(), opts); err != nil {
			ui.Debug("could not warm the player: %v", err)
		}
	}()
}

// spawnDaemon starts a daemon in the background and waits until its player
// page is ready. The daemon outlives this process: that is the whole point of
// it.
func spawnDaemon(ctx context.Context, opts BrowserOptions) (DaemonState, error) {
	before := time.Now()

	executable, err := os.Executable()
	if err != nil {
		return DaemonState{}, err
	}
	args := []string{"daemon"}
	if opts.Headless {
		args = append(args, "--headless")
	}
	cmd := exec.Command(executable, args...)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return DaemonState{}, fmt.Errorf("starting the background player: %w", err)
	}
	// The daemon is its own process now. Nothing waits for it, and nothing
	// kills it when this command exits.
	go func() { _ = cmd.Wait() }()

	state, ok := waitForDaemon(ctx, before)
	if !ok {
		return DaemonState{}, errors.New("the background player did not come up in time")
	}
	return state, nil
}

// waitForDaemon polls the state file until a daemon started after the given
// moment is ready.
func waitForDaemon(ctx context.Context, before time.Time) (DaemonState, bool) {
	deadline := time.Now().Add(daemonAttachTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return DaemonState{}, false
		}
		state, ok := readDaemonState()
		if ok && state.Ready && !state.Stop && state.Started.After(before) {
			return state, true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return DaemonState{}, false
}

// debuggerAnswers reports whether something is listening on a debugging port.
func debuggerAnswers(endpoint string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint + "/json/version")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
