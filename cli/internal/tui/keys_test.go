package tui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/fixture"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// fixtureApp is a shell wired to the stateful fake: create/rotate/revoke
// actually mutate a key store, so the wizard is exercised end to end rather
// than against a canned response that can never disagree with it.
func fixtureApp(t *testing.T) *App {
	t.Helper()
	srv := httptest.NewServer(fixture.Handler(fixture.Default()))
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "fgw_test")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	a := composerApp()
	a.Client = c
	return a
}

// runCmd runs one command and applies its message, failing rather than
// returning nil so a broken flow names the step it stopped at.
func runCmd(t *testing.T, a *App, cmd tea.Cmd, what string) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatalf("%s returned no command", what)
	}
	msg := cmd()
	a.update(msg)
	return msg
}

func loadKeys(t *testing.T, a *App) {
	t.Helper()
	runCmd(t, a, a.route("keys"), "keys")
	if a.Keys.err != nil {
		t.Fatalf("key list failed: %v", a.Keys.err)
	}
	if len(a.Keys.keys) == 0 {
		t.Fatal("the fixture seeds keys; an empty list means the fetch did not land")
	}
	if a.Screen != ScreenKeys {
		t.Fatalf("keys must switch to the keys pane, got %v", a.Screen)
	}
}

func keyNamed(a *App, name string) (api.Key, bool) {
	for _, k := range a.Keys.keys {
		if k.Name == name {
			return k, true
		}
	}
	return api.Key{}, false
}

func TestKeysTableUsesMaskedKey(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)
	view := a.paneView(120, 18)

	for _, k := range a.Keys.keys {
		if !strings.Contains(view, k.Name) {
			t.Fatalf("key %q missing from the table:\n%s", k.Name, view)
		}
	}
	// The wire's `key` field is masked head8…tail4 on every read; the table
	// shows that, never a full secret and never a bare row id in its place.
	masked := a.Keys.keys[0].Key
	if masked == "" {
		t.Fatal("the fixture serves a masked key on reads")
	}
	if !strings.Contains(view, masked) {
		t.Fatalf("the table must show the masked key %q:\n%s", masked, view)
	}
}

func TestKeysTableRendersEveryDerivedState(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)
	view := a.paneView(120, 18)
	// There is no state field on the wire — api.KeyState derives it.
	seen := map[string]bool{}
	for _, k := range a.Keys.keys {
		seen[api.KeyState(k)] = true
	}
	for _, want := range []string{"active", "revoked", "expired"} {
		if !seen[want] {
			continue // the fixture may not seed every state
		}
		if !strings.Contains(view, want) {
			t.Fatalf("state %q must appear in the table:\n%s", want, view)
		}
	}
}

func TestNewKeysCommandIgnoresEarlierListResult(t *testing.T) {
	a := composerApp()
	a.route("keys list")
	oldGen := a.Keys.gen

	a.route("keys rotate target")
	newGen := a.Keys.gen
	if newGen == oldGen {
		t.Fatal("a newer keys command must invalidate the earlier list request")
	}

	a.update(keysListMsg{gen: oldGen, keys: []api.Key{{ID: "old", Name: "target"}}})
	if a.Modal != nil {
		t.Fatal("an earlier list result must not open the newer command's modal")
	}
	if a.Keys.pendingVerb != "rotate" || a.Keys.pendingArg != "target" {
		t.Fatalf("stale result cleared the newer pending command: %q %q",
			a.Keys.pendingVerb, a.Keys.pendingArg)
	}

	a.update(keysListMsg{gen: newGen, keys: []api.Key{{ID: "current", Name: "target"}}})
	if a.Modal == nil || a.Modal.Title != "Rotate target?" {
		t.Fatalf("the current list result must resume the pending command, got %+v", a.Modal)
	}
}

