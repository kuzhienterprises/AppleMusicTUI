// Package tui holds the interactive parts of the CLI, built with Bubble Tea.
package tui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Apple-ish accent that reads well on both light and dark terminals.
	accent = lipgloss.AdaptiveColor{Light: "#b0223c", Dark: "#fa586a"}

	// The text hierarchy: a bright title, a medium artist, a dim album.
	titleText   = lipgloss.AdaptiveColor{Light: "#101010", Dark: "#ffffff"}
	muted       = lipgloss.AdaptiveColor{Light: "240", Dark: "250"}
	faint       = lipgloss.AdaptiveColor{Light: "247", Dark: "243"}
	ghost       = lipgloss.AdaptiveColor{Light: "252", Dark: "236"}
	placeholder = lipgloss.AdaptiveColor{Light: "253", Dark: "234"}

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(titleText)
	artistStyle = lipgloss.NewStyle().Foreground(muted)
	albumStyle  = lipgloss.NewStyle().Foreground(faint).Italic(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	helpStyle   = lipgloss.NewStyle().Foreground(faint)
	valueStyle  = lipgloss.NewStyle().Foreground(muted)
	dimStyle    = lipgloss.NewStyle().Foreground(ghost)

	// Keys carry the accent and their descriptions recede, so the footer can
	// be read as pairs rather than as a wall of text.
	keyStyle   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	labelStyle = lipgloss.NewStyle().Foreground(faint)

	// frameStyle turns the card into a window rather than a floating block.
	frameStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ghost).
			Padding(1, 2)

	cursorStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	pickerTitle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	selectedItem = lipgloss.NewStyle().Foreground(accent).Bold(true)

	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

const (
	// gradientPink and gradientWarm bound the spectrum's colour ramp. They are
	// mid-tones rather than the app accent so the ramp stays legible on a
	// light background as well as a dark one.
	gradientPink = "#ef4a6b"
	gradientWarm = "#f08a3c"

	// paletteSteps is how finely the ramp is quantised. Columns look up a
	// ready-made style instead of interpolating a colour every frame.
	paletteSteps = 24
)

// spectrumBlocks gives eight heights per row, so two rows give sixteen steps
// of vertical resolution.
var spectrumBlocks = []rune("▁▂▃▄▅▆▇█")

// defaultPalette is the pink-to-warm ramp used until a cover supplies its own
// colour.
var defaultPalette = buildPalette(gradientPink, gradientWarm, paletteSteps)

// buildPalette interpolates a ramp and wraps each step as a ready-made style.
func buildPalette(from, to string, steps int) []lipgloss.Style {
	if steps < 2 {
		steps = 2
	}

	palette := make([]lipgloss.Style, steps)
	for i := range palette {
		ratio := float64(i) / float64(steps-1)
		palette[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(lerpHex(from, to, ratio)))
	}
	return palette
}

// lerpHex blends two hex colours. Anything unparseable comes back as the
// starting colour rather than an error, because a gradient is not worth
// failing a frame over.
func lerpHex(from, to string, ratio float64) string {
	fromR, fromG, fromB := hexToRGB(from)
	toR, toG, toB := hexToRGB(to)

	blend := func(a, b int) int {
		return a + int(float64(b-a)*ratio+0.5)
	}
	return fmt.Sprintf("#%02x%02x%02x", blend(fromR, toR), blend(fromG, toG), blend(fromB, toB))
}

func hexToRGB(hex string) (int, int, int) {
	var red, green, blue uint32
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &red, &green, &blue); err != nil {
		return 0, 0, 0
	}
	return int(red), int(green), int(blue)
}

// renderArt draws a cover with "▀": the foreground colours the top half of a
// cell and the background the bottom half, so one text row carries two rows of
// pixels. The image is expected to be cols by rows*2 pixels.
func renderArt(cover image.Image, cols, rows int) []string {
	bounds := cover.Bounds()
	lines := make([]string, rows)

	for row := 0; row < rows; row++ {
		var line strings.Builder
		for column := 0; column < cols; column++ {
			top := pixelAt(cover, bounds.Min.X+column, bounds.Min.Y+2*row)
			bottom := pixelAt(cover, bounds.Min.X+column, bounds.Min.Y+2*row+1)

			line.WriteString(artCell(top, bottom).Render("▀"))
		}
		lines[row] = line.String()
	}
	return lines
}

// artCell turns two stacked pixels into one cell: the upper pixel becomes the
// foreground of "▀" and the lower one its background, so a single text row
// carries two rows of picture.
func artCell(top, bottom color.Color) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(hexOf(top))).
		Background(lipgloss.Color(hexOf(bottom)))
}

// pixelAt clamps to the image, so a cover that comes back a pixel short still
// draws.
func pixelAt(cover image.Image, x, y int) color.Color {
	bounds := cover.Bounds()
	if x >= bounds.Max.X {
		x = bounds.Max.X - 1
	}
	if y >= bounds.Max.Y {
		y = bounds.Max.Y - 1
	}
	if x < bounds.Min.X {
		x = bounds.Min.X
	}
	if y < bounds.Min.Y {
		y = bounds.Min.Y
	}
	return cover.At(x, y)
}

func hexOf(c color.Color) string {
	red, green, blue, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", uint8(red>>8), uint8(green>>8), uint8(blue>>8))
}

// artPlaceholder is the empty cover slot. It is the same size as the real
// thing, so the layout does not jump when the cover arrives.
func artPlaceholder(cols, rows int) []string {
	block := lipgloss.NewStyle().Background(placeholder).Render(strings.Repeat(" ", cols))

	lines := make([]string, rows)
	for i := range lines {
		lines[i] = block
	}
	return lines
}

// clip shortens text to fit a column width, measured the way the terminal
// will draw it rather than by rune count, so a title with wide characters
// cannot quietly push the layout off centre.
func clip(text string, width int) string {
	if width <= 1 {
		return text
	}
	if lipgloss.Width(text) <= width {
		return text
	}

	runes := []rune(text)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		if lipgloss.Width(string(runes)) <= width-1 {
			return string(runes) + "…"
		}
	}
	return "…"
}

// padRight pads text out to a column width, for the help panel's two columns.
func padRight(text string, width int) string {
	padding := width - lipgloss.Width(text)
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat(" ", padding)
}

// clock formats seconds as m:ss.
func clock(seconds float64) string {
	if seconds < 0 || seconds != seconds { // negative or NaN
		seconds = 0
	}
	total := int(seconds)
	return padNumber(total/60) + ":" + padNumber(total%60)
}

func padNumber(value int) string {
	if value < 10 {
		return "0" + itoa(value)
	}
	return itoa(value)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
