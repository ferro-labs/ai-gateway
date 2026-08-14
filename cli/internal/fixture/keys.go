package fixture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// keyRow is the /admin/keys row, tags exactly as the gateway serves them.
// There is deliberately no "state" and no "revoked" field: the CLI derives
// revoked/expired/active from revoked_at, active and expires_at.
type keyRow struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"` // masked head8...tail4 on reads
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	CreatedAt  string   `json:"created_at"`
	RevokedAt  *string  `json:"revoked_at"`
	ExpiresAt  *string  `json:"expires_at"`
	RotatedAt  *string  `json:"rotated_at"`
	LastUsedAt *string  `json:"last_used_at"`
	UsageCount int      `json:"usage_count"`
	Active     bool     `json:"active"`
}

// keyStore is the stateful half of the fake: create/rotate/revoke/delete mutate
// it and later GETs reflect the change, so the TUI key wizard can be exercised
// end to end. One lock guards the whole store — a fake serves one operator, so
// a mutex and a slice are enough.
type keyStore struct {
	mu      sync.RWMutex
	rows    []keyRow
	secrets map[string]string // key id -> full secret, never serialized on reads
	n       int
}

func newKeyStore() *keyStore {
	now := time.Now().UTC()
	ks := &keyStore{secrets: map[string]string{}}
	// Four seeded rows covering every state the CLI derives: active, active
	// with an expiry, revoked, and expired.
	ks.seed(actorOps, []string{scopeAdmin}, now.AddDate(0, -3, 0), func(r *keyRow) {
		r.LastUsedAt = sp(ts(now.Add(-4 * time.Minute)))
		r.UsageCount = 18422
	})
	ks.seed("ci-pipeline", []string{scopeReadOnly}, now.AddDate(0, -1, 0), func(r *keyRow) {
		r.ExpiresAt = sp(ts(now.AddDate(0, 2, 0)))
		r.LastUsedAt = sp(ts(now.Add(-51 * time.Minute)))
		r.UsageCount = 3907
	})
	ks.seed("laptop-old", []string{scopeAdmin}, now.AddDate(-1, 0, 0), func(r *keyRow) {
		r.RevokedAt = sp(ts(now.AddDate(0, 0, -12)))
		r.LastUsedAt = sp(ts(now.AddDate(0, 0, -12)))
		r.UsageCount = 240
		r.Active = false
	})
	// An expired key keeps active:true and revoked_at:null. The gateway clears
	// active only in Revoke (memory store keys.go, and the SQL store's own
	// UPDATE), so nothing flips it when a deadline passes — model.KeyIsUsable
	// checks expires_at separately for exactly that reason. Seeding this row
	// as active:false was fixture-only fiction, and it hid a real bug: the CLI
	// derived "expired" from !active, which a real expired key never sets, so
	// `ferro keys list` called it active.
	ks.seed("demo-temp", []string{scopeReadOnly}, now.AddDate(0, 0, -40), func(r *keyRow) {
		r.ExpiresAt = sp(ts(now.AddDate(0, 0, -6)))
		r.UsageCount = 12
	})
	return ks
}

// seed appends one row; opt tweaks it into the state being seeded.
func (ks *keyStore) seed(name string, scopes []string, created time.Time, opt func(*keyRow)) {
	id, secret := ks.nextID(), newSecret()
	row := keyRow{
		ID:        id,
		Key:       mask(secret),
		Name:      name,
		Scopes:    scopes,
		CreatedAt: ts(created),
		Active:    true,
	}
	if opt != nil {
		opt(&row)
	}
	ks.rows = append(ks.rows, row)
	ks.secrets[id] = secret
}

// nextID mints a readable, stable-looking id. Callers hold the lock (or are
// constructing the store).
func (ks *keyStore) nextID() string {
	ks.n++
	return fmt.Sprintf("key_%02d", ks.n)
}

// newSecret mints a master-key-shaped credential: `fgw_` + random token.
func newSecret() string { return "fgw_" + strings.ToLower(randToken()) }

func (ks *keyStore) list() []keyRow {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]keyRow, len(ks.rows))
	copy(out, ks.rows)
	return out
}

// unknownScope reports the first scope outside the gateway's closed set, and
// ok=false with it. An empty list is valid — the store defaults it — so this
// rejects only a scope that was actually asked for and cannot be granted.
func unknownScope(scopes []string) (string, bool) {
	for _, s := range scopes {
		if s != scopeAdmin && s != scopeReadOnly {
			return s, false
		}
	}
	return "", true
}

