// Package auth manages the two credentials the CLI needs: a developer token
// (scraped from the web player) and a music user token (captured from a
// browser sign-in).
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// Session bundles a ready-to-use API client with the storefront in effect.
type Session struct {
	Client     *apple.Client
	Storefront string
	UserToken  string
}

// ErrNotSignedIn is returned when a command needs personalised data but the
// user has not signed in yet.
var ErrNotSignedIn = errors.New("not signed in: run `am login` first")

// Connect resolves credentials, refreshing the developer token when needed.
// When requireUser is set, a missing music user token is an error.
func Connect(ctx context.Context, requireUser bool) (*Session, error) {
	cfg := config.Load()

	storefront := cfg.Storefront
	if storefront == "" {
		storefront = apple.DetectStorefront(ctx)
		if _, err := config.Update(func(c *config.Config) {
			if c.Storefront == "" {
				c.Storefront = storefront
			}
		}); err != nil {
			return nil, err
		}
		ui.Debug("detected storefront %q", storefront)
	}

	devToken, err := ensureDevToken(ctx, cfg, storefront)
	if err != nil {
		return nil, err
	}

	if requireUser && cfg.MusicUserToken == "" {
		return nil, ErrNotSignedIn
	}

	return &Session{
		Client:     apple.NewClient(devToken, cfg.MusicUserToken, storefront),
		Storefront: storefront,
		UserToken:  cfg.MusicUserToken,
	}, nil
}

func ensureDevToken(ctx context.Context, cfg config.Config, storefront string) (string, error) {
	// Refresh well before expiry: the token lasts about 70 days.
	const margin = 6 * time.Hour

	if cfg.DevToken != "" && time.Unix(cfg.DevTokenExp, 0).Add(-margin).After(time.Now()) {
		return cfg.DevToken, nil
	}

	ui.Debug("refreshing developer token from the Apple Music web player")
	token, err := apple.ScrapeDevToken(ctx, storefront)
	if err != nil {
		return "", err
	}
	if _, err := config.Update(func(c *config.Config) {
		c.DevToken = token.Token
		c.DevTokenExp = token.ExpiresAt.Unix()
	}); err != nil {
		return "", err
	}
	return token.Token, nil
}

// VerifyUserToken confirms the captured token actually works and returns the
// storefront Apple associates with the account.
func VerifyUserToken(ctx context.Context, devToken, userToken string) (string, error) {
	client := apple.NewClient(devToken, userToken, "us")

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.GetStorefront(ctx, &resp); err != nil {
		return "", fmt.Errorf("the captured session was rejected by Apple: %w", err)
	}
	if len(resp.Data) == 0 {
		return "", errors.New("no storefront returned for this account")
	}
	return resp.Data[0].ID, nil
}
