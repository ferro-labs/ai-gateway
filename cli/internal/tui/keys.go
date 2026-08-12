package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

// The keys screen is the console's only destructive surface, and every choice
// here follows from that:
//
//   - Rotate and revoke are gated on typing the key's exact name. The wording
//     of each gate is the approved blast-radius copy, because what an operator
//     needs at that moment is not "are you sure" but "here is what breaks".
//   - A secret exists in exactly one place: the modal that was handed it. It is
//     never pushed to the transcript, never recorded in history, and is gone
//     from the process the moment that modal closes. What the transcript gets
//     instead names the key and its scope, so the action is on the record and
//     the credential is not.
//   - The table shows the wire's masked `key`, never a full secret — reads are
//     masked head8…tail4 by the gateway and this screen does no unmasking.
//
// There is no state field on the wire; api.KeyState derives revoked/expired/
// active from revoked_at, expires_at and active. Nothing here re-derives it.

// defaultExpiry is the wizard's prefilled ceiling. An unexpiring admin key is
// a decision, so it costs a deliberate edit rather than a default.
const defaultExpiry = "720h"

// The actions this screen can be asked to perform. One string travels a long
// way — from the subcommand the operator typed, through pendingVerb, into
// modalResultMsg.kind in msgs.go, and back out to the arm that decides whether
// a secret modal opens — and every hop is a plain string comparison. Naming
// them keeps the three switches on one vocabulary. The alternative spellings
// (`new`, `delete`) stay literals where they are accepted: they are input the
// operator may type, not values this package passes around.
const (
	actionGet    = "get"
	actionCreate = "create"
	actionRotate = "rotate"
	actionRevoke = "revoke"
)

// The labels the modals share. A field named one thing in the create wizard
// and another in the detail strip reads as two different fields.
const (
	kvName     = "name"
	kvScope    = "scope"
	kvLastUsed = "last used"

	labelNext = "next"
)

// The create wizard's three step titles. One prefix, three steps: the title is
// the only thing telling an operator where they are in the chain.
const (
	titleCreateName   = "Create API key · name"
	titleCreateScope  = "Create API key · scope"
	titleCreateExpiry = "Create API key · expiry"
)

// keysScreen is the API keys pane: a table, an optional detail strip, and the
// modal flows that mutate it.
type keysScreen struct {
	keys   []api.Key
	sel    int
	detail bool
	err    error

	// gen invalidates in-flight work from a visit the operator has left or a
	// command that has been superseded, the same contract the shell's pollGen
	// has.
	gen int

	pendingVerb string
	pendingArg  string

	draft keyDraft
}

// keyDraft is the create wizard's accumulated answers. It holds no secret:
// the secret does not exist until the gateway answers, and then it lives only
// in the modal built from that answer.
type keyDraft struct {
	name, scope string
	expires     *time.Time
}

// ------------------------------------------------------------------ lifecycle

// enter opens the pane and dispatches the subcommand the operator typed.
// `keys` alone lists; `keys create|rotate|revoke` opens a flow.
func (s *keysScreen) enter(a *App, rest string) tea.Cmd {
	if a.Screen != ScreenKeys {
		s.detail = false
	}
	s.gen++
	s.pendingVerb, s.pendingArg = "", ""
	a.Screen = ScreenKeys
	cmd := s.fetch(a)

	f := strings.Fields(rest)
	if len(f) == 0 {
		return cmd
	}
	arg := strings.Join(f[1:], " ")
	switch strings.ToLower(f[0]) {
	case "list":
	case actionGet:
		s.pendingVerb, s.pendingArg = actionGet, arg
	case actionCreate, "new":
		s.openCreate(a)
	case actionRotate:
		s.pendingVerb, s.pendingArg = actionRotate, arg
	case actionRevoke, "delete":
		s.pendingVerb, s.pendingArg = actionRevoke, arg
	default:
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad,
			Text: fmt.Sprintf("keys: unknown action %q — list, get, create, rotate or revoke", f[0])})
		return nil
	}
	return cmd
}

// leave drops whatever the visit had in flight. There is no stream and no tick
// here, so bumping the generation is the whole of it — a list fetch that lands
// after the operator moved on must not repopulate a pane they are not looking
// at, and a modal must not outlive its screen.
func (s *keysScreen) leave() { s.gen++ }

// refresh re-reads the list. It is the only way rows change: this screen never
// patches a row locally from a mutation's response, because the gateway is the
// authority on a key's state and a local guess is how a revoked key keeps
// rendering as active.
func (s *keysScreen) refresh(a *App) tea.Cmd {
	s.gen++
	return s.fetch(a)
}

