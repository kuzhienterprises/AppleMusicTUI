package artwork

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestFillSubstitutesTheSizePlaceholders(t *testing.T) {
	got := fill("https://example.test/cover/{w}x{h}bb.{f}", 160)
	want := "https://example.test/cover/160x160bb.jpg"
	if got != want {
		t.Fatalf("fill = %q, want %q", got, want)
	}
}

func TestLoadRejectsAnEmptyTemplate(t *testing.T) {
	if _, err := Load(context.Background(), "   ", 20); err == nil {
		t.Fatal("expected an error for a track without artwork")
	}
}

func TestLoadRejectsANonPositiveSize(t *testing.T) {
	if _, err := Load(context.Background(), "https://example.test/{w}x{h}.jpg", 0); err == nil {
		t.Fatal("expected an error for a zero size")
	}
}

// Averaging has to actually average: a hard left/right split must survive as a
// hard split rather than being sampled from one side.
func TestScaleAveragesSourceBlocks(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			tone := uint8(0)
			if x >= 2 {
				tone = 255
			}
			source.Set(x, y, color.RGBA{R: tone, G: tone, B: tone, A: 255})
		}
	}

	scaled := scale(source, 2)
	if got := scaled.Bounds().Dx(); got != 2 {
		t.Fatalf("scaled width = %d, want 2", got)
	}

	left := scaled.RGBAAt(0, 0)
	right := scaled.RGBAAt(1, 0)

	if left.R != 0 {
		t.Errorf("left half = %v, want black", left)
	}
	if right.R != 255 {
		t.Errorf("right half = %v, want white", right)
	}

	// Both rows of the destination should match, since the source is uniform
	// vertically.
	if scaled.RGBAAt(0, 1) != left || scaled.RGBAAt(1, 1) != right {
		t.Error("rows disagrees; the vertical average is wrong")
	}
}

func TestScaleHandlesATinySource(t *testing.T) {
	// Upscaling a 1x1 source must not divide by zero.
	source := image.NewRGBA(image.Rect(0, 0, 1, 1))
	source.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})

	scaled := scale(source, 8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			got := scaled.RGBAAt(x, y)
			if got.R != 10 || got.G != 20 || got.B != 30 {
				t.Fatalf("pixel %d,%d = %v, want the source colour", x, y, got)
			}
		}
	}
}

func solid(width, height int, c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestDominantKeepsAColourfulCoverColourful(t *testing.T) {
	// A saturated red sleeve should produce a red accent.
	got := Dominant(solid(8, 8, color.RGBA{R: 220, G: 20, B: 30, A: 255}))
	if !strings.HasPrefix(got, "#") {
		t.Fatalf("Dominant = %q, want a hex colour", got)
	}

	red, green, blue := hexComponents(t, got)
	if red <= green || red <= blue {
		t.Errorf("Dominant = %q, expected red to lead for a red cover", got)
	}
}

// A monochrome sleeve still has to yield something usable as an accent rather
// than grey mush.
func TestDominantPushesGreyTowardsAVividTone(t *testing.T) {
	got := Dominant(solid(8, 8, color.RGBA{R: 128, G: 128, B: 128, A: 255}))
	red, green, blue := hexComponents(t, got)

	high, low := maxOf(red, green, blue), minOf(red, green, blue)
	if high-low < 40 {
		t.Errorf("Dominant = %q is too flat to use as an accent", got)
	}
}

func TestDominantHandlesNoImage(t *testing.T) {
	if got := Dominant(nil); got != "" {
		t.Fatalf("Dominant(nil) = %q, want an empty string", got)
	}
}

func hexComponents(t *testing.T, hex string) (uint8, uint8, uint8) {
	t.Helper()

	var red, green, blue uint32
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &red, &green, &blue); err != nil {
		t.Fatalf("could not parse %q: %v", hex, err)
	}
	return uint8(red), uint8(green), uint8(blue)
}

func maxOf(values ...uint8) uint8 {
	best := values[0]
	for _, v := range values[1:] {
		if v > best {
			best = v
		}
	}
	return best
}

func minOf(values ...uint8) uint8 {
	worst := values[0]
	for _, v := range values[1:] {
		if v < worst {
			worst = v
		}
	}
	return worst
}
