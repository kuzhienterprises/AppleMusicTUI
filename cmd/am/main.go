// Command am is an Apple Music client for the terminal.
package main

import (
	"os"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
