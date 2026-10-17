package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/auth"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

func newLoginCommand() *cobra.Command {
	var manualToken string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Apple Music using your browser",
		Long: `Opens Chrome (or Edge) on the Apple Music sign-in page and captures the
session cookie once you have signed in. The session usually stays valid for
several months, and a dedicated browser profile is used so your everyday
browsing is never touched.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if manualToken != "" {
				return loginWithToken(ctx, manualToken)
			}

			result, err := auth.Login(ctx)
			if err != nil {
				return err
			}
			ui.OK("Signed in (storefront: %s)", result.Storefront)
			ui.Info("  credentials saved to %s", config.FilePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&manualToken, "token", "", "skip the browser and supply a music user token directly")

	return cmd
}

// loginWithToken stores a token the user obtained elsewhere, after checking
// that Apple accepts it.
func loginWithToken(ctx context.Context, token string) error {
	session, err := auth.Connect(ctx, false)
	if err != nil {
		return err
	}

	storefront, err := auth.VerifyUserToken(ctx, session.Client.DevToken, token)
	if err != nil {
		return err
	}

	if _, err := config.Update(func(c *config.Config) {
		c.MusicUserToken = token
		c.Storefront = storefront
	}); err != nil {
		return err
	}

	ui.OK("Token accepted (storefront: %s)", storefront)
	return nil
}

func newLogoutCommand() *cobra.Command {
	var purge bool

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the signed-in session",
		RunE: func(_ *cobra.Command, _ []string) error {
			if purge {
				if err := config.Reset(); err != nil {
					return err
				}
				if err := config.RemoveProfile(); err != nil {
					return err
				}
				ui.OK("Removed every credential and the browser profile")
				return nil
			}

			if err := config.Clear("musicUserToken"); err != nil {
				return err
			}
			ui.OK("Signed out; the developer token was kept")
			return nil
		},
	}

	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the browser profile and the developer token")

	return cmd
}
