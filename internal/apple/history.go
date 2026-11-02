package apple

import (
	"context"
	"net/url"
	"strconv"
)

// recentPageSize is the most the recent-played endpoint will serve at once.
// Asking for more is rejected with a 400, so paging is the only way through a
// longer history.
const recentPageSize = 30

// RecentPlays returns the tracks the account has played recently, newest
// first.
//
// This is the account's own history, not this terminal's: Apple keeps it
// across devices, so it says more about what somebody listens to than anything
// we could record locally. It is capped at 30 per request and the rest is
// paged, which is why a limit above 30 costs several requests.
func (c *Client) RecentPlays(ctx context.Context, limit int) ([]Song, error) {
	if limit <= 0 {
		limit = recentPageSize
	}

	var songs []Song
	for offset := 0; len(songs) < limit; offset += recentPageSize {
		var response struct {
			Data []songResource `json:"data"`
		}
		query := url.Values{
			"limit":  {strconv.Itoa(recentPageSize)},
			"offset": {strconv.Itoa(offset)},
		}
		if err := c.do(ctx, "/v1/me/recent/played/tracks", query, &response); err != nil {
			return nil, err
		}

		// An empty page is the end of the history, and a short one is the
		// last page: either way there is nothing after it to ask for.
		if len(response.Data) == 0 {
			break
		}
		for _, item := range response.Data {
			songs = append(songs, item.toSong())
		}
		if len(response.Data) < recentPageSize {
			break
		}
	}

	if len(songs) > limit {
		songs = songs[:limit]
	}
	return songs, nil
}
