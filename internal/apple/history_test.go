package apple

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// recentServer serves the recent-played endpoint the way Apple does: 30 at a
// time, with offset paging, and a short final page.
func recentServer(t *testing.T, total int) (*httptest.Server, *[]string) {
	t.Helper()

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/recent/played/tracks" {
			t.Errorf("path = %q", r.URL.Path)
		}
		seen = append(seen, r.URL.RawQuery)

		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end := offset + limit
		if end > total {
			end = total
		}

		data := make([]map[string]any, 0, limit)
		for i := offset; i < end; i++ {
			data = append(data, map[string]any{
				"id":   strconv.Itoa(i),
				"type": "songs",
				"attributes": map[string]any{
					"name":             "Track " + strconv.Itoa(i),
					"artistName":       "Artist " + strconv.Itoa(i),
					"albumName":        "Album",
					"durationInMillis": 1000,
					"genreNames":       []string{"Pop"},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	return server, &seen
}

func TestRecentPlaysPagesThroughTheHistory(t *testing.T) {
	server, seen := recentServer(t, 42)
	defer server.Close()

	client := NewClient("token", "user", "us")
	client.BaseURL = server.URL

	songs, err := client.RecentPlays(context.Background(), 60)
	if err != nil {
		t.Fatalf("RecentPlays: %v", err)
	}

	// 42 is two pages, and the order has to survive the paging.
	if len(songs) != 42 {
		t.Fatalf("got %d songs, want 42", len(songs))
	}
	if songs[0].Name != "Track 0" || songs[41].Name != "Track 41" {
		t.Errorf("order was lost: first %q, last %q", songs[0].Name, songs[41].Name)
	}
	if songs[0].Artist != "Artist 0" || songs[0].Genres[0] != "Pop" {
		t.Errorf("attributes did not survive: %+v", songs[0])
	}
	if len(*seen) != 2 {
		t.Errorf("made %d requests, want 2: %v", len(*seen), *seen)
	}
}

func TestRecentPlaysStopsAtTheLimit(t *testing.T) {
	server, seen := recentServer(t, 42)
	defer server.Close()

	client := NewClient("token", "user", "us")
	client.BaseURL = server.URL

	songs, err := client.RecentPlays(context.Background(), 10)
	if err != nil {
		t.Fatalf("RecentPlays: %v", err)
	}
	if len(songs) != 10 {
		t.Fatalf("got %d songs, want 10", len(songs))
	}
	if len(*seen) != 1 {
		t.Errorf("a limit inside one page made %d requests", len(*seen))
	}
}

func TestRecentPlaysStopsOnAShortPage(t *testing.T) {
	server, seen := recentServer(t, 12)
	defer server.Close()

	client := NewClient("token", "user", "us")
	client.BaseURL = server.URL

	songs, err := client.RecentPlays(context.Background(), 90)
	if err != nil {
		t.Fatalf("RecentPlays: %v", err)
	}
	if len(songs) != 12 {
		t.Fatalf("got %d songs, want 12", len(songs))
	}
	if len(*seen) != 1 {
		t.Errorf("a short page was followed up: %v", *seen)
	}
}
