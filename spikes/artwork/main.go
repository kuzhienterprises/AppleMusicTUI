// Command artwork is a development harness for the cover pipeline. It fetches
// one cover the way the player does and prints it as a luminance ramp, so the
// framing and the downscale can be checked without a terminal that renders
// colour.
//
//	go run ./spikes/artwork
//	go run ./spikes/artwork "<artwork url template>"
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/artwork"
)

// defaultTemplate is a real Apple artwork URL: OK Computer, Radiohead.
const defaultTemplate = "https://is1-ssl.mzstatic.com/image/thumb/Music116/v4/07/60/ba/0760ba0f-148c-b18f-d0ff-169ee96f3af5/634904078164.png/{w}x{h}bb.jpg"

// Columns matches the player. Rows are half of that, because half blocks pack
// two pixels into each cell.
const columns = 20

const ramp = " .:-=+*#%@"

func main() {
	template := defaultTemplate
	if len(os.Args) > 1 {
		template = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	started := time.Now()
	cover, err := artwork.Load(ctx, template, columns)
	if err != nil {
		fmt.Println("could not load the cover:", err)
		os.Exit(1)
	}
	fmt.Printf("loaded %dx%d px in %s\n", cover.Bounds().Dx(), cover.Bounds().Dy(), time.Since(started).Round(time.Millisecond))

	// A second load must be served from the cache.
	started = time.Now()
	if _, err := artwork.Load(ctx, template, columns); err != nil {
		fmt.Println("cached load failed:", err)
		os.Exit(1)
	}
	fmt.Printf("cached load took %s\n", time.Since(started).Round(time.Microsecond))

	bounds := cover.Bounds()
	rows := columns / 2

	fmt.Println("\nluminance (dark to light):")
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; x++ {
			r, g, b, _ := cover.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			// Rec. 601 luma, which tracks perceived brightness closely enough
			// for a sanity check.
			luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535
			index := int(luma * float64(len(ramp)-1))
			if index < 0 {
				index = 0
			}
			if index >= len(ramp) {
				index = len(ramp) - 1
			}
			fmt.Printf("%c", ramp[index])
		}
		fmt.Println()
	}

	distinct := map[uint32]int{}
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; x++ {
			r, g, b, _ := cover.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			distinct[(r>>8)<<16|(g>>8)<<8|(b>>8)]++
		}
	}
	fmt.Printf("\ndistinct colours in the scaled cover: %d of %d pixels\n", len(distinct), columns*rows)
	fmt.Printf("dominant accent: %s\n", artwork.Dominant(cover))
}
