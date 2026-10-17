// Command shots renders the interface's frames into the images in the README.
//
// The interface writes its frames as ANSI text (see TestShotFrames in
// internal/tui). This turns that text into a page that looks like a terminal and
// screenshots it with headless Chrome.
//
// Two details are worth knowing:
//
//   - Every cell is pinned to one character of the grid. A glyph the font does
//     not carry — a note, a transport arrow, a tick — then falls back to another
//     family without pulling the rest of the line out of alignment.
//
//   - Cells are only merged into a run when they are all spaces, which keeps the
//     markup small without giving up that guarantee.
//
//     go run ./spikes/shots -out G:/tmp/shots -font C:/path/CascadiaMono.ttf frame.ansi
package main

import (
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Cascadia Mono's own metrics, in ems. The grid is built on these rather than
// on round numbers: the interface draws its frame with box characters and its
// spectrum with block characters, and those only join into unbroken lines if a
// cell is exactly as wide and as tall as the glyphs are drawn.
const (
	cascadiaAdvance    = 1200.0 / 2048.0
	cascadiaCellHeight = 2380.0 / 2048.0
)

func main() {
	var (
		outDir     = flag.String("out", "shots", "directory to write the pages and images into")
		font       = flag.String("font", "", "path to a monospace font file")
		chrome     = flag.String("chrome", `C:\Program Files\Google\Chrome\Application\chrome.exe`, "chrome executable")
		scale      = flag.Int("scale", 2, "device scale factor for the screenshot")
		size       = flag.Float64("font-size", 16, "font size in css pixels")
		cellWidth  = flag.Float64("cell-width", cascadiaAdvance, "the font's advance, in ems")
		cellHeight = flag.Float64("cell-height", cascadiaCellHeight, "the font's cell height, in ems")
		pad        = flag.String("pad", "22px 26px", "padding inside the card")
		bg         = flag.String("bg", "#15151c", "background around the card")
		term       = flag.String("term-bg", "#0c0c0f", "the terminal's own background")
	)
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: shots [flags] frame.ansi ...")
		os.Exit(2)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal(err)
	}

	settings := options{
		font: *font, size: *size, cellWidth: *cellWidth, cellHeight: *cellHeight,
		pad: *pad, bg: *bg, termBg: *term,
	}

	for _, path := range flag.Args() {
		raw, err := os.ReadFile(path)
		if err != nil {
			fatal(err)
		}

		rows := trim(parse(string(raw)))
		if len(rows) == 0 {
			fatal(fmt.Errorf("%s has nothing to draw", path))
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

		page := filepath.Join(*outDir, name+".html")
		image := filepath.Join(*outDir, name+".png")

		if err := os.WriteFile(page, []byte(render(rows, settings)), 0o644); err != nil {
			fatal(err)
		}

		width := int(float64(widest(rows))*settings.cell()) + 200
		height := int(float64(len(rows))*settings.row()) + 240
		if err := shoot(*chrome, *outDir, page, image, width, height, *scale); err != nil {
			fatal(err)
		}
		fmt.Printf("%s → %s (%d rows)\n", filepath.Base(path), filepath.Base(image), len(rows))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "shots:", err)
	os.Exit(1)
}

// options is everything the page needs to look like a terminal.
type options struct {
	font       string
	size       float64
	cellWidth  float64
	cellHeight float64
	pad        string
	bg         string
	termBg     string
}

// cell is how wide one character is, and row how tall it is.
func (o options) cell() float64 { return o.size * o.cellWidth }
func (o options) row() float64  { return o.size * o.cellHeight }

// style is the part of a cell the terminal would have drawn with.
type style struct {
	fg   string
	bg   string
	bold bool
}

type cell struct {
	text string
	style
}

// parse walks ANSI text into rows of cells. Only what the interface actually
// emits is understood: colour, bold, and their resets.
func parse(text string) [][]cell {
	var (
		rows  [][]cell
		row   []cell
		state style
	)

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; {
		case r == '\x1b':
			// Consume the whole sequence. CSI runs from the bracket to its
			// final byte, which is the first one in @ through ~.
			j := i + 1
			if j < len(runes) && runes[j] == '[' {
				j++
				start := j
				for j < len(runes) && (runes[j] < 0x40 || runes[j] > 0x7e) {
					j++
				}
				if j < len(runes) && runes[j] == 'm' {
					applySGR(string(runes[start:j]), &state)
				}
			}
			i = j

		case r == '\n':
			rows = append(rows, row)
			row = nil
		case r == '\r', r == '\x07':
			// Carriage returns and bells carry nothing to draw.

		default:
			row = append(row, cell{text: string(r), style: state})
		}
	}
	return append(rows, row)
}

