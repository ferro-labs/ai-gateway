package aigateway

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoadConfig feeds arbitrary bytes through the real config parse path.
// LoadConfig reads a file and dispatches on its extension, so the fuzzer writes
// each input to both a .yaml and a .json file to cover the YAML and JSON decode
// branches. Config files are operator-supplied but may be malformed; parsing any
// byte sequence must return an error rather than panic (guarding against decoder
// edge cases such as deeply nested YAML).
func FuzzLoadConfig(f *testing.F) {
	f.Add([]byte("strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n"))
	f.Add([]byte(`{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"}]}`))
	f.Add([]byte("strategy: [unterminated"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		for _, ext := range []string{".yaml", ".json"} {
			path := filepath.Join(dir, "config"+ext)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write temp config: %v", err)
			}
			_, _ = LoadConfig(path)
		}
	})
}
