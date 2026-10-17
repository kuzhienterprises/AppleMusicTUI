package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/visualizer"
)

type checkResult struct {
	name     string
	ok       bool
	detail   string
	critical bool
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Verify that everything needed for playback is in place",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			results := runChecks(ctx)

			ui.Info("")
			failed := 0
			for _, result := range results {
				label := result.name
				if result.detail != "" {
					label += " — " + result.detail
				}
				if result.ok {
					ui.OK("%s", label)
					continue
				}
				ui.Error("%s", label)
				if result.critical {
					failed++
				}
			}
			ui.Info("")

			if failed > 0 {
				return fmt.Errorf("%d critical check(s) failed", failed)
			}
			ui.OK("Everything needed for search and playback is ready.")
			return nil
		},
	}
}

func runChecks(ctx context.Context) []checkResult {
	var results []checkResult

	// Settings directory
	if err := os.MkdirAll(config.Dir, 0o700); err != nil {
		results = append(results, checkResult{"settings directory is writable", false, err.Error(), true})
	} else {
		results = append(results, checkResult{"settings directory is writable", true, config.Dir, false})
	}

	// Developer token
	cfg := config.Load()
	token, err := apple.ScrapeDevToken(ctx, storefrontOr(ctx, cfg))
	if err != nil {
		results = append(results, checkResult{"developer token can be obtained", false, err.Error(), true})
	} else {
		results = append(results, checkResult{
			"developer token can be obtained", true,
			"valid until " + token.ExpiresAt.Format("2006-01-02"), true,
		})
	}

	// Session
	if cfg.MusicUserToken == "" {
		results = append(results, checkResult{
			"signed in to Apple Music", false,
			"not signed in — run `am login` for playlists", false,
		})
	} else if err == nil {
		storefront, verifyErr := auth.VerifyUserToken(ctx, token.Token, cfg.MusicUserToken)
		if verifyErr != nil {
			results = append(results, checkResult{"signed in to Apple Music", false, verifyErr.Error(), false})
		} else {
			results = append(results, checkResult{"signed in to Apple Music", true, "storefront " + storefront, false})
		}
	}

	// mpv
	if mpvPath, err := player.FindMpv(); err != nil {
		results = append(results, checkResult{
			"mpv is installed", false,
			"install with `winget install mpv-player.mpv-CI.MSVC`", true,
		})
	} else {
		results = append(results, checkResult{"mpv is installed", true, mpvVersion(mpvPath), true})
	}

	// Browser, for sign-in
	if browser, err := findBrowserForDoctor(); err != nil {
		results = append(results, checkResult{"a Chromium browser is available", false, err.Error(), false})
	} else {
		results = append(results, checkResult{"a Chromium browser is available", true, browser, false})
	}

	// Terminal capability
	if interactive() {
		results = append(results, checkResult{"interactive terminal detected", true, "full-screen interface will be used", false})
	} else {
		results = append(results, checkResult{
			"interactive terminal detected", false,
			"running without a terminal: playback prints to stdout", false,
		})
	}

	// Spectrum. A failure here is cosmetic, so it never fails the run: the
	// player falls back to a labelled animation.
	if monitor, err := visualizer.StartLoopback(); err != nil {
		results = append(results, checkResult{
			"spectrum can read the audio output", false,
			err.Error() + "; the player falls back to a labelled animation", false,
		})
	} else {
		monitor.Close()
		results = append(results, checkResult{
			"spectrum can read the audio output", true,
			"WASAPI loopback on the default output device", false,
		})
	}

	return results
}

func mpvVersion(path string) string {
	output, err := exec.Command(path, "--version").Output()
	if err != nil {
		return path
	}
	line := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)[0]
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return "mpv " + parts[1]
	}
	return line
}

func findBrowserForDoctor() (string, error) {
	// Reuse the login path: it already knows where Chromium lives.
	return auth.FindBrowser()
}

func storefrontOr(ctx context.Context, cfg config.Config) string {
	if cfg.Storefront != "" {
		return cfg.Storefront
	}
	return apple.DetectStorefront(ctx)
}