func (s *keysScreen) fetch(a *App) tea.Cmd {
	if a.Client == nil {
		s.err = errNoConnection
		return nil
	}
	return fetchKeys(a.Client, s.gen)
}

func (s *keysScreen) update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case keysListMsg:
		if m.gen != s.gen {
			return nil
		}
		s.err = m.err
		if m.err == nil {
			s.keys = m.keys
			s.sel = clampIdx(s.sel, len(s.keys))
			return s.runPending(a)
		}
		s.pendingVerb, s.pendingArg = "", ""
		return nil

	case modalResultMsg:
		if m.gen != s.gen {
			return nil
		}
		return s.result(a, m)
	}
	return nil
}

func (s *keysScreen) runPending(a *App) tea.Cmd {
	verb, arg := s.pendingVerb, s.pendingArg
	s.pendingVerb, s.pendingArg = "", ""
	switch verb {
	case actionGet:
		if s.selectNamed(arg) {
			return nil
		}
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad, Text: fmt.Sprintf("keys get: no key named %q", arg)})
	case actionRotate:
		s.open(a, verb, arg, s.openRotate)
	case actionRevoke:
		s.open(a, verb, arg, s.openRevoke)
	}
	return nil
}

// key returns the selected row.
func (s *keysScreen) key() (api.Key, bool) {
	if s.sel < 0 || s.sel >= len(s.keys) {
		return api.Key{}, false
	}
	return s.keys[s.sel], true
}

func (s *keysScreen) selectNamed(arg string) bool {
	for i, k := range s.keys {
		if k.Name == arg || k.ID == arg {
			s.sel, s.detail = i, true
			return true
		}
	}
	return false
}

// handleKey is the pane's own keyboard. It claims a key only when the composer
// is empty, so typing a verb from this screen always beats moving a cursor.
func (s *keysScreen) handleKey(key string) bool {
	switch key {
	case keyUp:
		s.sel = clampIdx(s.sel-1, len(s.keys))
	case keyDown:
		s.sel = clampIdx(s.sel+1, len(s.keys))
	case keyEnter:
		s.detail = s.sel >= 0 && s.sel < len(s.keys)
	case keyEsc:
		if !s.detail {
			return false // esc means "back" once there is no strip to close
		}
		s.detail = false
	default:
		return false
	}
	return true
}

// -------------------------------------------------------------------- flows

// open resolves the key a destructive verb names — by name, by id, or by the
// current selection — and refuses rather than guessing when it names none.
func (s *keysScreen) open(a *App, verb, arg string, f func(*App, api.Key)) {
	if arg != "" {
		for _, k := range s.keys {
			if k.Name == arg || k.ID == arg {
				f(a, k)
				return
			}
		}
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad,
			Text: fmt.Sprintf("keys %s: no key named %q", verb, arg)})
		return
	}
	k, ok := s.key()
	if !ok {
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad,
			Text: fmt.Sprintf("keys %s: no key selected — run keys, pick a row with ↑/↓, then keys %s", verb, verb)})
		return
	}
	f(a, k)
}

// openCreate is step 1 of the wizard. Each step's OnConfirm builds the next,
// so the flow is a chain of modals rather than a step counter to keep in sync.
func (s *keysScreen) openCreate(a *App) {
	s.draft = keyDraft{scope: api.ScopeReadOnly}
	a.Modal = &Modal{
		Title:       titleCreateName,
		Body:        "Names are how log rows resolve after a key is revoked.",
		Input:       true,
		Placeholder: "prod-ingest",
		OKLabel:     labelNext,
		OnConfirm: func(a *App, v string) tea.Cmd {
			if v == "" {
				s.openCreate(a) // a nameless key is the one thing this step exists to prevent
				return nil
			}
			s.draft.name = v
			s.openScope(a)
			return nil
		},
	}
}

func (s *keysScreen) openScope(a *App) {
	a.Modal = &Modal{
		Title: titleCreateScope,
		Body: "Scope is always sent explicitly, so the key carries the scope you\n" +
			"chose rather than the gateway's default.",
		Rows:        []ModalKV{{K: kvName, V: s.draft.name, Bold: true}},
		Options:     []string{api.ScopeReadOnly, api.ScopeAdmin},
		OptionLabel: kvScope,
		OKLabel:     labelNext,
		OnConfirm: func(a *App, scope string) tea.Cmd {
			s.draft.scope = scope
			s.openExpiry(a, "")
			return nil
		},
	}
}

