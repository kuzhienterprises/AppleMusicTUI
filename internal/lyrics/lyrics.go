// Package lyrics turns Apple's timed lyrics into lines a terminal can scroll
// in time with the music.
//
// Apple serves lyrics as TTML: XML where each sung line is a paragraph with a
// start and an end. That is all this package needs, so it reads the timing and
// the words and ignores the rest of the format's vocabulary.
package lyrics

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Line is one sung line, and when it is sung.
type Line struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Doc is one track's lyrics.
type Doc struct {
	// Lines are in the order they are sung.
	Lines []Line
	// Language is the language the words are in, e.g. "en".
	Language string
	// Synced is true when the lines carry timings. Lyrics without timings
	// can still be read, but they cannot be followed.
	Synced bool
}

// ErrNone reports a track with no words to show. Apple has none for plenty
// of tracks, instrumentals most of all, and that is an answer rather than a
// failure: it is worth saying out loud instead of drawing a blank panel over.
var ErrNone = errors.New("no lyrics for this track")

// Parse reads the TTML document Apple returns for a song's lyrics.
func Parse(data []byte) (*Doc, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	doc := &Doc{}

	var (
		current  *Line
		text     strings.Builder
		timedP   bool
		timing   string
		spans    bool
		lastSpan time.Duration
	)

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the lyrics: %w", err)
		}

		switch element := token.(type) {
		case xml.StartElement:
			switch strings.ToLower(element.Name.Local) {
			case "tt":
				timing = attribute(element, "timing")
				doc.Language = attribute(element, "lang")
			case "p":
				current = &Line{
					Start: parseTime(attribute(element, "begin")),
					End:   parseTime(attribute(element, "end")),
				}
				timedP = attribute(element, "begin") != ""
				text.Reset()
			case "span":
				if current == nil {
					continue
				}
				spans = true
				// Word-timed lyrics put the timing on the spans instead of
				// on the paragraph. The first span opens the line and the
				// last one closes it.
				begin := parseTime(attribute(element, "begin"))
				end := parseTime(attribute(element, "end"))
				if !timedP && begin > 0 {
					current.Start = begin
					timedP = true
				}
				if end > 0 {
					lastSpan = end
				}
				if text.Len() > 0 {
					text.WriteString(" ")
				}
			}

		case xml.CharData:
			if current != nil {
				text.WriteString(string(element))
			}

		case xml.EndElement:
			if strings.ToLower(element.Name.Local) == "p" && current != nil {
				// Fields collapses the newlines and indentation the format
				// leaves between words, and the double spaces that some
				// background vocals arrive with.
				current.Text = strings.Join(strings.Fields(text.String()), " ")
				if spans && lastSpan > current.End {
					current.End = lastSpan
				}
				if current.Text != "" {
					doc.Lines = append(doc.Lines, *current)
				}
				current = nil
				spans = false
				lastSpan = 0
			}
		}
	}

	if len(doc.Lines) == 0 {
		return nil, ErrNone
	}

	// A document is timed if the format says so, or if the lines actually
	// carry times. Either is enough to scroll by.
	doc.Synced = timing != "" || doc.Lines[0].End > 0
	return doc, nil
}

// LineAt reports which line is being sung at the given moment, or -1 before
// the first one. A gap between lines keeps the previous line up: that is how
// the music reads, and clearing the panel between every phrase would flicker.
func (d *Doc) LineAt(elapsed time.Duration) int {
	if d == nil || len(d.Lines) == 0 {
		return -1
	}

	index := -1
	low, high := 0, len(d.Lines)-1
	for low <= high {
		middle := low + (high-low)/2
		if d.Lines[middle].Start <= elapsed {
			index = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return index
}

// attribute returns the value of an attribute by its local name, ignoring the
// namespace it was written with.
func attribute(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

// parseTime reads the timestamps TTML uses: seconds with a fraction, minutes
// and seconds, or hours, minutes and seconds.
func parseTime(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	parts := strings.Split(value, ":")
	seconds, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0
	}

	total := seconds
	for index := len(parts) - 2; index >= 0; index-- {
		part, err := strconv.ParseFloat(parts[index], 64)
		if err != nil {
			return 0
		}
		total += part * math.Pow(60, float64(len(parts)-1-index))
	}
	return time.Duration(total * float64(time.Second))
}
