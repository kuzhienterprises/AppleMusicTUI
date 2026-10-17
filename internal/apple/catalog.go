package apple

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type songResource struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Attributes *songAttributes `json:"attributes"`
}

type songAttributes struct {
	Name             string `json:"name"`
	ArtistName       string `json:"artistName"`
	AlbumName        string `json:"albumName"`
	DurationInMillis int    `json:"durationInMillis"`
	Artwork          *struct {
		URL string `json:"url"`
	} `json:"artwork"`
	Previews []struct {
		URL string `json:"url"`
	} `json:"previews"`
	URL        string `json:"url"`
	ISRC       string `json:"isrc"`
	PlayParams *struct {
		ID        string `json:"id"`
		CatalogID string `json:"catalogId"`
	} `json:"playParams"`
}

func (r songResource) toSong() Song {
	song := Song{ID: r.ID, CatalogID: r.ID}
	if r.Attributes == nil {
		return song
	}
	a := r.Attributes
	song.Name = a.Name
	song.Artist = a.ArtistName
	song.Album = a.AlbumName
	song.DurationMs = a.DurationInMillis
	song.URL = a.URL
	song.ISRC = a.ISRC
	if a.Artwork != nil {
		song.Artwork = a.Artwork.URL
	}
	if len(a.Previews) > 0 {
		song.PreviewURL = a.Previews[0].URL
	}
	if a.PlayParams != nil && a.PlayParams.CatalogID != "" {
		song.CatalogID = a.PlayParams.CatalogID
	}
	return song
}

// SearchSongs finds catalogue tracks matching a free-text term.
func (c *Client) SearchSongs(ctx context.Context, term string, limit int) ([]Song, error) {
	query := url.Values{}
	query.Set("term", term)
	query.Set("types", "songs")
	query.Set("limit", strconv.Itoa(limit))

	var resp struct {
		Results struct {
			Songs struct {
				Data []songResource `json:"data"`
			} `json:"songs"`
		} `json:"results"`
	}
	if err := c.do(ctx, fmt.Sprintf("/v1/catalog/%s/search", c.Storefront), query, &resp); err != nil {
		return nil, err
	}
	return mapSongs(resp.Results.Songs.Data), nil
}

// SearchPlaylists finds catalogue playlists matching a free-text term.
func (c *Client) SearchPlaylists(ctx context.Context, term string, limit int) ([]Playlist, error) {
	query := url.Values{}
	query.Set("term", term)
	query.Set("types", "playlists")
	query.Set("limit", strconv.Itoa(limit))

	var resp struct {
		Results struct {
			Playlists struct {
				Data []playlistResource `json:"data"`
			} `json:"playlists"`
		} `json:"results"`
	}
	if err := c.do(ctx, fmt.Sprintf("/v1/catalog/%s/search", c.Storefront), query, &resp); err != nil {
		return nil, err
	}
	playlists := make([]Playlist, 0, len(resp.Results.Playlists.Data))
	for _, item := range resp.Results.Playlists.Data {
		playlists = append(playlists, item.toPlaylist(false))
	}
	return playlists, nil
}

// Song fetches a single catalogue track.
func (c *Client) Song(ctx context.Context, id string) (Song, error) {
	var resp struct {
		Data []songResource `json:"data"`
	}
	if err := c.do(ctx, fmt.Sprintf("/v1/catalog/%s/songs/%s", c.Storefront, id), nil, &resp); err != nil {
		return Song{}, err
	}
	if len(resp.Data) == 0 {
		return Song{}, fmt.Errorf("no song with catalogue id %s", id)
	}
	return resp.Data[0].toSong(), nil
}

// SongsByIDs batch-fetches catalogue tracks. It exists mainly to enrich
// library tracks, which do not carry preview URLs of their own.
func (c *Client) SongsByIDs(ctx context.Context, ids []string) (map[string]Song, error) {
	result := make(map[string]Song, len(ids))
	const batchSize = 100
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		query := url.Values{}
		query.Set("ids", strings.Join(ids[start:end], ","))

		var resp struct {
			Data []songResource `json:"data"`
		}
		if err := c.do(ctx, fmt.Sprintf("/v1/catalog/%s/songs", c.Storefront), query, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data {
			result[item.ID] = item.toSong()
		}
	}
	return result, nil
}

// CatalogPlaylistTracks returns the tracks of a catalogue playlist.
func (c *Client) CatalogPlaylistTracks(ctx context.Context, id string) ([]Song, error) {
	return c.collectSongs(ctx, fmt.Sprintf("/v1/catalog/%s/playlists/%s/tracks", c.Storefront, id), url.Values{"limit": {"100"}})
}

// collectSongs walks a paginated song collection (catalogue or library).
func (c *Client) collectSongs(ctx context.Context, path string, query url.Values) ([]Song, error) {
	var songs []Song
	for path != "" {
		var resp struct {
			Data []songResource `json:"data"`
			Next string         `json:"next"`
		}
		if err := c.do(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		songs = append(songs, mapSongs(resp.Data)...)
		// `next` already embeds the query string.
		path, query = resp.Next, nil
	}
	return songs, nil
}

func mapSongs(resources []songResource) []Song {
	songs := make([]Song, 0, len(resources))
	for _, resource := range resources {
		if resource.Type != "" && !strings.Contains(resource.Type, "song") {
			continue
		}
		song := resource.toSong()
		if song.Name == "" {
			continue
		}
		songs = append(songs, song)
	}
	return songs
}
