package player

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
)

// Event is anything mpv reports asynchronously.
type Event struct {
	Event  string          `json:"event"`
	Name   string          `json:"name"`
	Data   json.RawMessage `json:"data"`
	Reason string          `json:"reason"`
}

// Options configure how mpv is launched.
type Options struct {
	// Idle keeps mpv alive after the playlist ends, which is what the
	// interactive player needs. One-shot playback leaves it false so mpv
	// exits on its own.
	Idle bool
	// StartIndex is the position in the playlist to begin at.
	StartIndex int
}

// Mpv is a running mpv process together with a control channel.
type Mpv struct {
	cmd      *exec.Cmd
	conn     net.Conn
	pipePath string
	events   chan Event
	exited   chan struct{}

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	closed  bool
}

// FindMpv locates the mpv executable: the configured path, then PATH, then
// the usual winget install locations.
func FindMpv() (string, error) {
	if configured := config.Load().MpvPath; configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
	}

	if path, err := exec.LookPath("mpv"); err == nil {
		return path, nil
	}

	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			patterns := []string{
				filepath.Join(local, "Microsoft", "WinGet", "Links", "mpv.exe"),
				// winget extracts to a versioned package directory and does
				// not always leave a shim on PATH.
				filepath.Join(local, "Microsoft", "WinGet", "Packages", "mpv*", "mpv.exe"),
				filepath.Join(local, "Microsoft", "WinGet", "Packages", "mpv*", "*", "mpv.exe"),
			}
			for _, pattern := range patterns {
				matches, err := filepath.Glob(pattern)
				if err == nil && len(matches) > 0 {
					return matches[0], nil
				}
			}
		}
	}

	return "", errors.New("mpv was not found; install it with `winget install mpv-player.mpv-CI.MSVC` or set mpvPath in the config")
}

// StartMpv launches mpv with the given playlist and connects to its IPC
// socket.
func StartMpv(tracks []Track, opts Options) (*Mpv, error) {
	if len(tracks) == 0 {
		return nil, errors.New("nothing to play")
	}
	if opts.StartIndex < 0 || opts.StartIndex >= len(tracks) {
		opts.StartIndex = 0
	}

	execPath, err := FindMpv()
	if err != nil {
		return nil, err
	}

	pipePath := ipcPath()
	args := []string{
		"--no-video",
		"--no-terminal",
		"--really-quiet",
		"--audio-display=no",
		"--force-window=no",
		"--prefetch-playlist=yes",
		"--input-ipc-server=" + pipePath,
		fmt.Sprintf("--idle=%s", yesNo(opts.Idle)),
		fmt.Sprintf("--playlist-start=%d", opts.StartIndex),
		"--",
	}
	args = append(args, urlsOf(tracks)...)

	cmd := exec.Command(execPath, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mpv: %w", err)
	}

	conn, err := dialIPC(pipePath, 10*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	instance := &Mpv{
		cmd:      cmd,
		conn:     conn,
		pipePath: pipePath,
		events:   make(chan Event, 128),
		exited:   make(chan struct{}),
		pending:  make(map[int]chan json.RawMessage),
	}

	go func() {
		_ = cmd.Wait()
		close(instance.exited)
	}()
	go instance.readLoop()

	return instance, nil
}

// Events streams mpv notifications. The channel closes when mpv exits.
func (m *Mpv) Events() <-chan Event { return m.events }

// Exited is closed once the mpv process ends.
func (m *Mpv) Exited() <-chan struct{} { return m.exited }

// Command sends a command without waiting for a reply.
func (m *Mpv) Command(args ...any) error {
	_, err := m.request(args, false)
	return err
}

// GetProperty reads a single property value.
func (m *Mpv) GetProperty(name string, out any) error {
	raw, err := m.request([]any{"get_property", name}, true)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Observe asks mpv to push changes of a property as events.
func (m *Mpv) Observe(id int, name string) error {
	return m.Command("observe_property", id, name)
}

func (m *Mpv) request(args []any, wantReply bool) (json.RawMessage, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("the mpv connection is closed")
	}
	m.nextID++
	id := m.nextID
	var reply chan json.RawMessage
	if wantReply {
		reply = make(chan json.RawMessage, 1)
		m.pending[id] = reply
	}
	payload := map[string]any{"command": args, "request_id": id}
	m.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if _, err := m.conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	if !wantReply {
		return nil, nil
	}

	select {
	case raw := <-reply:
		return raw, nil
	case <-time.After(5 * time.Second):
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return nil, fmt.Errorf("timed out waiting for property %v", args)
	case <-m.exited:
		return nil, errors.New("mpv exited")
	}
}

// Close stops mpv and releases the IPC socket.
func (m *Mpv) Close() error {
	m.mu.Lock()
	alreadyClosed := m.closed
	m.mu.Unlock()
	if alreadyClosed {
		return nil
	}

	_ = m.Command("quit")

	select {
	case <-m.exited:
	case <-time.After(2 * time.Second):
		if m.cmd.Process != nil {
			_ = m.cmd.Process.Kill()
		}
		<-m.exited
	}

	m.mu.Lock()
	m.closed = true
	_ = m.conn.Close()
	m.mu.Unlock()

	if runtime.GOOS != "windows" {
		_ = os.Remove(m.pipePath)
	}
	return nil
}

func (m *Mpv) readLoop() {
	defer close(m.events)

	scanner := bufio.NewScanner(m.conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		var message struct {
			Event     string          `json:"event"`
			Name      string          `json:"name"`
			Data      json.RawMessage `json:"data"`
			Reason    string          `json:"reason"`
			RequestID int             `json:"request_id"`
			Error     string          `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			continue
		}

		if message.RequestID != 0 {
			m.mu.Lock()
			reply, ok := m.pending[message.RequestID]
			delete(m.pending, message.RequestID)
			m.mu.Unlock()
			if ok {
				reply <- message.Data
			}
			continue
		}

		if message.Event == "" {
			continue
		}

		select {
		case m.events <- Event{Event: message.Event, Name: message.Name, Data: message.Data, Reason: message.Reason}:
		default:
			// Never block mpv's reader on a slow consumer.
		}
	}
}

func ipcPath() string {
	suffix := fmt.Sprintf("mpv-amcli-%d-%d", os.Getpid(), time.Now().UnixNano())
	if runtime.GOOS == "windows" {
		return `\\.\pipe\` + suffix
	}
	return filepath.Join(os.TempDir(), suffix+".sock")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
