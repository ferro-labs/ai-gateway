package cli

import (
	"os"
	"runtime"
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
	return false
}

// Clr wraps s in the given ANSI code unless NoColor() is true.
func Clr(code, s string) string {
	if NoColor() {
		return s
	}
	return code + s + ColorReset
}