func (s *keysScreen) openExpiry(a *App, errMsg string) {
	a.Modal = &Modal{
		Title: titleCreateExpiry,
		Body:  "Default 720h. An unexpiring admin key needs a written exception.",
		Rows: []ModalKV{
			{K: kvName, V: s.draft.name, Bold: true},
			{K: kvScope, V: s.draft.scope},
		},
		Input:       true,
		Value:       defaultExpiry,
		Placeholder: "720h · empty for no expiry",
		Err:         errMsg,
		OKLabel:     actionCreate,
		OnConfirm: func(a *App, v string) tea.Cmd {
			exp, err := parseExpiry(v)
			if err != nil {
				s.openExpiry(a, err.Error())
				return nil
			}
			s.draft.expires = exp
			return createKey(a.Client, s.gen, api.KeyCreateRequest{
				Name:      s.draft.name,
				Scopes:    []string{s.draft.scope},
				ExpiresAt: exp,
			})
		},
	}
}

func (s *keysScreen) openRotate(a *App, k api.Key) {
	a.Modal = &Modal{
		Title: "Rotate " + k.Name + "?",
		Body: "Blast radius: every client holding the current secret fails immediately\n" +
			"until it is redeployed with the new one.",
		Rows: []ModalKV{
			{K: "id", V: k.ID},
			{K: kvScope, V: strings.Join(k.Scopes, ",")},
			{K: kvLastUsed, V: lastUsed(k), Warn: true},
		},
		Input:       true,
		Placeholder: `type "` + k.Name + `" to confirm`,
		Confirm:     k.Name,
		OKLabel:     actionRotate,
		Danger:      true,
		OnConfirm: func(a *App, _ string) tea.Cmd {
			return rotateKey(a.Client, s.gen, k.ID)
		},
	}
}

func (s *keysScreen) openRevoke(a *App, k api.Key) {
	a.Modal = &Modal{
		Title: "Revoke " + k.Name + "?",
		Body: "Revocation is immediate and cannot be undone. Existing log rows keep\n" +
			"their api_key_id and will render with a revoked badge.",
		Rows: []ModalKV{
			{K: "id", V: k.ID},
			{K: kvScope, V: strings.Join(k.Scopes, ",")},
		},
		Input:       true,
		Placeholder: `type "` + k.Name + `" to confirm`,
		Confirm:     k.Name,
		OKLabel:     actionRevoke,
		Danger:      true,
		OnConfirm: func(a *App, _ string) tea.Cmd {
			return revokeKey(a.Client, s.gen, k.ID, k.Name, strings.Join(k.Scopes, ","))
		},
	}
}

// result turns a finished mutation into the next thing on screen. A failure is
// pushed to the transcript in the gateway's own words and the operator is
// returned to it, because a red row on a pane nobody is looking at is a
// failure that happened silently.
func (s *keysScreen) result(a *App, m modalResultMsg) tea.Cmd {
	if m.err != nil {
		s.err = m.err
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad, Text: "keys " + m.kind + ": " + m.err.Error()})
		return nil
	}
	switch m.kind {
	case actionRevoke:
		a.Transcript.Push(TranscriptRow{Glyph: kindOK, Text: "revoked " + m.name + " (" + m.scope + ")"})
		return s.refresh(a)
	case actionCreate, actionRotate:
		s.openSecret(a, m.kind, m.key)
		return nil
	}
	return nil
}

// openSecret is the one place a secret is ever rendered. Confirm and cancel run
// the same closure: leaving by esc still records what happened and still drops
// the secret, because esc is a real way out and not an error path.
func (s *keysScreen) openSecret(a *App, kind string, k *api.Key) {
	if k == nil {
		s.err = fmt.Errorf("keys %s returned no key", kind)
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad, Text: s.err.Error()})
		return
	}
	scope := strings.Join(k.Scopes, ",")
	// The list is re-read rather than patched from this response: it carries a
	// secret we are about to forget on purpose, and the gateway is the
	// authority on what the key now looks like.
	done := func(a *App) tea.Cmd {
		a.Transcript.Push(TranscriptRow{Glyph: kindOK, Text: summarize(kind, k, scope)})
		a.Screen = ScreenKeys
		return s.refresh(a)
	}

	if kind == actionRotate {
		a.Modal = &Modal{
			Title: "Rotated " + k.Name,
			Body:  "The previous secret stopped authenticating immediately.",
			Rows: []ModalKV{
				{K: "new secret", V: k.Key, Accent: true, Bold: true},
				{K: "id", V: k.ID},
				{K: "blast radius", V: "every client using the old secret", Warn: true},
			},
			OKLabel:   "done",
			OnConfirm: func(a *App, _ string) tea.Cmd { return done(a) },
			OnCancel:  done,
		}
		return
	}

	a.Modal = &Modal{
		Title: "Key created",
		Body:  "Shown exactly once. Copy it now — the gateway stores only a hash.",
		Rows: []ModalKV{
			{K: kvName, V: k.Name, Bold: true},
			{K: "secret", V: k.Key, Accent: true, Bold: true},
			{K: "copy", V: "it will not be shown again"},
		},
		OKLabel:   "done",
		OnConfirm: func(a *App, _ string) tea.Cmd { return done(a) },
		OnCancel:  done,
	}
}

