package table

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteRendersHeaderRuleAndRows(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, []string{"NAME", "STATE"}, [][]string{{"openai", "closed"}})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + rule + 1 row, got %d lines:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("header line: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "----") {
		t.Fatalf("rule must be dashes matching header width, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "openai") {
		t.Fatalf("row line: %q", lines[2])
	}
}

// Rows must be Write's output split into lines, not a second layout — that is
// the whole point of routing both callers through one implementation.
func TestRowsMatchesWrite(t *testing.T) {
	headers := []string{"NAME", "READY"}
	cells := [][]string{{"filesystem", "yes"}, {"a-much-longer-server-name", "no"}}

	var buf bytes.Buffer
	Write(&buf, headers, cells)
	want := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i := range want {
		want[i] = strings.TrimRight(want[i], " ")
	}

	got := Rows(headers, cells)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Rows drifted from Write:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestOrDash(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "-"},
		{"anthropic", "anthropic"},
	} {
		if got := OrDash(tc.in); got != tc.want {
			t.Errorf("OrDash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCountOrDashDistinguishesNilFromZero(t *testing.T) {
	zero := 0
	five := 5
	for _, tc := range []struct {
		name string
		in   *int
		want string
	}{
		{"nil means never asked", nil, "-"},
		{"zero is a real answer", &zero, "0"},
		{"a positive count", &five, "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountOrDash(tc.in); got != tc.want {
				t.Errorf("CountOrDash(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPositiveOrDashTreatsZeroAsAbsent(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int
		want string
	}{
		{"zero is not a real context window", 0, "-"},
		{"negative is not a real context window", -1, "-"},
		{"a real context window", 200000, "200000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PositiveOrDash(tc.in); got != tc.want {
				t.Errorf("PositiveOrDash(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBoolYN(t *testing.T) {
	if BoolYN(true) != "yes" || BoolYN(false) != "no" {
		t.Fatalf("BoolYN(true)=%q BoolYN(false)=%q", BoolYN(true), BoolYN(false))
	}
}

func TestFmtTimeRendersLocalRFC3339(t *testing.T) {
	if got := FmtTime(time.Time{}); got != "-" {
		t.Fatalf("zero time must render as \"-\", got %q", got)
	}
	utc, err := time.Parse(time.RFC3339, "2026-08-08T09:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	// Pin the zone and assert a literal. Computing the expectation as
	// utc.Local().Format(...) restates the implementation, and on a UTC
	// machine — which CI is — it collapses to the same string however
	// FmtTime handles zones, so the conversion would go untested precisely
	// where it runs. FixedZone rather than LoadLocation because the test
	// matrix includes Windows, where the tz database may not be present.
	orig := time.Local
	time.Local = time.FixedZone("IST", 5*60*60+30*60)
	t.Cleanup(func() { time.Local = orig })

	if got, want := FmtTime(utc), "2026-08-08T14:30:00+05:30"; got != want {
		t.Fatalf("FmtTime must render in local time: got %q, want %q", got, want)
	}
}

func TestFmtTimePtr(t *testing.T) {
	if got := FmtTimePtr(nil); got != "-" {
		t.Fatalf("nil pointer must render as \"-\", got %q", got)
	}
	utc, err := time.Parse(time.RFC3339, "2026-08-08T09:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := FmtTimePtr(&utc), FmtTime(utc); got != want {
		t.Fatalf("FmtTimePtr(&t) = %q, want %q", got, want)
	}
}
