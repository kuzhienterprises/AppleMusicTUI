// Package artwork downloads cover images and reduces them to something a
// terminal can actually draw.
//
// Apple hands out artwork as a URL template carrying {w} and {h} placeholders,
// so asking for a small square is a matter of substitution. The image that
// comes back is far larger than a terminal cell grid can show, so it is
// averaged down rather than sampled: nearest-neighbour on a 20x20 grid looks
// like static.
package artwork

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// requestSize is the pixel size asked of Apple's CDN. It is well above
	// what a terminal can show, which leaves plenty to average down from.
	requestSize = 160

	// maxBytes caps a download, so a redirect cannot pull in something huge
	// just because a response claims to be an image.
	maxBytes = 8 << 20
)

// client is shared so the connection to Apple's image CDN is reused across
// track changes.
var client = &http.Client{Timeout: 15 * time.Second}

// The cache holds decoded, scaled covers keyed by URL and size, so walking
// back and forth through a queue never re-downloads anything.
var (
	cacheMu sync.Mutex
	cache   = map[string]image.Image{}
)

// Load downloads the cover described by an Apple artwork template and scales
// it to a size x size square.
func Load(ctx context.Context, template string, size int) (image.Image, error) {
	if strings.TrimSpace(template) == "" {
		return nil, errors.New("this track has no artwork")
	}
	if size <= 0 {
		return nil, fmt.Errorf("artwork size must be positive, got %d", size)
	}

	url := fill(template, requestSize)
	key := url + "|" + strconv.Itoa(size)

	cacheMu.Lock()
	cached, ok := cache[key]
	cacheMu.Unlock()
	if ok {
		return cached, nil
	}

	source, err := download(ctx, url)
	if err != nil {
		return nil, err
	}

	scaled := scale(source, size)

	cacheMu.Lock()
	cache[key] = scaled
	cacheMu.Unlock()

	return scaled, nil
}

// fill substitutes the size placeholders. {f} is the format extension, which
// some templates carry and some do not.
func fill(template string, size int) string {
	pixels := strconv.Itoa(size)
	return strings.NewReplacer(
		"{w}", pixels,
		"{h}", pixels,
		"{f}", "jpg",
	).Replace(template)
}

func download(ctx context.Context, url string) (image.Image, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "applemusic-cli")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("requesting artwork: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artwork returned %s", response.Status)
	}

	decoded, _, err := image.Decode(io.LimitReader(response.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("decoding artwork: %w", err)
	}
	return decoded, nil
}

// scale averages the source down to size x size. Averaging the whole source
// block behind each destination pixel keeps fine detail from aliasing into
// noise, which a box filter does better than a sampling one for large
// reductions like this.
func scale(source image.Image, size int) *image.RGBA {
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth == 0 || sourceHeight == 0 {
		return image.NewRGBA(image.Rect(0, 0, size, size))
	}

	target := image.NewRGBA(image.Rect(0, 0, size, size))

	for y := 0; y < size; y++ {
		top := bounds.Min.Y + y*sourceHeight/size
		bottom := bounds.Min.Y + (y+1)*sourceHeight/size
		if bottom <= top {
			bottom = top + 1
		}

		for x := 0; x < size; x++ {
			left := bounds.Min.X + x*sourceWidth/size
			right := bounds.Min.X + (x+1)*sourceWidth/size
			if right <= left {
				right = left + 1
			}

			var red, green, blue, count uint64
			for py := top; py < bottom; py++ {
				for px := left; px < right; px++ {
					r, g, b, _ := source.At(px, py).RGBA()
					red += uint64(r)
					green += uint64(g)
					blue += uint64(b)
					count++
				}
			}
			if count == 0 {
				continue
			}

			target.SetRGBA(x, y, color.RGBA{
				R: uint8(red / count >> 8),
				G: uint8(green / count >> 8),
				B: uint8(blue / count >> 8),
				A: 255,
			})
		}
	}

	return target
}

// Dominant returns a hex colour for the cover, usable as an accent.
//
// Averaging a whole cover mutes it badly — a sleeve that is mostly paper ends
// up beige. So only reasonably colourful, reasonably bright pixels vote, and
// the winner is pushed back towards a vivid tone. A genuinely monochrome cover
// still produces something readable rather than grey mush.
func Dominant(img image.Image) string {
	if img == nil {
		return ""
	}

	bounds := img.Bounds()
	var (
		vivid       [3]uint64
		vividCount  uint64
		average     [3]uint64
		averageSize uint64
	)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			rf, gf, bf := float64(r)/65535, float64(g)/65535, float64(b)/65535

			average[0] += uint64(r)
			average[1] += uint64(g)
			average[2] += uint64(b)
			averageSize++

			if _, saturation, value := rgbToHSV(rf, gf, bf); saturation > 0.25 && value > 0.15 {
				vivid[0] += uint64(r)
				vivid[1] += uint64(g)
				vivid[2] += uint64(b)
				vividCount++
			}
		}
	}

	if averageSize == 0 {
		return ""
	}

	red, green, blue := average[0], average[1], average[2]
	if vividCount > 0 {
		red, green, blue = vivid[0]/vividCount, vivid[1]/vividCount, vivid[2]/vividCount
	}

	hue, saturation, value := rgbToHSV(
		float64(red)/65535,
		float64(green)/65535,
		float64(blue)/65535,
	)
	if saturation < 0.5 {
		saturation = 0.5
	}
	value = math.Min(math.Max(value, 0.65), 0.9)

	r8, g8, b8 := hsvToRGB(hue, saturation, value)
	return fmt.Sprintf("#%02x%02x%02x", r8, g8, b8)
}

// rgbToHSV takes and returns components in 0..1.
func rgbToHSV(r, g, b float64) (hue, saturation, value float64) {
	maximum := math.Max(r, math.Max(g, b))
	minimum := math.Min(r, math.Min(g, b))
	delta := maximum - minimum

	value = maximum
	if maximum > 0 {
		saturation = delta / maximum
	}
	if delta == 0 {
		return 0, saturation, value
	}

	switch maximum {
	case r:
		hue = math.Mod((g-b)/delta, 6)
	case g:
		hue = (b-r)/delta + 2
	default:
		hue = (r-g)/delta + 4
	}

	hue *= 60
	if hue < 0 {
		hue += 360
	}
	return hue, saturation, value
}

func hsvToRGB(hue, saturation, value float64) (r, g, b uint8) {
	chroma := value * saturation
	secondary := chroma * (1 - math.Abs(math.Mod(hue/60, 2)-1))
	offset := value - chroma

	var rf, gf, bf float64
	switch {
	case hue < 60:
		rf, gf, bf = chroma, secondary, 0
	case hue < 120:
		rf, gf, bf = secondary, chroma, 0
	case hue < 180:
		rf, gf, bf = 0, chroma, secondary
	case hue < 240:
		rf, gf, bf = 0, secondary, chroma
	case hue < 300:
		rf, gf, bf = secondary, 0, chroma
	default:
		rf, gf, bf = chroma, 0, secondary
	}

	toByte := func(v float64) uint8 {
		return uint8(math.Round(math.Min(math.Max(v+offset, 0), 1) * 255))
	}
	return toByte(rf), toByte(gf), toByte(bf)
}