// summarize is the transcript row a finished flow leaves behind. It names the
// key, its scope and its ceiling — everything except the credential.
func summarize(kind string, k *api.Key, scope string) string {
	verb := map[string]string{actionCreate: "created", actionRotate: "rotated"}[kind]
	out := verb + " " + k.Name + " (" + scope
	if k.ExpiresAt != nil {
		out += ", expires " + k.ExpiresAt.Local().Format(time.DateOnly)
	} else if kind == actionCreate {
		out += ", no expiry"
	}
	return out + ")"
}

// --------------------------------------------------------------------- view

// keyColumns is the table, widest-useful first. NAME flexes because it is the
// column an operator reads down; everything else is a fixed field.
func keyColumns() []column {
	return []column{
		{head: colName, w: 18, flex: true},
		{head: "KEY", w: 17},
		{head: "SCOPE", w: 10},
		{head: "EXPIRES", w: 10},
		{head: colState, w: 7},
	}
}

func (s *keysScreen) view(a *App, w, rows int) string {
	th := a.Theme
	lines := []string{s.subLine(a, w)}

	if s.err != nil {
		return fill(append(lines, "", th.Bad.Render("keys: "+s.err.Error())), w, rows)
	}

	var strip []string
	if s.detail {
		if k, ok := s.key(); ok {
			strip = detailStrip(a, "Key "+k.Name, keyDetail(a, k), w)
		}
	}

	// The table gets whatever the sub-line and the strip leave. It never
	// scrolls off its own header: the header is dropped last, not first.
	body := max(rows-len(lines)-len(strip), 0)
	cols := layout(keyColumns(), w)
	if body > 0 {
		lines = append(lines, th.Dim.Render(headerLine(cols)))
		body--
	}
	if len(s.keys) == 0 {
		lines = append(lines, th.Dim.Render("this gateway has no API keys"))
	}
	for _, i := range window(len(s.keys), s.sel, body) {
		lines = append(lines, s.row(a, cols, i, w))
	}
	return fill(append(padTo(lines, rows-len(strip)), strip...), w, rows)
}

func (s *keysScreen) subLine(a *App, w int) string {
	th := a.Theme
	left := th.Dim.Render("keys create · keys rotate · keys revoke")
	right := th.Dim.Render(fmt.Sprintf("%d keys", len(s.keys)))
	if len(s.keys) == 0 {
		right = ""
	}
	return gapPad(w, " "+left, right)
}

func (s *keysScreen) row(a *App, cols []column, i, w int) string {
	th, k := a.Theme, s.keys[i]
	state := api.KeyState(k)

	stateStyle := th.OK
	switch state {
	case api.KeyStateRevoked:
		stateStyle = th.Bad
	case api.KeyStateExpired:
		stateStyle = th.Warn
	}
	expires := "never"
	if k.ExpiresAt != nil {
		expires = k.ExpiresAt.Local().Format(time.DateOnly)
	}

	line := rowLine(cols, []cell{
		{v: k.Name, style: th.Bright},
		{v: k.Key, style: th.Dim},
		{v: strings.Join(k.Scopes, ","), style: th.Text},
		{v: expires, style: th.Dim},
		{v: state, style: stateStyle},
	})
	if i == s.sel {
		return th.Selected.Render(padRight(line, w))
	}
	return line
}

