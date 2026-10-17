// Command wasapi is a development harness for the spectrum visualizer: it
// captures the default output device and prints the bands as text, so the
// analysis can be eyeballed with real audio playing.
//
//	go run ./spikes/wasapi
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/visualizer"
)

const ramp = " ▁▂▃▄▅▆▇█"

func main() {
	monitor, err := visualizer.StartLoopback()
	if err != nil {
		fmt.Println("loopback unavailable:", err)
		fmt.Println("using the stand-in animation instead")
		monitor = visualizer.Start()
	}
	defer monitor.Close()

	seconds := 20
	if len(os.Args) > 1 {
		fmt.Sscanf(os.Args[1], "%d", &seconds)
	}
	fmt.Printf("capturing for %ds (live=%v) - play something now\n", seconds, monitor.Live())

	blocks := []rune(ramp)
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)

		bands := monitor.Bands()
		var row strings.Builder
		peak := 0.0
		for _, level := range bands {
			index := int(level*float64(len(blocks)-1) + 0.5)
			if index < 0 {
				index = 0
			}
			if index >= len(blocks) {
				index = len(blocks) - 1
			}
			row.WriteRune(blocks[index])
			if level > peak {
				peak = level
			}
		}
		fmt.Printf("%s  peak=%.2f\n", row.String(), peak)
	}
}
