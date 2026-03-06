package core

import (
	"regexp"
	"strconv"
)

// statusCodePattern matches HTTP status codes formatted as "(NNN)" inside
// provider error messages (e.g. "provider API error (429): ...").
var statusCodePattern = regexp.MustCompile(`\((\d{3})\)`)

// ParseStatusCode extracts the HTTP status code embedded in a provider error
// message. Returns 0 if no 3-digit parenthesised code can be found.
// All built-in providers embed a 3-digit HTTP status code in parentheses inside
// their error messages (e.g. "... API error (NNN): message").
func ParseStatusCode(err error) int {
	if err == nil {
		return 0
	}
	m := statusCodePattern.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(m[1])
	return code
}