// keyDetail is the strip's rows. Every value is from the wire; the actions row
// names the verbs rather than binding a key to them, because the composer is
// the console's only action surface.
func keyDetail(_ *App, k api.Key) []ModalKV {
	state := api.KeyState(k)
	row := ModalKV{K: "state", V: state}
	switch state {
	case api.KeyStateRevoked, api.KeyStateExpired:
		row.Warn = true
	}
	return []ModalKV{
		{K: "id", V: k.ID, Bold: true},
		{K: "key", V: k.Key},
		{K: kvScope, V: strings.Join(k.Scopes, ",")},
		{K: "created", V: table.FmtTime(k.CreatedAt)},
		{K: kvLastUsed, V: lastUsed(k)},
		{K: "expires", V: table.FmtTimePtr(k.ExpiresAt)},
		row,
		{K: "actions", V: "keys rotate · keys revoke", Accent: true},
	}
}

func lastUsed(k api.Key) string {
	if k.LastUsedAt == nil {
		return "never"
	}
	return fmt.Sprintf("%s · %s requests", table.FmtTime(*k.LastUsedAt), comma(int(k.UsageCount)))
}

// ------------------------------------------------------------------ helpers

// parseExpiry reads the wizard's ceiling. Empty is a real answer — no expiry —
// so it is not an error; anything else must be a duration Go can parse.
func parseExpiry(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return nil, fmt.Errorf("%q is not a duration — try 720h, 30m, or empty for no expiry", v)
	}
	if d <= 0 {
		return nil, fmt.Errorf("%q expires in the past", v)
	}
	t := time.Now().Add(d).UTC()
	return &t, nil
}

func clampIdx(i, n int) int {
	if n == 0 {
		return -1
	}
	return min(max(i, 0), n-1)
}

// ---------------------------------------------------- shared table furniture
//
// Used by this screen and by logs.go. Both draw a fixed-width table into a
// pane that owes an exact line count, so the one thing neither may do is wrap:
// a folded line breaks the count and every frame below it. Columns are
// therefore dropped, right to left, until the row fits.

type column struct {
	head  string
	w     int
	flex  bool // absorbs the width the fixed columns leave
	right bool // numerics align right
}

type cell struct {
	v     string
	style lipgloss.Style
}

// layout returns the columns that fit w cells, with the flex column resized to
// whatever is left. It keeps at least the first column, which is the one that
// identifies the row.
func layout(cols []column, w int) []column {
	const gap = 1
	fits := func(c []column) int {
		total := 0
		for i, col := range c {
			total += col.w
			if i > 0 {
				total += gap
			}
		}
		return total
	}
	out := append([]column(nil), cols...)
	for len(out) > 1 && fits(out) > w {
		out = out[:len(out)-1]
	}
	if slack := w - fits(out); slack != 0 {
		for i := range out {
			if out[i].flex {
				out[i].w = max(out[i].w+slack, 4)
				break
			}
		}
	}
	return out
}

func headerLine(cols []column) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		if c.right {
			parts = append(parts, padLeft(c.head, c.w))
			continue
		}
		parts = append(parts, padRight(c.head, c.w))
	}
	return strings.Join(parts, " ")
}

func rowLine(cols []column, cells []cell) string {
	parts := make([]string, 0, len(cols))
	for i, c := range cols {
		if i >= len(cells) {
			break
		}
		v := cells[i].v
		if c.right {
			v = padLeft(v, c.w)
		} else {
			v = padRight(v, c.w)
		}
		parts = append(parts, cells[i].style.Render(v))
	}
	return strings.Join(parts, " ")
}

// window returns the indices of the n rows to draw, scrolled so the selection
// stays on screen. A table that cannot show its selection is a table whose
// cursor keys do nothing visible.
func window(total, sel, n int) []int {
	if n <= 0 || total == 0 {
		return nil
	}
	start := 0
	if sel >= n {
		start = sel - n + 1
	}
	if end := min(start+n, total); end-start > 0 {
		out := make([]int, 0, end-start)
		for i := start; i < end; i++ {
			out = append(out, i)
		}
		return out
	}
	return nil
}

// detailStrip is the bottom-anchored inspector both tables share.
func detailStrip(a *App, title string, rows []ModalKV, w int) []string {
	th := a.Theme
	out := append(make([]string, 0, 2+len(rows)),
		th.Border.Render(a.rule(max(w, 0))),
		gapPad(w, th.Bright.Bold(th.Mode.Color).Render(title), th.Dim.Render("esc close")),
	)
	for _, r := range rows {
		out = append(out, th.Dim.Render(padRight(r.K, modalKeyCol))+kvStyle(th, r).Render(r.V))
	}
	return clampLines(out, w)
}

// padTo pads or trims to exactly n lines, so a view can reserve the space a
// bottom-anchored strip will occupy before it appends one.
func padTo(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines[:n]
}
