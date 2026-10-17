package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

func newStatusCommand() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current session and environment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg := config.Load()

			storefront := cfg.Storefront
			if storefront == "" {
				storefront = apple.DetectStorefront(ctx)
			}

			playback := "preview clips (30s) — run `am login` for full tracks"
			if cfg.MusicUserToken != "" {
				playback = "full-length, played by a hidden browser"
			}

			status := statusReport{
				Storefront:   storefront,
				SignedIn:     cfg.MusicUserToken != "",
				ConfigFile:   config.FilePath,
				PlaybackMode: playback,
			}

			if cfg.DevToken != "" {
				if _, payload, err := apple.DecodeJWT(cfg.DevToken); err == nil {
					if exp, ok := payload["exp"].(float64); ok {
						expiry := time.Unix(int64(exp), 0)
						status.DevTokenExpiry = expiry.Format(time.RFC3339)
						status.DevTokenDaysLeft = int(time.Until(expiry).Hours() / 24)
					}
				}
			}

			if status.SignedIn {
				session, err := auth.Connect(ctx, true)
				if err != nil {
					status.Warning = err.Error()
				} else if playlists, err := session.Client.LibraryPlaylists(ctx); err != nil {
					status.Warning = err.Error()
				} else {
					status.PlaylistCount = len(playlists)
				}
			}

			if mpvPath, err := player.FindMpv(); err != nil {
				status.MpvMissing = true
			} else {
				status.MpvPath = mpvPath
			}

			if asJSON {
				return printJSON(status)
			}

			ui.Info("")
			ui.Info("  Apple Music CLI")
			ui.Info("  ──────────────────────────────────────────")
			ui.Info("  storefront      %s", status.Storefront)
			ui.Info("  signed in       %s", yesNoLabel(status.SignedIn))
			if status.PlaylistCount > 0 {
				ui.Info("  playlists       %d", status.PlaylistCount)
			}
			if status.DevTokenDaysLeft != 0 {
				ui.Info("  developer token %d day(s) left (renewed automatically)", status.DevTokenDaysLeft)
			}
			if status.MpvPath != "" {
				ui.Info("  mpv             %s", status.MpvPath)
			} else {
				ui.Info("  mpv             missing — run `am doctor`")
			}
			ui.Info("  playback        %s", status.PlaybackMode)
			ui.Info("  config          %s", status.ConfigFile)
			if status.Warning != "" {
				ui.Info("")
				ui.Warn("%s", status.Warning)
			}
			ui.Info("")
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")

	return cmd
}

type statusReport struct {
	Storefront       string `json:"storefront"`
	SignedIn         bool   `json:"signedIn"`
	PlaylistCount    int    `json:"playlistCount,omitempty"`
	DevTokenExpiry   string `json:"devTokenExpiry,omitempty"`
	DevTokenDaysLeft int    `json:"devTokenDaysLeft,omitempty"`
	MpvPath          string `json:"mpvPath,omitempty"`
	MpvMissing       bool   `json:"mpvMissing,omitempty"`
	ConfigFile       string `json:"configFile"`
	PlaybackMode     string `json:"playbackMode"`
	Warning          string `json:"warning,omitempty"`
}

func yesNoLabel(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
