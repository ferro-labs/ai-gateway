package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// strictDecodeCases are bodies whose acceptance must be identical whether the
// config arrives as a file or as a request body.
//
// Each rejected case is one key away from a valid config, which is the shape a
// hand edit actually takes: the value is well-formed, the schema is not, and a
// lenient decoder produces a Config missing exactly the setting that was being
// changed. targets[].models is the one that motivated the guard — it is edited
// by hand, has no other source, and its silent loss shows up nowhere.
var strictDecodeCases = []struct {
	name   string
	body   string
	reject bool
}{
	{
		name: "valid",
		body: `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"},{"virtual_key":"anthropic"}]}`,
	},
	{
		name: "valid with declared models",
		body: `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai","models":["gpt-4o-mini"]}]}`,
	},
	{
		name:   "declared models misspelled singular",
		body:   `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai","model":["gpt-4o-mini"]}]}`,
		reject: true,
	},
	{
		name:   "virtual_key misspelled",
		body:   `{"strategy":{"mode":"fallback"},"targets":[{"target_key":"openai"}]}`,
		reject: true,
	},
	{
		name:   "unknown top-level key",
		body:   `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"}],"fallback":true}`,
		reject: true,
	},
	{
		name:   "unknown strategy key",
		body:   `{"strategy":{"mode":"fallback","operator":"eq"},"targets":[{"virtual_key":"openai"}]}`,
		reject: true,
	},
	{
		name:   "trailing data after the object",
		body:   `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"}]}{"targets":[]}`,
		reject: true,
	},
}

// TestConfigBodyDecodeMatchesFileDecode holds the two ways a config enters the
// gateway to one answer.
//
// A config applied through PUT /admin/config and the same config handed to
// `ferrogw validate` have to be valid or invalid together. When only the file
// path decoded strictly, a misspelled key was an error in CI and a silent
// discard in production — the operator got "updated" for a config that dropped
// the setting they had just written.
//
// The expectation is deliberately the *other decoder*, not a hardcoded status:
// that is what makes the two unable to drift apart, since tightening or
// loosening either one alone fails here.
func TestConfigBodyDecodeMatchesFileDecode(t *testing.T) {
	for _, tc := range strictDecodeCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write config file: %v", err)
			}
			_, fileErr := config.LoadConfig(path)

			var cfg config.Config
			bodyErr := decodeConfigBody(strings.NewReader(tc.body), &cfg)

			if (fileErr != nil) != tc.reject {
				t.Fatalf("config.LoadConfig: reject=%v, want %v (err: %v)", fileErr != nil, tc.reject, fileErr)
			}
			if (bodyErr != nil) != (fileErr != nil) {
				t.Fatalf("decodeConfigBody and config.LoadConfig disagree: body err=%v, file err=%v", bodyErr, fileErr)
			}
		})
	}
}

// TestUpdateConfigRejectsUnknownFieldAndLeavesConfigUnchanged is the same rule
// at the socket: a typo has to be refused, and refusing it must not have
// half-applied anything.
func TestUpdateConfigRejectsUnknownFieldAndLeavesConfigUnchanged(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai","model":["gpt-4o-mini"]}]}`
	req := authedRequest(http.MethodPut, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown field, got %d: %s", w.Code, w.Body.String())
	}
	// The name of the offending key is the whole point: an operator who cannot
	// see which key was wrong is no better off than one who was told nothing.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if !strings.Contains(envelope.Error.Message, `"model"`) {
		t.Fatalf("error does not name the rejected key: %s", envelope.Error.Message)
	}

	getReq := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	var cfg config.Config
	if err := json.NewDecoder(getW.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if cfg.Strategy.Mode != config.ModeSingle {
		t.Fatalf("rejected update still changed the active config: mode is %s", cfg.Strategy.Mode)
	}
}