func TestWizardHappyPath(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)

	a.route("keys create")
	if a.Modal == nil {
		t.Fatal("keys create must open the wizard")
	}
	if a.Modal.Title != "Create API key · name" {
		t.Fatalf("step 1 title drifted from the expected wording: %q", a.Modal.Title)
	}
	if !strings.Contains(a.Modal.Body, "Names are how log rows resolve after a key is revoked.") {
		t.Fatalf("step 1 body drifted from the expected wording: %q", a.Modal.Body)
	}
	typeModal(a, a.Modal, "ci-readonly")
	a.Modal.handle("enter", a)

	if a.Modal == nil || !strings.Contains(a.Modal.Title, "scope") {
		t.Fatalf("step 2 must be the scope step, got %+v", a.Modal)
	}
	if !strings.Contains(a.Modal.Body, "Scope is always sent explicitly") {
		t.Fatalf("step 2 body drifted from the expected wording: %q", a.Modal.Body)
	}
	// The gateway defaults an omitted scope to read_only. This panel told the
	// operator it was granted admin, which is the opposite.
	if strings.Contains(a.Modal.Body, "granted admin") {
		t.Fatalf("step 2 must not restate the old backwards claim: %q", a.Modal.Body)
	}
	if a.Modal.Option() != "read_only" {
		t.Fatalf("the wizard defaults to read_only, got %q", a.Modal.Option())
	}
	a.Modal.handle("space", a) // → admin
	if a.Modal.Option() != "admin" {
		t.Fatalf("space must toggle the scope, got %q", a.Modal.Option())
	}
	a.Modal.handle("enter", a)

	if a.Modal == nil || !strings.Contains(a.Modal.Title, "expiry") {
		t.Fatalf("step 3 must be the expiry step, got %+v", a.Modal)
	}
	if !strings.Contains(a.Modal.Body, "Default 720h. An unexpiring admin key needs a written exception.") {
		t.Fatalf("step 3 body drifted from the expected wording: %q", a.Modal.Body)
	}
	if a.Modal.Value != "720h" {
		t.Fatalf("the expiry step is prefilled with 720h, got %q", a.Modal.Value)
	}

	before := time.Now()
	msg := runCmd(t, a, a.Modal.handle("enter", a), "create")
	res, ok := msg.(modalResultMsg)
	if !ok || res.err != nil {
		t.Fatalf("create must reach the gateway, got %#v", msg)
	}
	if res.key == nil || res.key.Key == "" {
		t.Fatal("create returns the full secret exactly once")
	}
	secret := res.key.Key

	if a.Modal == nil || a.Modal.Title != "Key created" {
		t.Fatalf("a created key must land in its own modal, got %+v", a.Modal)
	}
	if !strings.Contains(a.Modal.Body, "Shown exactly once. Copy it now — the gateway stores only a hash.") {
		t.Fatalf("the secret step body drifted from the expected wording: %q", a.Modal.Body)
	}
	if !strings.Contains(a.paneView(110, 18), secret) {
		t.Fatalf("the secret must be visible in its modal:\n%s", a.paneView(110, 18))
	}

	// Closing it refreshes the list, which is where the request's own values
	// come back: the scope the toggle chose and the expiry it parsed.
	runCmd(t, a, a.Modal.handle("enter", a), "close secret modal")
	created, found := keyNamed(a, "ci-readonly")
	if !found {
		t.Fatalf("the created key must appear after the refresh: %+v", a.Keys.keys)
	}
	if len(created.Scopes) != 1 || created.Scopes[0] != "admin" {
		t.Fatalf("the toggled scope must be what was sent, got %v", created.Scopes)
	}
	if created.ExpiresAt == nil {
		t.Fatal("720h must have been sent as an expiry")
	}
	if d := created.ExpiresAt.Sub(before); d < 719*time.Hour || d > 721*time.Hour {
		t.Fatalf("expiry must be ~720h out, got %v", d)
	}
}

func TestWizardSecretNeverInTranscriptOrHistory(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)

	a.route("keys create")
	typeModal(a, a.Modal, "ci-readonly")
	a.Modal.handle("enter", a)
	a.Modal.handle("enter", a) // keep read_only
	msg := runCmd(t, a, a.Modal.handle("enter", a), "create")
	secret := msg.(modalResultMsg).key.Key
	if secret == "" {
		t.Fatal("nothing to assert about if no secret was issued")
	}
	runCmd(t, a, a.Modal.handle("enter", a), "close secret modal")

	if strings.Contains(transcriptText(a), secret) {
		t.Fatalf("the secret must never enter the transcript:\n%s", transcriptText(a))
	}
	for _, h := range a.Composer.History {
		if strings.Contains(h, secret) {
			t.Fatalf("the secret must never enter the command history: %q", h)
		}
	}
	if strings.Contains(a.render(), secret) {
		t.Fatal("the secret must be gone from the screen once its modal closes")
	}
	// What it does leave behind is the key and its scope, so the operation is
	// on the record without the credential being on it.
	if !strings.Contains(transcriptText(a), "ci-readonly") ||
		!strings.Contains(transcriptText(a), "read_only") {
		t.Fatalf("a completed create must name the key and its scope:\n%s", transcriptText(a))
	}
}

