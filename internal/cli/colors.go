package cli

import (
	"os"
	"runtime"

	"golang.org/x/term"
)

// ANSI color codes for terminal output.
const (
	ColorCyan   = "\033[96m"
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
	ColorYellow = "\033[93m"
	ColorGreen  = "\033[92m"
	ColorRed    = "\033[91m"
	ColorWhite  = "\033[97m"
	ColorOrange = "\033[38;5;208m"
	ColorReset  = "\033[0m"
)

// ASCII-safe symbols that render on every OS and terminal.
const (
	SymOK   = "[OK]"
	SymFAIL = "[X]"
	SymWARN = "[!]"
	SymDASH = "[-]"
)

// stdoutIsTerminal reports whether this process's stdout is an interactive
// terminal. It is a var because there is no portable way to open a pty just to
// assert that colour IS emitted in front of one, so the positive case is pinned
// by overriding this rather than by faking a device.
var stdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// NoColor returns true when colored output should be suppressed.
var NoColor = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	// Disable colors on Windows cmd.exe unless running in Windows Terminal
	// or another modern terminal that sets WT_SESSION or TERM.
	if runtime.GOOS == "windows" {
		if os.Getenv("WT_SESSION") == "" && os.Getenv("TERM") == "" {
			return true
		}
	}
	// An ANSI escape is for a human at a terminal. Without this, `ferrogw
	// version > out.txt` and `ferrogw status | grep` carried escape bytes in
	// the payload, so the file and the pipe held something no consumer asked
	// for. --format json/yaml was never affected; the human paths were.
	return !stdoutIsTerminal()
}

// Clr wraps s in the given ANSI code unless NoColor() is true.
func Clr(code, s string) string {
	if NoColor() {
		return s
	}
	return code + s + ColorReset
}