// applySGR reads the parameters of one Select Graphic Rendition sequence.
func applySGR(params string, state *style) {
	if params == "" {
		params = "0"
	}

	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		value, err := strconv.Atoi(strings.TrimSpace(fields[i]))
		if err != nil {
			continue
		}

		switch {
		case value == 0:
			*state = style{}
		case value == 1:
			state.bold = true
		case value == 22:
			state.bold = false
		case value == 39:
			state.fg = ""
		case value == 49:
			state.bg = ""
		case value == 38 || value == 48:
			colour, used, ok := readColour(fields[i+1:])
			if !ok {
				break
			}
			if value == 38 {
				state.fg = colour
			} else {
				state.bg = colour
			}
			i += used
		}
	}
}

// readColour reads an extended colour specification: either "5;n" for the 256
// colour table or "2;r;g;b" for direct colour. It reports how many fields it
// consumed.
func readColour(fields []string) (string, int, bool) {
	if len(fields) == 0 {
		return "", 0, false
	}

	switch fields[0] {
	case "5":
		if len(fields) < 2 {
			return "", 0, false
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return "", 0, false
		}
		return hex256(n), 2, true

	case "2":
		if len(fields) < 4 {
			return "", 0, false
		}
		channels := make([]int, 3)
		for i := range channels {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil {
				return "", 0, false
			}
			channels[i] = n
		}
		return fmt.Sprintf("#%02x%02x%02x", channels[0], channels[1], channels[2]), 4, true
	}
	return "", 0, false
}

// hex256 turns an index from the xterm palette into a colour. Nothing in the
// interface needs this — lipgloss resolves over a direct colour profile — but a
// frame rendered by another tool would, and a wrong colour beats a panic.
func hex256(n int) string {
	switch {
	case n < 16:
		system := [...]string{
			"#000000", "#cd0000", "#00cd00", "#cdcd00", "#0000ee", "#cd00cd", "#00cdcd", "#e5e5e5",
			"#7f7f7f", "#ff0000", "#00ff00", "#ffff00", "#5c5cff", "#ff00ff", "#00ffff", "#ffffff",
		}
		return system[n]
	case n < 232:
		n -= 16
		steps := [...]int{0, 95, 135, 175, 215, 255}
		return fmt.Sprintf("#%02x%02x%02x", steps[n/36], steps[n/6%6], steps[n%6])
	default:
		level := 8 + (n-232)*10
		if level > 255 {
			level = 255
		}
		return fmt.Sprintf("#%02x%02x%02x", level, level, level)
	}
}

// blank reports whether a cell draws nothing: a space with no colour behind it.
func blank(c cell) bool { return c.text == " " && c.bg == "" }

// trim drops the blank rows and columns around the frame. The interface is
// centred in the terminal it was drawn for, and that margin is not part of the
// picture.
func trim(rows [][]cell) [][]cell {
	first, last := 0, len(rows)-1
	for first <= last && blankRow(rows[first]) {
		first++
	}
	for last >= first && blankRow(rows[last]) {
		last--
	}
	if first > last {
		return nil
	}
	rows = rows[first : last+1]

	left, right := -1, 0
	for _, row := range rows {
		start, end := 0, len(row)
		for start < end && blank(row[start]) {
			start++
		}
		for end > start && blank(row[end-1]) {
			end--
		}
		if start == end {
			continue
		}
		if left < 0 || start < left {
			left = start
		}
		if end > right {
			right = end
		}
	}
	if left < 0 {
		return nil
	}

	trimmed := make([][]cell, 0, len(rows))
	for _, row := range rows {
		if left >= len(row) {
			trimmed = append(trimmed, nil)
			continue
		}
		end := min(right, len(row))
		trimmed = append(trimmed, row[left:end])
	}
	return trimmed
}

