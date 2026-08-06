package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// errorBody decodes the unified admin error envelope so a test can assert on
// the machine-readable code as well as the human-readable message.
func errorBody(t *testing.T, w *httptest.ResponseRecorder) (message, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Message, body.Error.Code
}

func TestCreateKeyRejectsUnknownScope(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		scope string
	}{
		{"near miss of read_only", `{"name":"k","scopes":["readonly"]}`, "readonly"},
		{"typo of admin", `{"name":"k","scopes":["adminn"]}`, "adminn"},
		{"nonsense", `{"name":"k","scopes":["totally-bogus"]}`, "totally-bogus"},
		{"empty string element", `{"name":"k","scopes":[""]}`, ""},
		{"one valid one not", `{"name":"k","scopes":["read_only","bogus"]}`, "bogus"},
		{"case mismatch", `{"name":"k","scopes":["Admin"]}`, "Admin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, r := setupTestRouter()
			key := createAdminKey(t, h)

			req := authedRequest(http.MethodPost, "/admin/keys", tc.body, key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			message, code := errorBody(t, w)
			if code != "invalid_scope" {
				t.Errorf("expected code invalid_scope, got %q", code)
			}
			// Naming only the offending value moves the guessing; the accepted
			// set has to be in the message too.
			if !strings.Contains(message, tc.scope) {
				t.Errorf("message %q does not name the offending scope %q", message, tc.scope)
			}
			for _, valid := range []string{model.ScopeAdmin, model.ScopeReadOnly} {
				if !strings.Contains(message, valid) {
					t.Errorf("message %q does not list the valid scope %q", message, valid)
				}
			}

			// A refused request must mint nothing: the whole point is that no
			// working-looking credential exists afterwards.
			if keys := h.Keys.List(t.Context()); len(keys) != 1 {
				t.Errorf("expected only the caller's key to exist, got %d keys", len(keys))
			}
		})
	}
}

func TestCreateKeyAcceptsKnownScopes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"admin", `{"name":"k","scopes":["admin"]}`, []string{model.ScopeAdmin}},
		{"read_only", `{"name":"k","scopes":["read_only"]}`, []string{model.ScopeReadOnly}},
		{"both", `{"name":"k","scopes":["admin","read_only"]}`, []string{model.ScopeAdmin, model.ScopeReadOnly}},
		// An omitted or empty list is an under-specified request, not a
		// misspelled one. The store's documented least-privilege default still
		// applies, so existing clients that never send the field keep working.
		{"omitted", `{"name":"k"}`, []string{model.ScopeReadOnly}},
		{"empty list", `{"name":"k","scopes":[]}`, []string{model.ScopeReadOnly}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, r := setupTestRouter()
			key := createAdminKey(t, h)

			req := authedRequest(http.MethodPost, "/admin/keys", tc.body, key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
			}
			var created model.APIKey
			decodeJSON(t, w.Body, &created)
			if !slices.Equal(created.Scopes, tc.want) {
				t.Errorf("expected scopes %v, got %v", tc.want, created.Scopes)
			}
		})
	}
}

func TestUpdateKeyRejectsUnknownScope(t *testing.T) {
	h, r := setupTestRouter()
	caller := createAdminKey(t, h)
	target := createTestKey(t, h, "target", []string{model.ScopeAdmin}, nil)

	body := `{"name":"renamed","scopes":["readonly"]}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, caller)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	message, code := errorBody(t, w)
	if code != "invalid_scope" {
		t.Errorf("expected code invalid_scope, got %q", code)
	}
	if !strings.Contains(message, "readonly") || !strings.Contains(message, model.ScopeReadOnly) {
		t.Errorf("message %q should name the offending scope and the valid set", message)
	}

	// The refusal must land before any store write, or a rejected request
	// still renames the key.
	stored, ok := h.Keys.Get(t.Context(), target.ID)
	if !ok {
		t.Fatal("target key disappeared")
	}
	if stored.Name != "target" {
		t.Errorf("expected name unchanged, got %q", stored.Name)
	}
	if !slices.Equal(stored.Scopes, []string{model.ScopeAdmin}) {
		t.Errorf("expected scopes unchanged, got %v", stored.Scopes)
	}
}

// A malformed scope is a bad request whatever the store's state is, so the
// 400 must win over the 409 the lockout guards would otherwise raise. Without
// this ordering an operator who typos a scope on their own key is told they
// cannot de-scope themselves and never learns the scope was the problem.
func TestUpdateKeyUnknownScopeOutranksLockoutGuard(t *testing.T) {
	h, r := setupTestRouter()
	caller := createAdminKey(t, h)

	body := `{"name":"admin-key","scopes":["adminn"]}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+caller.ID, body, caller)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if _, code := errorBody(t, w); code != "invalid_scope" {
		t.Errorf("expected code invalid_scope, got %q", code)
	}
}

// An empty list on update keeps its established meaning — leave scopes alone —
// so renaming a key without restating its permissions still works.
func TestUpdateKeyEmptyScopesLeavesScopesUntouched(t *testing.T) {
	h, r := setupTestRouter()
	caller := createAdminKey(t, h)
	target := createTestKey(t, h, "target", []string{model.ScopeReadOnly}, nil)

	body := `{"name":"renamed","scopes":[]}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, caller)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	stored, ok := h.Keys.Get(t.Context(), target.ID)
	if !ok {
		t.Fatal("target key disappeared")
	}
	if stored.Name != "renamed" {
		t.Errorf("expected name renamed, got %q", stored.Name)
	}
	if !slices.Equal(stored.Scopes, []string{model.ScopeReadOnly}) {
		t.Errorf("expected scopes unchanged, got %v", stored.Scopes)
	}
}
