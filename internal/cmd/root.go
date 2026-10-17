// Package cmd wires the command-line interface.
package cmd

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

var (
	flagVerbose    bool
	flagPlain      bool
	flagStorefront string
	flagEngine     string
)

// version is what `am --version` reports. It is a variable rather than a
// constant so a release can set it without a source change:
//
//	go build -ldflags "-X github.com/kuzhienterprises/AppleMusicTUI/internal/cmd.version=v1.2.3"
var version = "dev"

// buildVersion is the version to report. Releases set it with the linker, but
// a binary built by
//
//	go install github.com/kuzhienterprises/AppleMusicTUI/cmd/am@latest
//
// has no linker flag and carries the version in its module metadata instead.
// Without this it would answer "dev" to somebody who asked for a release, and
// "dev" is what the bug template asks people to paste.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := newRootCommand().ExecuteContext(ctx); err != nil {
		ui.Error("%v", err)
		return 1
	}
	return 0
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "am",
		Short: "Apple Music in your terminal",
		Long: `am is an Apple Music client for the terminal.

It signs in through your own browser, reads your playlists, searches the Apple
Music catalogue, and plays audio without any visible window.

Full-length tracks are DRM encrypted, so the audio is played by Apple's own web
player running in a hidden browser; the terminal just drives it. Signed-out
sessions fall back to Apple's 30 second preview clips, which play through mpv.`,
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			ui.SetVerbose(flagVerbose)
			if flagStorefront != "" {
				if _, err := config.Update(func(c *config.Config) { c.Storefront = flagStorefront }); err != nil {
					return err
				}
			}
			return nil
		},
	}

	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "print diagnostic detail")
	root.PersistentFlags().BoolVar(&flagPlain, "plain", false, "never take over the terminal; print instead")
	root.PersistentFlags().StringVar(&flagStorefront, "storefront", "", "storefront country code, e.g. us or in")
	root.PersistentFlags().StringVar(&flagEngine, "engine", "auto", "playback engine: auto, full or preview")

	root.AddCommand(
		newLoginCommand(),
		newLogoutCommand(),
		newStatusCommand(),
		newDoctorCommand(),
		newSearchCommand(),
		newPlayCommand(),
		newPlaylistsCommand(),
		newPlaylistCommand(),
	)
	return root
}
