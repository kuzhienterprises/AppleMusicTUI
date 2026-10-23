package player

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
)

// The daemon publishes where its browser is through a small file, and that
// file is the only thing a client has to find. Its round trip is worth
// testing, because a corrupt file would leave clients unable to tell a
// running daemon from a dead one.
func TestDaemonStateRoundTrip(t *testing.T) {
	original := config.DaemonFile
	config.DaemonFile = filepath.Join(t.TempDir(), "daemon.json")
	t.Cleanup(func() { config.DaemonFile = original })

	state := DaemonState{
		PID:      42,
		Port:     1234,
		Ready:    true,
		LastUsed: time.Now().Truncate(time.Second),
	}
	if err := writeDaemonState(state); err != nil {
		t.Fatalf("writing the state: %v", err)
	}

	got, ok := readDaemonState()
	if !ok {
		t.Fatal("the state file was not read back")
	}
	if got.PID != state.PID || got.Port != state.Port || !got.Ready {
		t.Fatalf("the state did not survive the round trip: %+v", got)
	}
	if got.LastUsed.IsZero() {
		t.Fatal("the last use did not survive the round trip")
	}
}

// A missing state file means there is no daemon, and clients must not mistake
// anything else for one.
func TestMissingDaemonStateIsNotRunning(t *testing.T) {
	original := config.DaemonFile
	config.DaemonFile = filepath.Join(t.TempDir(), "nothing-here.json")
	t.Cleanup(func() { config.DaemonFile = original })

	if _, ok := readDaemonState(); ok {
		t.Fatal("a missing state file must not look like a running daemon")
	}
	if _, running := DaemonStatus(); running {
		t.Fatal("a missing state file must not report a warm player")
	}
}
