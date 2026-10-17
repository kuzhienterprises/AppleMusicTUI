package apple

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The web player keeps its developer token inside the minified JS bundle.
// Two patterns matter: where the bundle lives, and what the token looks like.
var (
	assetRegex = regexp.MustCompile(`/assets/(index~[A-Za-z0-9]+\.js)`)
	jwtRegex   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{30,}\.[A-Za-z0-9_-]{20,}`)
)

// DevToken is a developer token together with the moment it stops working.
type DevToken struct {
	Token     string
	ExpiresAt time.Time
}

// DecodeJWT returns the decoded header and payload of a compact JWS.
func DecodeJWT(token string) (header, payload map[string]any, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("not a JWT")
	}
	if header, err = decodeSegment(parts[0]); err != nil {
		return nil, nil, err
	}
	if payload, err = decodeSegment(parts[1]); err != nil {
		return nil, nil, err
	}
	return header, payload, nil
}

func decodeSegment(segment string) (map[string]any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// ScrapeDevToken pulls a fresh developer token out of the Apple Music web
// player. No Apple Developer account is required, but the token is bound to
// the web player's origin and rotates roughly every 70 days.
func ScrapeDevToken(ctx context.Context, storefront string) (*DevToken, error) {
	page, err := plainGet(ctx, fmt.Sprintf("https://music.apple.com/%s/browse", storefront))
	if err != nil {
		return nil, fmt.Errorf("fetching the Apple Music web player: %w", err)
	}

	match := assetRegex.FindStringSubmatch(page)
	if match == nil {
		return nil, fmt.Errorf("could not locate the Apple Music JS bundle; Apple may have changed the page layout")
	}

	bundle, err := plainGet(ctx, "https://music.apple.com/assets/"+match[1])
	if err != nil {
		return nil, fmt.Errorf("fetching the Apple Music JS bundle: %w", err)
	}

	seen := map[string]bool{}
	for _, candidate := range jwtRegex.FindAllString(bundle, -1) {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true

		header, payload, err := DecodeJWT(candidate)
		if err != nil {
			continue
		}
		// The bundle embeds several tokens; the catalogue token is the one
		// issued to "AMPWebPlay".
		if fmt.Sprint(header["alg"]) != "ES256" || fmt.Sprint(payload["iss"]) != "AMPWebPlay" {
			continue
		}
		exp, ok := payload["exp"].(float64)
		if !ok {
			continue
		}
		return &DevToken{Token: candidate, ExpiresAt: time.Unix(int64(exp), 0)}, nil
	}

	return nil, fmt.Errorf("no AMPWebPlay developer token found in the Apple Music JS bundle")
}

// RenewAfter reports the moment a cached token should be refreshed: a little
// before it actually expires, so requests never race the expiry.

func plainGet(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ChromeUA)
	req.Header.Set("Accept", "*/*")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// DetectStorefront works out the user's country so that results match what
// their subscription actually covers. music.apple.com redirects to a
// localised path such as /in/.
func DetectStorefront(ctx context.Context) string {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://music.apple.com/", nil)
	if err != nil {
		return "us"
	}
	req.Header.Set("User-Agent", ChromeUA)

	resp, err := client.Do(req)
	if err != nil {
		return "us"
	}
	defer resp.Body.Close()

	if location := resp.Header.Get("Location"); location != "" {
		if code := storefrontFromPath(location); code != "" {
			return code
		}
	}

	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			if m := regexp.MustCompile(`"storefront(?:Id)?"\s*:\s*"([a-z]{2})"`).FindSubmatch(body); m != nil {
				return string(m[1])
			}
		}
	}
	return "us"
}

func storefrontFromPath(location string) string {
	m := regexp.MustCompile(`music\.apple\.com/([a-z]{2})(?:/|$)`).FindStringSubmatch(location)
	if m == nil {
		return ""
	}
	return m[1]
}
