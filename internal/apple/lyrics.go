package apple

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/lyrics"
)

// DefaultLyricsHosts are the hosts the web player itself uses for lyrics.
//
// They are not api.music.apple.com. The developer token the player hands out
// does not carry the lyrics permission on the public catalogue host, which
// answers "Insufficient Permissions", but the same token is accepted by the
// hosts the player's own library and lyrics come from. Both are tried in
// order, so one host being unavailable is not the end of it.
var DefaultLyricsHosts = []string{
	"https://amp-api-edge.music.apple.com",
	"https://amp-api.music.apple.com",
}

// lyricsResponse is the shape of the lyrics resource: one document, whose TTML
// attribute holds the words and their timings.
type lyricsResponse struct {
	Data []struct {
		Attributes struct {
			TTML string `json:"ttml"`
		} `json:"attributes"`
	} `json:"data"`
}

// Lyrics fetches the timed lyrics for a catalogue song.
//
// Metadata tracks and previews have no lyrics. They come back as
// lyrics.ErrNone, which callers are expected to show as "no lyrics" rather
// than as a fault.
func (c *Client) Lyrics(ctx context.Context, songID string) (*lyrics.Doc, error) {
	songID = strings.TrimSpace(songID)
	if songID == "" || songID == "0" {
		return nil, lyrics.ErrNone
	}

	storefront := c.Storefront
	if storefront == "" {
		storefront = "us"
	}
	path := fmt.Sprintf("/v1/catalog/%s/songs/%s/lyrics", storefront, songID)

	// The first host that answers decides. A rejection is worth trying on the
	// next host, because the hosts disagree about permissions; a network
	// failure is not, and is returned as it happened.
	var (
		lastErr error
		missing = true // every host so far said there are no lyrics to give
	)
	for _, host := range c.LyricsHosts {
		var response lyricsResponse
		err := c.doAt(ctx, host, path, nil, &response)
		if err == nil {
			if len(response.Data) == 0 || strings.TrimSpace(response.Data[0].Attributes.TTML) == "" {
				return nil, lyrics.ErrNone
			}
			doc, err := lyrics.Parse([]byte(response.Data[0].Attributes.TTML))
			if err != nil {
				return nil, err
			}
			return doc, nil
		}

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			return nil, err
		}
		// A 404 is the catalogue saying the track has no words. A refusal is
		// a different thing - the token lost the permission, say - and must
		// not be reported as a song without lyrics.
		if apiErr.Status != http.StatusNotFound {
			missing = false
		}
		lastErr = err
	}

	if missing {
		return nil, lyrics.ErrNone
	}
	return nil, fmt.Errorf("reading the lyrics: %w", lastErr)
}