func blankRow(row []cell) bool {
	for _, c := range row {
		if !blank(c) {
			return false
		}
	}
	return true
}

// widest is the longest row, which is what the page has to be able to hold.
func widest(rows [][]cell) int {
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	return width
}

func (s style) css() string {
	var parts []string
	if s.fg != "" {
		parts = append(parts, "color:"+s.fg)
	}
	if s.bg != "" {
		parts = append(parts, "background:"+s.bg)
	}
	if s.bold {
		parts = append(parts, "font-weight:600")
	}
	return strings.Join(parts, ";")
}

// render writes the page. Rows are blocks with a fixed height and cells are
// inline blocks one character wide, so nothing the font does can move a glyph
// off its square.
func render(rows [][]cell, o options) string {
	var b strings.Builder

	font := ""
	if o.font != "" {
		absolute, err := filepath.Abs(o.font)
		if err == nil {
			font = "file:///" + filepath.ToSlash(absolute)
		}
	}

	b.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n<title>am</title>\n<style>\n")
	if font != "" {
		fmt.Fprintf(&b, "@font-face{font-family:ShotMono;src:url(%q) format(\"truetype\");font-weight:100 900}\n", font)
	}
	fmt.Fprintf(&b, ":root{color-scheme:dark}\n")
	fmt.Fprintf(&b, "html,body{margin:0;height:100%%}\n")
	fmt.Fprintf(&b, "body{background:%s;display:flex;align-items:center;justify-content:center;"+
		"font-family:ShotMono,Consolas,monospace;-webkit-font-smoothing:antialiased;text-rendering:geometricPrecision}\n", o.bg)
	fmt.Fprintf(&b, ".card{background:%s;border-radius:12px;padding:%s;box-shadow:inset 0 0 0 1px rgba(255,255,255,.06)}\n", o.termBg, o.pad)
	fmt.Fprintf(&b, ".row{white-space:pre;height:%.6fem;line-height:%.6fem;font-size:%.0fpx}\n", o.cellHeight, o.cellHeight, o.size)
	fmt.Fprintf(&b, ".c{display:inline-block;width:%.6fem;text-align:center}\n", o.cellWidth)
	b.WriteString("</style>\n<div class=\"card\">\n")

	for _, row := range rows {
		b.WriteString("<div class=\"row\">")
		for i := 0; i < len(row); {
			// Runs of spaces are the bulk of every frame, and they carry no
			// glyph that could need a grid of its own.
			if row[i].text == " " {
				j := i
				for j < len(row) && row[j].text == " " && row[j].style == row[i].style {
					j++
				}
				fmt.Fprintf(&b, "<span class=\"c\" style=\"%s;width:%.6fem\">%s</span>",
					row[i].style.css(), o.cellWidth*float64(j-i), strings.Repeat(" ", j-i))
				i = j
				continue
			}

			fmt.Fprintf(&b, "<span class=\"c\" style=\"%s\">%s</span>",
				row[i].style.css(), html.EscapeString(row[i].text))
			i++
		}
		b.WriteString("</div>\n")
	}

	b.WriteString("</div>\n")
	return b.String()
}

// shoot screenshots the page. Chrome gets a profile of its own: with the user's
// profile it would hand the request to the browser they already have open and
// write nothing.
func shoot(chrome, profileDir, page, image string, width, height, scale int) error {
	absolute, err := filepath.Abs(image)
	if err != nil {
		return err
	}

	command := exec.Command(chrome,
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--no-first-run",
		"--no-default-browser-check",
		"--allow-file-access-from-files",
		"--user-data-dir="+filepath.Join(profileDir, "chrome-profile"),
		"--force-device-scale-factor="+strconv.Itoa(scale),
		"--window-size="+strconv.Itoa(width)+","+strconv.Itoa(height),
		"--virtual-time-budget=5000",
		"--screenshot="+absolute,
		"file:///"+filepath.ToSlash(mustAbs(page)),
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("chrome: %w: %s", err, output)
	}
	return nil
}

func mustAbs(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}
