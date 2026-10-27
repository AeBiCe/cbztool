// Package term renders the status markers cbz prints in front of warnings
// ("[!]", yellow "!") and errors ("[X]", red "X"). Color is used only when the
// stream is a terminal and neither NO_COLOR nor TERM=dumb is set, so piped
// output and logs stay plain text.
package term

import (
	"fmt"
	"os"
)

const (
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
	reset  = "\x1b[0m"
)

// Warn returns the warning marker for output written to f.
func Warn(f *os.File) string { return mark(f, "!", yellow) }

// Error returns the error marker for output written to f.
func Error(f *os.File) string { return mark(f, "X", red) }

// Warnf prints a warning line to stderr.
func Warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Warn(os.Stderr), fmt.Sprintf(format, a...))
}

// Errorf prints an error line to stderr.
func Errorf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Error(os.Stderr), fmt.Sprintf(format, a...))
}

func mark(f *os.File, sym, color string) string {
	if !Color(f) {
		return "[" + sym + "]"
	}
	return "[" + color + sym + reset + "]"
}

// Color reports whether output to f should be colored.
func Color(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
