package web

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The gateway serves Content-Security-Policy: script-src 'self'. Under that
// policy an inline <script> block or an on*="…" attribute silently stops
// running, which is exactly how a broken login page ships unnoticed. Event
// handlers belong in web/static as data-action registrations.
var (
	inlineScript  = regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>\s*[^<\s]`)
	inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=\s*["']`)
)

func TestTemplatesCarryNoInlineScript(t *testing.T) {
	err := fs.WalkDir(Assets, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		body, readErr := fs.ReadFile(Assets, path)
		if readErr != nil {
			return readErr
		}
		markup := string(body)

		if loc := inlineScript.FindString(markup); loc != "" {
			t.Errorf("%s: inline <script> is blocked by script-src 'self'; move it into web/static (found %q)", path, strings.TrimSpace(loc))
		}
		if loc := inlineHandler.FindString(markup); loc != "" {
			t.Errorf("%s: inline %q handler is blocked by script-src 'self'; use data-action + registerActions", path, strings.TrimSpace(loc))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}
