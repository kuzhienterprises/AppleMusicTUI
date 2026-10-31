package apple

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/lyrics"
)

const lyricsDocument = `{
  "data": [{
    "id": "1440806041",
    "type": "lyrics",
    "attributes": {
      "ttml": "\u003ctt xmlns=\"http://www.w3.org/ns/ttml\" itunes:timing=\"Line\"\u003e\u003cbody\u003e\u003cdiv\u003e\u003cp begin=\"0.000\" end=\"2.906\"\u003eIs this the real life?\u003c/p\u003e\u003c/div\u003e\u003c/body\u003e\u003c/tt\u003e"
    }
  }]
}`

// Lyrics are fetched from the player's hosts, with the same headers the
// catalogue uses: the origin-bound token is rejected without them.
func TestLyricsRequestShape(t *testing.T) {
	var gotPath, gotOrigin, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotOrigin = r.Header.Get("Origin")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(lyricsDocument))
	}))
	defer server.Close()

	client := NewClient("token", "", "in")
	client.LyricsHosts = []string{server.URL}

	doc, err := client.Lyrics(context.Background(), "1440806041")
	if err != nil {
		t.Fatalf("Lyrics: %v", err)
	}

	if gotPath != "/v1/catalog/in/songs/1440806041/lyrics" {
		t.Errorf("path = %q", gotPath)
	}
	if gotOrigin != WebOrigin {
		t.Errorf("origin = %q, want %q", gotOrigin, WebOrigin)
	}
	if gotAuth != "Bearer token" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if len(doc.Lines) != 1 || doc.Lines[0].Text != "Is this the real life?" {
		t.Fatalf("lyrics did not survive the round trip: %+v", doc.Lines)
	}
}

// One host refusing the request is not the end of it: the next one may hold
// the permission the first one lacks.
func TestLyricsFallsBackToTheNextHost(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"title":"Insufficient Permissions"}]}`))
	}))
	defer refusing.Close()

	serving := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(lyricsDocument))
	}))
	defer serving.Close()

	client := NewClient("token", "", "in")
	client.LyricsHosts = []string{refusing.URL, serving.URL}

	if _, err := client.Lyrics(context.Background(), "1440806041"); err != nil {
		t.Fatalf("the second host was never consulted: %v", err)
	}
}

func TestLyricsWithoutDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"attributes":{"ttml":""}}]}`))
	}))
	defer server.Close()

	client := NewClient("token", "", "us")
	client.LyricsHosts = []string{server.URL}

	if _, err := client.Lyrics(context.Background(), "1"); !errors.Is(err, lyrics.ErrNone) {
		t.Fatalf("err = %v, want lyrics.ErrNone", err)
	}
}

func TestLyricsWithoutASong(t *testing.T) {
	client := NewClient("token", "", "us")
	if _, err := client.Lyrics(context.Background(), ""); !errors.Is(err, lyrics.ErrNone) {
		t.Fatalf("err = %v, want lyrics.ErrNone", err)
	}
}

// A catalogue that has no words for the track answers 404 on every host, and
// that is the panel saying there are no lyrics.
func TestLyricsNotFoundEverywhere(t *testing.T) {
	missing := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"title":"Resource Not Found"}]}`))
		}))
	}
	first, second := missing(), missing()
	defer first.Close()
	defer second.Close()

	client := NewClient("token", "", "us")
	client.LyricsHosts = []string{first.URL, second.URL}

	if _, err := client.Lyrics(context.Background(), "1"); !errors.Is(err, lyrics.ErrNone) {
		t.Fatalf("err = %v, want lyrics.ErrNone", err)
	}
}

// A refusal is not the same answer: if the token has lost the permission, the
// track may well have words and the panel must not pretend otherwise.
func TestLyricsRefusedEverywhereIsAFailure(t *testing.T) {
	refusing := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"title":"Insufficient Permissions"}]}`))
		}))
	}
	first, second := refusing(), refusing()
	defer first.Close()
	defer second.Close()

	client := NewClient("token", "", "us")
	client.LyricsHosts = []string{first.URL, second.URL}

	_, err := client.Lyrics(context.Background(), "1")
	if err == nil {
		t.Fatal("a refusal was reported as success")
	}
	if errors.Is(err, lyrics.ErrNone) {
		t.Fatalf("a refusal was reported as a track without words: %v", err)
	}
}
