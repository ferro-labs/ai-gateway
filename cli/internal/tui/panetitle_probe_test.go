package tui

import (
	"strings"
	"testing"
)

// A demo recording showed "COMMAND OUTPUT" rendered twice in the pane after
// navigating to another screen and back. mainFrame prepends the title and
// homeView returns only the transcript, so the pane title must appear exactly
// once in a render — on Home, and after any round trip through another screen.
func TestPaneTitleRendersExactlyOnce(t *testing.T) {
	a := testApp(126, 34)
	a.Transcript.Push(TranscriptRow{Glyph: "$", Text: "ferro status"})

	count := func(where string) int {
		n := strings.Count(a.render(), "COMMAND OUTPUT")
		t.Logf("%s: %d occurrence(s)", where, n)
		return n
	}

	if got := count("home, first render"); got != 1 {
		t.Fatalf("pane title must render once on Home, got %d", got)
	}

	// Round trip: Home -> playground -> Home, which is what the recording did.
	a.Screen = ScreenPlayground
	_ = a.render()
	a.Screen = ScreenHome
	if got := count("home, after a playground round trip"); got != 1 {
		t.Fatalf("pane title duplicated after returning to Home: %d occurrences", got)
	}

	// And through the logs screen, the other transition the demo makes.
	a.Screen = ScreenLogs
	_ = a.render()
	a.Screen = ScreenHome
	if got := count("home, after a logs round trip"); got != 1 {
		t.Fatalf("pane title duplicated after returning from logs: %d occurrences", got)
	}
}
