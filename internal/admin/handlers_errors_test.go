package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// dbFailKeyStore embeds KeyStore but forces the mutating operations to fail with
// a wrapped, non-ErrKeyNotFound error, simulating a database or transient store
// failure so we can assert it is reported as 500 (not masked as 404) and that
// the wrapped internal text is never leaked to the client.
type dbFailKeyStore struct {
	*KeyStore
}

func (s *dbFailKeyStore) Delete(context.Context, string) error {
	return fmt.Errorf("delete key: %w", errors.New("db connection lost"))
}

func (s *dbFailKeyStore) RotateKey(context.Context, string) (*APIKey, error) {
	return nil, fmt.Errorf("rotate key: %w", errors.New("db connection lost"))
}

// reqWithID builds a request carrying the chi "id" URL param the key handlers read.
func reqWithID(method, id string) *http.Request {
	req := httptest.NewRequest(method, "/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestKeyHandler_StoreFailure_Returns500AndDoesNotLeak(t *testing.T) {
	h := &Handlers{Keys: &dbFailKeyStore{KeyStore: NewKeyStore()}}

	t.Run("delete", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.deleteKey(w, reqWithID(http.MethodDelete, "some-id"))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("a store failure must map to 500, got %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "db connection lost") {
			t.Fatalf("internal error text leaked to client: %s", w.Body.String())
		}
	})

	t.Run("rotate", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.rotateKey(w, reqWithID(http.MethodPost, "some-id"))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("a store failure must map to 500, got %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "db connection lost") {
			t.Fatalf("internal error text leaked to client: %s", w.Body.String())
		}
	})
}

func TestKeyHandler_NotFound_StillReturns404(t *testing.T) {
	h := &Handlers{Keys: NewKeyStore()}
	w := httptest.NewRecorder()
	h.deleteKey(w, reqWithID(http.MethodDelete, "does-not-exist"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("a genuine missing key must map to 404, got %d", w.Code)
	}
}
