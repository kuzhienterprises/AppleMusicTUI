// Package config resolves where the CLI keeps its credentials and settings.
//
// Tokens are secrets, so they live outside the source tree in the user's
// application data directory, never in the repository.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config is the persisted state of the CLI.
type Config struct {
	// DevToken is the ES256 developer JWT scraped from the Apple Music web
	// player. It is required for every catalog request.
	DevToken string `json:"devToken,omitempty"`
	// DevTokenExp is the expiry of DevToken, in Unix seconds.
	DevTokenExp int64 `json:"devTokenExp,omitempty"`
	// MusicUserToken grants access to the signed-in user's library.
	MusicUserToken string `json:"musicUserToken,omitempty"`
	// Storefront is the two-letter country code, e.g. "us" or "in".
	Storefront string `json:"storefront,omitempty"`
	// MpvPath overrides autodetection of the mpv binary.
	MpvPath   string `json:"mpvPath,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func baseDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "applemusic-cli")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "applemusic-cli")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".applemusic-cli"
	}
	return filepath.Join(home, ".config", "applemusic-cli")
}

var (
	// Dir holds all CLI state.
	Dir = baseDir()
	// FilePath is the JSON settings file.
	FilePath = filepath.Join(Dir, "config.json")
	// ProfileDir is the dedicated browser profile used for sign-in.
	ProfileDir = filepath.Join(Dir, "browser-profile")
)

// Load reads the settings file, returning a zero Config when absent.
func Load() Config {
	var cfg Config
	data, err := os.ReadFile(FilePath)
	if err != nil {
		return Config{}
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// Save writes the settings file with owner-only permissions.
func Save(cfg Config) error {
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	cfg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(FilePath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	_ = os.Chmod(FilePath, 0o600)
	return nil
}

// Update applies a mutation to the stored config and persists the result.
func Update(mutate func(*Config)) (Config, error) {
	cfg := Load()
	mutate(&cfg)
	if err := Save(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Clear removes the named fields. Known names are devToken, musicUserToken,
// storefront and mpvPath.
func Clear(keys ...string) error {
	cfg := Load()
	for _, key := range keys {
		switch key {
		case "devToken":
			cfg.DevToken, cfg.DevTokenExp = "", 0
		case "musicUserToken":
			cfg.MusicUserToken = ""
		case "storefront":
			cfg.Storefront = ""
		case "mpvPath":
			cfg.MpvPath = ""
		}
	}
	return Save(cfg)
}

// Reset deletes the settings file entirely.
func Reset() error {
	err := os.Remove(FilePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RemoveProfile deletes the browser profile, signing the user out of the
// captured session for good.
func RemoveProfile() error {
	err := os.RemoveAll(ProfileDir)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