// defaultScopes gives an unspecified scope list the gateway's own default. The
// real server applies it in the key store rather than the handler — both
// backends call one defaultScopes on the way into Create — so an absent, null
// and empty `scopes` all mint a least-privilege read-only key. Storing the
// empty list instead would have handed every fixture-backed test a scopeless
// key the real gateway never issues.
func defaultScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{scopeReadOnly}
	}
	return scopes
}

// create returns the stored (masked) row and the full secret, which the caller
// serves exactly once.
func (ks *keyStore) create(name string, scopes []string, expiresAt *string) (keyRow, string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	id, secret := ks.nextID(), newSecret()
	row := keyRow{
		ID:        id,
		Key:       mask(secret),
		Name:      name,
		Scopes:    defaultScopes(scopes),
		CreatedAt: ts(time.Now().UTC()),
		ExpiresAt: expiresAt,
		Active:    true,
	}
	ks.rows = append(ks.rows, row)
	ks.secrets[id] = secret
	return row, secret
}

// rotate replaces the secret in place, returning the row and the new secret.
func (ks *keyStore) rotate(id string) (keyRow, string, bool) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	i := ks.indexOf(id)
	if i < 0 {
		return keyRow{}, "", false
	}
	secret := newSecret()
	ks.rows[i].Key = mask(secret)
	ks.rows[i].RotatedAt = sp(ts(time.Now().UTC()))
	ks.secrets[id] = secret
	return ks.rows[i], secret, true
}

func (ks *keyStore) revoke(id string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	i := ks.indexOf(id)
	if i < 0 {
		return false
	}
	ks.rows[i].RevokedAt = sp(ts(time.Now().UTC()))
	ks.rows[i].Active = false
	return true
}

func (ks *keyStore) remove(id string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	i := ks.indexOf(id)
	if i < 0 {
		return false
	}
	ks.rows = append(ks.rows[:i], ks.rows[i+1:]...)
	delete(ks.secrets, id)
	return true
}

// indexOf is called with the lock held.
func (ks *keyStore) indexOf(id string) int {
	for i := range ks.rows {
		if ks.rows[i].ID == id {
			return i
		}
	}
	return -1
}

// mask is the gateway's read masking: first 8 characters, "...", last 4.
func mask(secret string) string {
	if len(secret) < 12 {
		return "..."
	}
	return secret[:8] + "..." + secret[len(secret)-4:]
}

// registerKeys wires the /admin/keys surface onto the mux.
func registerKeys(mux *http.ServeMux, ks *keyStore) {
	mux.HandleFunc("GET /admin/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, ks.list()) // bare array, no envelope
	})

	// The real gateway serves this (internal/admin/handlers/server.go); leaving
	// it out made `ferro keys get` 404 against the fake while working against a
	// live gateway — a fixture gap that reads as a CLI bug.
	mux.HandleFunc("GET /admin/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		for _, row := range ks.list() {
			if row.ID == id {
				writeJSON(w, http.StatusOK, row) // masked, like every read
				return
			}
		}
		writeErr(w, http.StatusNotFound, kindNotFound, "api key not found")
	})

	mux.HandleFunc("POST /admin/keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name      string   `json:"name"`
			Scopes    []string `json:"scopes"`
			ExpiresAt *string  `json:"expires_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest, "malformed request body")
			return
		}
		if in.Name == "" {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest, "name is required")
			return
		}
		if bad, ok := unknownScope(in.Scopes); !ok {
			// model.ValidateScopes, worded and ordered exactly as it words it.
			writeErr(w, http.StatusBadRequest, kindInvalidRequest,
				fmt.Sprintf("unknown scope %q: valid scopes are %s, %s", bad, scopeAdmin, scopeReadOnly))
			return
		}
		// No nil-to-empty normalisation here: the store defaults an unspecified
		// list, so a created row always carries at least one scope.
		row, secret := ks.create(in.Name, in.Scopes, in.ExpiresAt)
		row.Key = secret // the full secret, served exactly once
		writeJSON(w, http.StatusCreated, row)
	})

	mux.HandleFunc("POST /admin/keys/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		row, secret, ok := ks.rotate(r.PathValue("id"))
		if !ok {
			writeErr(w, http.StatusNotFound, kindNotFound, msgKeyNotFound)
			return
		}
		row.Key = secret // the new secret, served exactly once
		writeJSON(w, http.StatusOK, row)
	})

	mux.HandleFunc("POST /admin/keys/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !ks.revoke(r.PathValue("id")) {
			writeErr(w, http.StatusNotFound, kindNotFound, msgKeyNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{fieldStatus: statusRevoked})
	})

	mux.HandleFunc("DELETE /admin/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !ks.remove(r.PathValue("id")) {
			writeErr(w, http.StatusNotFound, kindNotFound, msgKeyNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent) // 204, empty body
	})
}
