package cli

import "os"

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

// NoColor returns true when colored output should be suppressed.
var NoColor = func() bool {
	return os.Getenv("NO_COLOR") != ""
}

// Clr wraps s in the given ANSI code unless NoColor() is true.
func Clr(code, s string) string {
	if NoColor() {
		return s
	}
	return code + s + ColorReset
}
