package apple

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type playlistResource struct {
	ID         string `json:"id"`
	Attributes *struct {
		Name        string          `json:"name"`
		Description json.RawMessage `json:"description"`
		Artwork     *struct {
			URL string `json:"url"`
		} `json:"artwork"`
		TrackCount int `json:"trackCount"`
	} `json:"attributes"`
}

func (r playlistResource) toPlaylist(isLibrary bool) Playlist {
	playlist := Playlist{ID: r.ID, IsLibrary: isLibrary}
	if r.Attributes == nil {
		return playlist
	}
	playlist.Name = r.Attributes.Name
	playlist.Description = cleanDescription(parseDescription(r.Attributes.Description))
	playlist.TrackCount = r.Attributes.TrackCount
	if r.Attributes.Artwork != nil {
		playlist.Artwork = r.Attributes.Artwork.URL
	}
	return playlist
}

// parseDescription copes with both shapes Apple uses: a plain string for
// library playlists, and an object with "standard"/"short" for catalogue ones.
func parseDescription(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var object struct {
		Standard string `json:"standard"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return object.Standard
	}
	return ""
}

// LibraryPlaylists lists every playlist in the user's library.
func (c *Client) LibraryPlaylists(ctx context.Context) ([]Playlist, error) {
	var playlists []Playlist
	path := "/v1/me/library/playlists"
	query := url.Values{"limit": {"100"}}

	for path != "" {
		var resp struct {
			Data []playlistResource `json:"data"`
			Next string             `json:"next"`
		}
		if err := c.do(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data {
			playlists = append(playlists, item.toPlaylist(true))
		}
		path, query = resp.Next, nil
	}

	sort.SliceStable(playlists, func(i, j int) bool {
		return strings.ToLower(playlists[i].Name) < strings.ToLower(playlists[j].Name)
	})
	return playlists, nil
}

// LibraryPlaylistTracks returns the tracks of a library playlist, enriched
// with catalogue data so that previews are available.
func (c *Client) LibraryPlaylistTracks(ctx context.Context, playlistID string) ([]Song, error) {
	songs, err := c.collectSongs(ctx, fmt.Sprintf("/v1/me/library/playlists/%s/tracks", playlistID), url.Values{"limit": {"100"}})
	if err != nil {
		return nil, err
	}
	return c.Enrich(ctx, songs), nil
}

// Enrich fills in preview URLs and artwork for library tracks by looking them
// up in the catalogue. Failures are tolerated: the track simply stays
// unplayable rather than failing the whole listing.
func (c *Client) Enrich(ctx context.Context, songs []Song) []Song {
	var ids []string
	for _, song := range songs {
		if song.PreviewURL == "" && song.CatalogID != "" {
			ids = append(ids, song.CatalogID)
		}
	}
	if len(ids) == 0 {
		return songs
	}

	catalog, err := c.SongsByIDs(ctx, ids)
	if err != nil {
		return songs
	}

	for i := range songs {
		if songs[i].PreviewURL != "" {
			continue
		}
		extra, ok := catalog[songs[i].CatalogID]
		if !ok {
			continue
		}
		songs[i].PreviewURL = extra.PreviewURL
		if songs[i].Artwork == "" {
			songs[i].Artwork = extra.Artwork
		}
		if songs[i].DurationMs == 0 {
			songs[i].DurationMs = extra.DurationMs
		}
		if songs[i].ISRC == "" {
			songs[i].ISRC = extra.ISRC
		}
	}
	return songs
}

// FindLibraryPlaylist resolves a playlist by exact id or by a
// case-insensitive name match.
func (c *Client) FindLibraryPlaylist(ctx context.Context, nameOrID string) (Playlist, error) {
	playlists, err := c.LibraryPlaylists(ctx)
	if err != nil {
		return Playlist{}, err
	}

	for _, playlist := range playlists {
		if playlist.ID == nameOrID {
			return playlist, nil
		}
	}
	for _, playlist := range playlists {
		if strings.EqualFold(playlist.Name, nameOrID) {
			return playlist, nil
		}
	}
	for _, playlist := range playlists {
		if strings.Contains(strings.ToLower(playlist.Name), strings.ToLower(nameOrID)) {
			return playlist, nil
		}
	}
	return Playlist{}, fmt.Errorf("no library playlist matching %q", nameOrID)
}
