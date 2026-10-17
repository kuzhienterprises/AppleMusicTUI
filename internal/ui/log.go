// Package ui holds terminal presentation helpers shared by every command.
package ui

import (
	"fmt"
	"os"
)

var verbose bool

// SetVerbose enables debug output.
func SetVerbose(value bool) { verbose = value }

// Verbose reports whether debug output is enabled.
func Verbose() bool { return verbose }

// Diagnostics are written to stderr so stdout stays clean for --json.
func Info(format string, args ...any)  { fmt.Fprintf(os.Stderr, format+"\n", args...) }
func Step(format string, args ...any)  { fmt.Fprintf(os.Stderr, "\u2022 "+format+"\n", args...) }
func OK(format string, args ...any)    { fmt.Fprintf(os.Stderr, "\u2713 "+format+"\n", args...) }
func Warn(format string, args ...any)  { fmt.Fprintf(os.Stderr, "! "+format+"\n", args...) }
func Error(format string, args ...any) { fmt.Fprintf(os.Stderr, "\u2717 "+format+"\n", args...) }

// Debug only prints when --verbose is set.
func Debug(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
	}
}
