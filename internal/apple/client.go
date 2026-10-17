// Package apple talks to the Apple Music catalogue and the user's library.
package apple

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	// ChromeUA identifies us the way the web player does. Some Apple
	// endpoints behave differently for unknown clients.
	ChromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// WebOrigin is the origin the scraped developer token is bound to.
	// Requests without it are rejected with 401.
	WebOrigin = "https://music.apple.com"
)

// APIError describes a non-2xx response from Apple.
type APIError struct {
	Status int
	Path   string
	Title  string
	Body   string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("Apple Music API returned %d for %s", e.Status, e.Path)
	if e.Title != "" {
		msg += ": " + e.Title
	}
	if e.IsAuth() {
		msg += "\n  hint: the session is no longer valid — run `am login`"
	}
	return msg
}

// IsAuth reports whether the failure was an authentication problem.
func (e *APIError) IsAuth() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Client performs authenticated requests against the Apple Music API.
type Client struct {
	HTTP       *http.Client
	BaseURL    string
	DevToken   string
	UserToken  string
	Storefront string
}

// NewClient builds a client. userToken may be empty for catalog-only use.
func NewClient(devToken, userToken, storefront string) *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		BaseURL:    "https://api.music.apple.com",
		DevToken:   devToken,
		UserToken:  userToken,
		Storefront: storefront,
	}
}

// HasUserToken reports whether the client can reach personalised endpoints.
func (c *Client) HasUserToken() bool { return c.UserToken != "" }

func (c *Client) do(ctx context.Context, path string, query url.Values, out any) error {
	requestURL := c.BaseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.DevToken)
	// The web player's developer token is origin-bound; omitting this header
	// yields a bare 401.
	req.Header.Set("Origin", WebOrigin)
	req.Header.Set("Referer", WebOrigin+"/")
	req.Header.Set("User-Agent", ChromeUA)
	req.Header.Set("Accept", "application/json")
	if c.UserToken != "" {
		req.Header.Set("Music-User-Token", c.UserToken)
		req.Header.Set("media-user-token", c.UserToken)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from %s failed: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Path: path, Body: string(body)}
		var parsed struct {
			Errors []struct {
				Title  string `json:"title"`
				Detail string `json:"detail"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &parsed) == nil && len(parsed.Errors) > 0 {
			apiErr.Title = parsed.Errors[0].Title
			if apiErr.Title == "" {
				apiErr.Title = parsed.Errors[0].Detail
			}
		}
		return apiErr
	}

	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response from %s failed: %w", path, err)
	}
	return nil
}
