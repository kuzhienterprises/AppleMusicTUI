package cmd

import (
	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/player"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

func newDaemonCommand() *cobra.Command {
	var (
		headless bool
		stop     bool
	)

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Keep the hidden player warm between commands",
		Long: `Keeps a browser alive with Apple's player page loaded, so the next
command can attach to it instead of starting from scratch.

This is what makes the difference between waiting twenty seconds for the first
note and waiting a couple of seconds. It is normally started in the background
by the first command that needs it; running it by hand is useful when you
want the player warm before you do anything else.

The browser it keeps is Apple's own player, doing Apple's own DRM. Nothing
about what plays changes, only how long you wait for it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if stop {
				if err := player.StopDaemon(); err != nil {
					return err
				}
				ui.OK("The player daemon is stopping.")
				return nil
			}
			return player.RunDaemon(cmd.Context(), player.BrowserOptions{Headless: headless})
		},
	}

	cmd.Flags().BoolVar(&headless, "headless", false, "run the browser with no window at all")
	cmd.Flags().BoolVar(&stop, "stop", false, "stop a running player daemon")

	return cmd
}
