package lyrics

import (
	"errors"
	"testing"
	"time"
)

// The shape of a real answer: a song part, line timings, and the metadata the
// player ignores.
const lineTimed = `<?xml version="1.0" encoding="UTF-8"?>
<tt xmlns="http://www.w3.org/ns/ttml" xmlns:itunes="http://music.apple.com/lyric-ttml-internal" xmlns:ttm="http://www.w3.org/ns/ttml#meta" itunes:timing="Line" xml:lang="en">
  <head><metadata><ttm:agent type="person" xml:id="v1"><ttm:name type="full">Vocal 1</ttm:name></ttm:agent></metadata></head>
  <body dur="5:54.320">
    <div begin="0.000" end="50.701" itunes:songPart="Intro">
      <p begin="0.000" end="2.906" itunes:key="L1" ttm:agent="v1">Is this the real life?</p>
      <p begin="3.725" end="6.373" itunes:key="L2" ttm:agent="v1">Is this just fantasy?</p>
      <p begin="1:01.636" end="1:04.230" itunes:key="L3" ttm:agent="v1">Caught in a landslide &amp; no escape</p>
    </div>
  </body>
</tt>`

func TestParseLineTimedLyrics(t *testing.T) {
	doc, err := Parse([]byte(lineTimed))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(doc.Lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(doc.Lines))
	}
	if doc.Language != "en" {
		t.Errorf("language = %q, want en", doc.Language)
	}
	if !doc.Synced {
		t.Error("line-timed lyrics are not marked as synced")
	}

	if got, want := doc.Lines[0].Start, time.Duration(0); got != want {
		t.Errorf("first line starts at %v, want %v", got, want)
	}
	if got, want := doc.Lines[1].Start, 3725*time.Millisecond; got != want {
		t.Errorf("second line starts at %v, want %v", got, want)
	}
	if got, want := doc.Lines[2].Start, 61*time.Second+636*time.Millisecond; got != want {
		t.Errorf("minute timestamp = %v, want %v", got, want)
	}
	if got, want := doc.Lines[2].Text, "Caught in a landslide & no escape"; got != want {
		t.Errorf("entity was not decoded: %q, want %q", got, want)
	}
}

func TestLineAt(t *testing.T) {
	doc, err := Parse([]byte(lineTimed))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		at    time.Duration
		index int
	}{
		{-time.Second, -1},
		{0, 0},
		{2 * time.Second, 0},
		{3725 * time.Millisecond, 1},
		{7 * time.Second, 1}, // the gap keeps the last line up
		{62 * time.Second, 2},
		{10 * time.Minute, 2},
	}
	for _, c := range cases {
		if got := doc.LineAt(c.at); got != c.index {
			t.Errorf("LineAt(%v) = %d, want %d", c.at, got, c.index)
		}
	}
}

// Some songs carry the timing on every word instead of on the line. The words
// still form one line, and the line still has to start and end somewhere.
const wordTimed = `<tt xmlns="http://www.w3.org/ns/ttml" xmlns:itunes="http://music.apple.com/lyric-ttml-internal" itunes:timing="Word">
  <body><div>
    <p><span begin="1.000" end="1.400">Hello</span><span begin="1.400" end="2.000">world</span></p>
    <p><span begin="3.000" end="3.500">Goodbye</span></p>
  </div></body>
</tt>`

func TestParseWordTimedLyrics(t *testing.T) {
	doc, err := Parse([]byte(wordTimed))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(doc.Lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(doc.Lines))
	}
	first := doc.Lines[0]
	if first.Text != "Hello world" {
		t.Errorf("words were not joined: %q", first.Text)
	}
	if first.Start != time.Second || first.End != 2*time.Second {
		t.Errorf("line runs %v-%v, want 1s-2s", first.Start, first.End)
	}
	if doc.LineAt(1500*time.Millisecond) != 0 {
		t.Error("word-timed line was not found at 1.5s")
	}
}

func TestParseEmptyLyrics(t *testing.T) {
	_, err := Parse([]byte(`<tt><body><div></div></body></tt>`))
	if !errors.Is(err, ErrNone) {
		t.Fatalf("err = %v, want ErrNone", err)
	}
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"0.000", 0},
		{"2.906", 2906 * time.Millisecond},
		{"1:02.500", 62*time.Second + 500*time.Millisecond},
		{"1:02:03.250", time.Hour + 2*time.Minute + 3*time.Second + 250*time.Millisecond},
		{"", 0},
		{"not a time", 0},
	}
	for _, c := range cases {
		if got := parseTime(c.value); got != c.want {
			t.Errorf("parseTime(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}