func TestRotateFlowSecretOnce(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)
	a.Keys.sel = 0
	name := a.Keys.keys[0].Name

	runCmd(t, a, a.route("keys rotate"), "refresh before rotate")
	if a.Modal == nil {
		t.Fatal("keys rotate must open a confirmation")
	}
	if !strings.Contains(a.Modal.Body,
		"Blast radius: every client holding the current secret fails immediately") {
		t.Fatalf("rotate body drifted from the expected wording: %q", a.Modal.Body)
	}
	if a.Modal.Placeholder != `type "`+name+`" to confirm` {
		t.Fatalf("rotate placeholder drifted: %q", a.Modal.Placeholder)
	}
	if cmd := a.Modal.handle("enter", a); cmd != nil {
		t.Fatal("rotate must not fire before the key's name is typed")
	}
	if a.Modal == nil {
		t.Fatal("an unconfirmed destructive modal stays open")
	}

	typeModal(a, a.Modal, name)
	msg := runCmd(t, a, a.Modal.handle("enter", a), "rotate")
	res, ok := msg.(modalResultMsg)
	if !ok || res.err != nil {
		t.Fatalf("rotate must reach the gateway, got %#v", msg)
	}
	secret := res.key.Key
	if secret == "" {
		t.Fatal("rotate returns the new secret exactly once")
	}
	if !strings.Contains(a.paneView(110, 18), secret) {
		t.Fatal("the rotated secret must be visible in its modal")
	}

	runCmd(t, a, a.Modal.handle("enter", a), "close rotate secret modal")
	if strings.Contains(transcriptText(a), secret) {
		t.Fatalf("the rotated secret must never enter the transcript:\n%s", transcriptText(a))
	}
	if strings.Contains(a.render(), secret) {
		t.Fatal("the rotated secret must be gone once its modal closes")
	}
	if k, _ := keyNamed(a, name); k.RotatedAt == nil {
		t.Fatal("the refreshed row must show the rotation")
	}
}

func TestRevokeUpdatesStateColumn(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)

	// Pick a key that is currently active, so "revoked" is a change.
	idx := -1
	for i, k := range a.Keys.keys {
		if api.KeyState(k) == "active" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("the fixture seeds at least one active key")
	}
	a.Keys.sel = idx
	name := a.Keys.keys[idx].Name

	runCmd(t, a, a.route("keys revoke"), "refresh before revoke")
	if a.Modal == nil {
		t.Fatal("keys revoke must open a confirmation")
	}
	if !strings.Contains(a.Modal.Body, "Revocation is immediate and cannot be undone.") ||
		!strings.Contains(a.Modal.Body, "revoked badge.") {
		t.Fatalf("revoke body drifted from the expected wording: %q", a.Modal.Body)
	}
	typeModal(a, a.Modal, "not-the-name")
	if cmd := a.Modal.handle("enter", a); cmd != nil {
		t.Fatal("a wrong phrase must not revoke")
	}
	a.Modal.handle("ctrl+u", a)
	typeModal(a, a.Modal, name)

	runCmd(t, a, a.Modal.handle("enter", a), "revoke")
	// The result arm refreshes the list; run that fetch too.
	runCmd(t, a, a.Keys.refresh(a), "refresh")

	k, found := keyNamed(a, name)
	if !found {
		t.Fatalf("a revoked key keeps its row: %+v", a.Keys.keys)
	}
	if got := api.KeyState(k); got != "revoked" {
		t.Fatalf("state column must read revoked after a revoke, got %q", got)
	}
	if !strings.Contains(a.paneView(120, 18), "revoked") {
		t.Fatalf("the table must render the new state:\n%s", a.paneView(120, 18))
	}
	if !strings.Contains(transcriptText(a), name) {
		t.Fatalf("a completed revoke must name the key:\n%s", transcriptText(a))
	}
}

func TestKeysDestructiveVerbsNeedASelection(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)
	a.Keys.keys, a.Keys.sel = nil, -1
	for _, verb := range []string{"keys rotate", "keys revoke"} {
		a.route(verb)
		a.update(keysListMsg{gen: a.Keys.gen, keys: nil})
		if a.Modal != nil {
			t.Fatalf("%q with nothing selected must not open a destructive modal", verb)
		}
		if lastRow(a).Glyph != "bad" {
			t.Fatalf("%q with nothing selected must say so: %#v", verb, lastRow(a))
		}
	}
}

func TestKeysScreenKeepsThePaneContract(t *testing.T) {
	a := fixtureApp(t)
	loadKeys(t, a)
	a.Keys.sel, a.Keys.detail = 1, true
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		a.Theme = theme.New(mode)
		for _, rows := range []int{1, 4, 9, 20} {
			for _, w := range []int{30, 60, 92, 126} {
				body := a.paneView(w, rows)
				if got := len(strings.Split(body, "\n")); got != rows {
					t.Fatalf("mode=%+v w=%d rows=%d returned %d lines", mode, w, rows, got)
				}
				for i, line := range strings.Split(body, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Fatalf("mode=%+v w=%d line %d overflows: %d cells %q", mode, w, i, lw, line)
					}
				}
			}
		}
	}
}
