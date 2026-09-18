package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

const defaultRejectionReason = "rejected"

// errorFailsOpen reports whether an ERROR from a plugin of the given type is
// swallowed so the request can continue.
//
// Only observability plugins qualify: logging and metrics watch the request, they
// do not gate it, so a dead log sink must never take down the request path.
// Everything that participates in the decision fails closed — a guardrail that
// could not run has approved nothing, an auth plugin that could not run has
// authenticated nobody, and a transform that could not run has left the payload
// unsanitised.
//
// This governs plugin failures only. A deliberate rejection is always honoured,
// whatever the plugin's type: a plugin sets Context.Reject only when it has decided
// to deny the request, and discarding that decision would make the documented
// Reject contract a lie.
func errorFailsOpen(pluginType PluginType) bool {
	switch pluginType {
	case TypeLogging, TypeMetrics:
		return true
	default:
		return false
	}
}

// handlePluginFailure applies the failure policy after a plugin stage runs, and
// keeps two different events apart: a plugin that DENIED the request, and a plugin
// that BROKE. The first is a RejectionError, the second a FailureError; the HTTP
// layer maps them to a client error and a server error respectively.
func (m *Manager) handlePluginFailure(p Plugin, stage Stage, pctx *Context, err error) error {
	// A rejection outranks an error: the plugin reached a verdict, so report the
	// verdict even if it also returned an error on its way out.
	if pctx.Reject {
		return m.rejectionErrorFor(p, stage, pctx, err)
	}
	if err == nil {
		return nil
	}
	if errorFailsOpen(p.Type()) {
		m.log.Warn("fail-open plugin error ignored", "plugin", p.Name(), "type", p.Type(), "stage", stage, "error", err)
		return nil
	}
	return &FailureError{Plugin: p.Name(), PluginType: p.Type(), Stage: stage, Err: err}
}

func (m *Manager) rejectionErrorFor(p Plugin, stage Stage, pctx *Context, err error) *RejectionError {
	return &RejectionError{
		Plugin:     p.Name(),
		Instance:   m.instanceID(p),
		PluginType: p.Type(),
		Stage:      stage,
		Reason:     rejectionReason(pctx, err),
	}
}

func rejectionReason(pctx *Context, err error) string {
	if pctx.Reason != "" {
		return pctx.Reason
	}
	if err != nil {
		return err.Error()
	}
	return defaultRejectionReason
}

// Manager manages plugin lifecycle and execution.
type Manager struct {
	log    *logger.Logger
	before []Plugin
	after  []Plugin
	onErr  []Plugin
	// instanceIDs maps a registered plugin instance to its operator-supplied id
	// (PluginConfig.ID). Keyed by pointer identity, never by the Plugin value
	// itself: a value-typed plugin holding a slice or map is unhashable, and Go
	// panics on such a key even when the map is empty. An instance registered at
	// several stages — one multi-stage plugin — carries one id. A non-pointer
	// plugin has no identity to key on and carries none. Guarded by mu, written
	// only at Register.
	instanceIDs map[pluginInstanceKey]string
	// emitMatch, when set, receives each guardrail match at the end of the stage
	// that recorded it, with whether a guardrail denied the request at that stage.
	// The gateway installs it (SetGuardrailMatchSink) so the manager
	// need not know how an Event is built or where it goes; nil means no emission.
	emitMatch   func(ctx context.Context, match GuardrailMatch, allowed bool)
	mu          sync.RWMutex
	lifecycleMu sync.Mutex
	lifecycle   *sync.Cond
	active      int
	closed      bool
}

// NewManager creates a new plugin manager with the given logger. A nil logger
// falls back to logger.Default().
func NewManager(log *logger.Logger) *Manager {
	if log == nil {
		log = logger.Default()
	}
	m := &Manager{log: log}
	m.lifecycle = sync.NewCond(&m.lifecycleMu)
	return m
}

// Acquire marks the manager as in use until the returned release function is
// called. Close waits for active users before releasing plugin resources.
func (m *Manager) Acquire() func() {
	m.lifecycleMu.Lock()
	m.ensureLifecycleLocked()
	if m.closed {
		m.lifecycleMu.Unlock()
		return func() {}
	}
	m.active++
	m.lifecycleMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.lifecycleMu.Lock()
			m.active--
			if m.active == 0 {
				m.lifecycle.Broadcast()
			}
			m.lifecycleMu.Unlock()
		})
	}
}

// Register registers a plugin at the given stage. It is RegisterWithID with no
// instance id — kept so existing callers, in this repo and out of tree, compile
// unchanged.
func (m *Manager) Register(stage Stage, p Plugin) error {
	return m.RegisterWithID(stage, p, "")
}

// RegisterWithID registers a plugin at the given stage under an operator-supplied
// instance id (see PluginConfig.ID), which the manager echoes on that instance's
// rejection and match signal and never interprets. An empty id is the same as
// Register.
//
// A plugin that declares which stages it can act at (StageRestricted) is
// refused at the others rather than registered into a no-op: binding a stage is
// the first moment the pair is knowable, and the alternative is a plugin that
// reports itself enabled for the life of the deployment and enforces nothing.
func (m *Manager) RegisterWithID(stage Stage, p Plugin, id string) error {
	if err := ValidateStage(p, stage); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	switch stage {
	case StageBeforeRequest:
		m.before = append(m.before, p)
	case StageAfterRequest:
		m.after = append(m.after, p)
	case StageOnError:
		m.onErr = append(m.onErr, p)
	default:
		return fmt.Errorf("unknown plugin stage: %s", stage)
	}
	if key, ok := instanceKeyOf(p); ok && id != "" {
		if m.instanceIDs == nil {
			m.instanceIDs = make(map[pluginInstanceKey]string)
		}
		m.instanceIDs[key] = id
	}
	m.log.Info("plugin registered", "name", p.Name(), "type", p.Type(), "stage", stage)
	return nil
}

// instanceID returns the operator-supplied id a plugin instance was registered
// under, or "" when it was registered without one.
func (m *Manager) instanceID(p Plugin) string {
	key, ok := instanceKeyOf(p)
	if !ok {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instanceIDs[key]
}

// SetGuardrailMatchSink installs the callback that receives each guardrail match
// at the end of its stage. Pass nil to disable emission. Set once at wiring,
// before serving.
func (m *Manager) SetGuardrailMatchSink(fn func(ctx context.Context, match GuardrailMatch, allowed bool)) {
	m.emitMatch = fn
}

// attributeGuardrailMatches stamps the matches one plugin recorded during its
// Execute with that plugin's identity, and collapses repeats.
//
// The plugin knows only the action; who matched is the framework's to state, the
// same reason it sets pctx.Stage. One signal per action per invocation: a
// warn/log rule is evaluated on every piece of text, so a long conversation
// would otherwise emit one event per message — identical events, since a match
// names the decision and never the text.
func (m *Manager) attributeGuardrailMatches(p Plugin, pctx *Context, matchStart int, rejectedBefore bool) {
	// A guardrail that denied is the only kind of rejection allowed answers for;
	// a rate limiter or a budget denying the same request is not a guardrail's
	// verdict. Recorded here because this is where the deciding plugin's type is
	// known — the stage flush sees only the shared Reject flag. Checked before
	// the matches are: a guardrail can deny without recording one, and it still
	// denied the request every other guardrail's match is reported against.
	//
	// THIS plugin must be the one that denied, which is why the flag is compared
	// against its value before Execute rather than just read. Reject stays set
	// once a request is denied, so a guardrail running afterwards — at the
	// on_error stage, which runs precisely because something denied — would
	// otherwise be recorded as having denied a request it only observed.
	if pctx.Reject && !rejectedBefore && p.Type() == TypeGuardrail {
		pctx.guardrailRejected = true
	}
	if len(pctx.GuardrailMatches) <= matchStart {
		return
	}

	id := m.instanceID(p)
	name := p.Name()
	// An in-place filter over the matches this plugin appended: kept's write
	// cursor never passes the read cursor, so the two safely share one backing
	// array. kept[matchStart:] is what has been kept from THIS plugin so far.
	kept := pctx.GuardrailMatches[:matchStart]
	for _, match := range pctx.GuardrailMatches[matchStart:] {
		if slices.ContainsFunc(kept[matchStart:], func(k GuardrailMatch) bool { return k.Action == match.Action }) {
			continue
		}
		match.Plugin, match.Instance = name, id
		kept = append(kept, match)
	}
	pctx.GuardrailMatches = kept
}

// flushGuardrailMatches emits every match recorded so far and clears them, so a
// stage reports its own matches and an agentic loop turn does not re-report an
// earlier turn's.
//
// allowed answers "did a GUARDRAIL deny this request, at THIS stage".
//
// Not "was it rejected at all": a rate limiter, a budget or an auth plugin
// denying the request says nothing about the guardrail that matched, and reading
// the shared Reject flag would report every warn and log match on such a request
// as a denial.
//
// And not "did the request ultimately succeed": each stage flushes its own
// matches, so a before_request match reports how the request fared on the way
// in. A guardrail rejecting the RESPONSE at after_request does not retroactively
// deny the prompt — they are verdicts on different content — and on a streamed
// response the after stage runs once the body is already delivered, so there is
// no later moment at which a single answer would be more final. Every event
// names its stage, so a consumer can tell which decision it is reading.
//
// Deferred by each stage runner, so it sees that stage's final verdict.
func (m *Manager) flushGuardrailMatches(ctx context.Context, pctx *Context) {
	if m.emitMatch != nil && len(pctx.GuardrailMatches) > 0 {
		allowed := !pctx.guardrailRejected
		for _, match := range pctx.GuardrailMatches {
			m.emitMatch(ctx, match, allowed)
		}
	}
	// A stage boundary: the next stage reaches its own verdict, so neither the
	// matches nor the verdict they were reported under carries into it. Cleared
	// even when nothing matched — a stage that recorded no match still ends here,
	// and leaving the flag set would deny the next stage's matches for it.
	pctx.GuardrailMatches = pctx.GuardrailMatches[:0]
	pctx.guardrailRejected = false
}

// RunBefore executes all before-request plugins. Fail-closed plugin errors or
// rejections abort the request; fail-open plugin failures are logged and ignored.
//
// No plugin can shorten this chain. Context.SkipProvider says the provider must not
// be called, not that the plugins behind it must not run — a cache that answered
// the request has said nothing about the rate limiter or the budget listed after
// it, and letting it speak for them is how a shipped config silently disabled every
// guardrail on a cache hit.
//
// The chain ends only on a rejection or a failure, and it ends this stage alone:
// the on_error stage runs before the abort is returned, so a request denied by
// policy still reaches whatever records requests. That guarantee lives here rather
// than at each call site because a caller that forgets it produces a request the
// operator has no record of — the shape this stage exists to prevent.
func (m *Manager) RunBefore(ctx context.Context, pctx *Context) error {
	defer m.flushGuardrailMatches(ctx, pctx)
	m.mu.RLock()
	plugins := m.before
	m.mu.RUnlock()
	for _, p := range plugins {
		err := m.executePlugin(ctx, p, pctx, StageBeforeRequest)
		if failureErr := m.handlePluginFailure(p, StageBeforeRequest, pctx, err); failureErr != nil {
			pctx.Error = failureErr
			// Flush THIS stage before the nested on_error stage runs. The deferred
			// flush would otherwise fire after RunOnError's, so the two stages
			// would report in the wrong order and the on_error stage would inherit
			// this stage's verdict instead of reaching its own.
			m.flushGuardrailMatches(ctx, pctx)
			m.RunOnError(ctx, pctx)
			return failureErr
		}
	}
	return nil
}

// RunBeforeLoopTurn runs the before-request plugins that must see EVERY provider
// call, for one turn of an agentic tool loop.
//
// The loop's turns after the first used to reach the provider with no plugin
// having seen them, because runMCPLoop calls the router directly and the
// before stage had already run once. The messages those turns carry include
// tool RESULTS returned by an external MCP server, so a configured content
// guardrail was inspecting the caller's prompt and nothing else — the same
// governance escape as an unguarded surface, one level in.
//
// Two types are deliberately excluded, and the exclusions are the whole reason
// this is not simply RunBefore:
//
//   - TypeTransform rewrites the request. A transform re-run mid-loop would
//     rewrite the model between turns, so the conversation would finish on a
//     different model than it started on.
//   - TypeLogging and TypeMetrics observe a REQUEST. Re-running them would
//     write one request-log row and one metric sample per turn, turning a
//     single request into N in every reporting surface.
//
// Everything else runs: a guardrail inspects what is about to be sent, and a
// rate limiter or budget sees a call it is meant to be bounding.
//
// A rejection ends the loop and is returned to the caller. A plugin FAILURE is
// handled exactly as it is at the ordinary stage, by the shared policy — this
// runs the same executePlugin and handlePluginFailure, so a fail-open plugin
// still fails open here.
func (m *Manager) RunBeforeLoopTurn(ctx context.Context, pctx *Context) error {
	defer m.flushGuardrailMatches(ctx, pctx)
	m.mu.RLock()
	plugins := m.before
	m.mu.RUnlock()
	for _, p := range plugins {
		switch p.Type() {
		case TypeTransform, TypeLogging, TypeMetrics:
			continue
		}
		err := m.executePlugin(ctx, p, pctx, StageBeforeRequest)
		if failureErr := m.handlePluginFailure(p, StageBeforeRequest, pctx, err); failureErr != nil {
			return failureErr
		}
		if pctx.Reject {
			return nil
		}
	}
	return nil
}

// RunAfter executes all after-request plugins. Fail-closed plugin errors or
// rejections abort the response; fail-open plugin failures are logged and ignored.
//
// It runs whether or not a provider was contacted: a response served from cache is
// still a served request, and suppressing the stage would lose it from every log,
// metric and audit surface. Plugins that must not act on a request that never
// reached a provider read Context.SkipProvider, which is still set here.
//
// Unlike RunBefore this does not run the on_error stage itself: its callers already
// do, because only they hold the measurements that stage records.
func (m *Manager) RunAfter(ctx context.Context, pctx *Context) error {
	defer m.flushGuardrailMatches(ctx, pctx)
	m.mu.RLock()
	plugins := m.after
	m.mu.RUnlock()
	for _, p := range plugins {
		err := m.executePlugin(ctx, p, pctx, StageAfterRequest)
		if failureErr := m.handlePluginFailure(p, StageAfterRequest, pctx, err); failureErr != nil {
			return failureErr
		}
	}
	return nil
}

// onErrorBudget bounds the detached on_error stage. Dropping the request's
// cancellation also drops its deadline, and a stage that records what happened
// must not be able to hold a goroutine open on an unresponsive store.
const onErrorBudget = 10 * time.Second

// RunOnError executes all on-error plugins.
//
// The stage runs on a context detached from the request's cancellation. It is
// the last thing a failed request does, and on the streaming path the client's
// connection is already gone by the time it runs — so the request context is
// cancelled, and every ctx-aware plugin here is dead on arrival. That is how a
// mid-stream failure came to write no on_error row at all: the request-logger's
// insert returned "context canceled" and the failure existed in no operator-
// facing surface, which is the exact shape this stage exists to prevent. Values
// and the trace context are preserved, so the row still carries the request's
// trace ID. Cancellation is replaced by onErrorBudget rather than removed.
func (m *Manager) RunOnError(ctx context.Context, pctx *Context) {
	defer m.flushGuardrailMatches(ctx, pctx)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), onErrorBudget)
	defer cancel()

	m.mu.RLock()
	plugins := m.onErr
	m.mu.RUnlock()
	for _, p := range plugins {
		if err := m.executePlugin(ctx, p, pctx, StageOnError); err != nil {
			m.log.Warn("on-error plugin error", "plugin", p.Name(), "error", err)
		}
	}
}

// executePlugin runs a single plugin under a child span opened through the
// observability seam. When the request carries a root span (pctx.Span) the
// child records the plugin name/kind/stage plus the rejection or error
// outcome; error messages are redacted by the seam per the configured
// privacy level. With the NoOp provider — or when no root span is set — the
// child is a no-op and adds effectively zero overhead.
func (m *Manager) executePlugin(ctx context.Context, p Plugin, pctx *Context, stage Stage) (err error) {
	// The stage the plugin is running in is the framework's to state, not the
	// plugin's to guess from which fields are populated.
	pctx.Stage = stage

	var span observability.Span
	if pctx.Span != nil {
		ctx, span = pctx.Span.StartChild(ctx, "plugin."+string(stage)+"."+p.Name(), observability.SpanKindInternal)
		span.SetAttribute(observability.AttrFerroPluginName, p.Name())
		span.SetAttribute(observability.AttrFerroPluginKind, string(p.Type()))
		span.SetAttribute(observability.AttrFerroPluginStage, string(stage))
		defer span.End()
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			stack := debug.Stack()
			err = fmt.Errorf("plugin %s panicked at %s", p.Name(), stage)
			m.log.Error("plugin panicked",
				"plugin", p.Name(),
				"stage", stage,
				"panic", recovered,
				"stack", string(stack),
			)
			if span != nil {
				span.SetAttribute(observability.AttrFerroPluginOutcome, "error")
				span.SetError(err)
			}
		}
	}()

	matchStart := len(pctx.GuardrailMatches)
	// Deferred rather than written after Execute: a plugin that records a match
	// and then panics would otherwise leave that match unattributed, and the
	// stage flush would emit it with no plugin name or instance.
	defer m.attributeGuardrailMatches(p, pctx, matchStart, pctx.Reject)

	err = p.Execute(ctx, pctx)

	if span != nil {
		switch {
		case pctx.Reject:
			span.SetAttribute(observability.AttrFerroPluginOutcome, "rejected")
			if pctx.Reason != "" {
				span.SetAttribute(observability.AttrFerroPluginReason, pctx.Reason)
			}
		case err != nil:
			span.SetAttribute(observability.AttrFerroPluginOutcome, "error")
			span.SetError(err)
		default:
			span.SetAttribute(observability.AttrFerroPluginOutcome, "ok")
		}
	}
	return err
}

// Close starts closing the manager, releases each registered plugin instance
// once, and clears the manager. If requests are still using this manager, Close
// returns immediately and cleanup runs after the active users drain.
func (m *Manager) Close() error {
	m.lifecycleMu.Lock()
	m.ensureLifecycleLocked()
	if m.closed {
		m.lifecycleMu.Unlock()
		return nil
	}
	m.closed = true
	if m.active > 0 {
		m.lifecycleMu.Unlock()
		go m.closeWhenDrained()
		return nil
	}
	m.lifecycleMu.Unlock()

	return m.closePlugins()
}

func (m *Manager) closeWhenDrained() {
	m.lifecycleMu.Lock()
	m.ensureLifecycleLocked()
	for m.active > 0 {
		m.lifecycle.Wait()
	}
	m.lifecycleMu.Unlock()

	if err := m.closePlugins(); err != nil {
		m.log.Warn("deferred plugin close failed", "error", err)
	}
}

func (m *Manager) closePlugins() error {
	m.mu.Lock()
	plugins := make([]Plugin, 0, len(m.before)+len(m.after)+len(m.onErr))
	plugins = append(plugins, m.before...)
	plugins = append(plugins, m.after...)
	plugins = append(plugins, m.onErr...)
	m.before = nil
	m.after = nil
	m.onErr = nil
	m.mu.Unlock()

	var err error
	for _, p := range uniquePluginInstances(plugins) {
		if closeErr := p.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("plugin %s close failed: %w", p.Name(), closeErr))
		}
	}
	return err
}

func (m *Manager) ensureLifecycleLocked() {
	if m.lifecycle == nil {
		m.lifecycle = sync.NewCond(&m.lifecycleMu)
	}
}

func uniquePluginInstances(plugins []Plugin) []Plugin {
	unique := make([]Plugin, 0, len(plugins))
	seen := make(map[pluginInstanceKey]struct{}, len(plugins))
	for _, p := range plugins {
		key, ok := instanceKeyOf(p)
		if !ok {
			unique = append(unique, p)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, p)
	}
	return unique
}

// instanceKeyOf returns the pointer identity of a plugin instance. ok is false
// for a nil or non-pointer plugin, which has no identity to compare on.
func instanceKeyOf(p Plugin) (pluginInstanceKey, bool) {
	v := reflect.ValueOf(p)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return pluginInstanceKey{}, false
	}
	return pluginInstanceKey{typ: v.Type(), ptr: v.Pointer()}, true
}

type pluginInstanceKey struct {
	typ reflect.Type
	ptr uintptr
}

// HasPlugins returns true if any plugins are registered.
func (m *Manager) HasPlugins() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.before)+len(m.after)+len(m.onErr) > 0
}

// HasBeforeRequestTransform reports whether any before_request plugin is a
// transform, and so whether the request the provider sees may differ from the
// one the caller sent.
//
// Execute may mutate the request it is handed, and a transform is the plugin
// type whose job that is — so a gateway that decides anything from the request
// BEFORE this stage has to know whether the stage can still change the answer.
//
// The plugin's own Type() is the authority, the same one the fail-open policy
// reads, rather than the type written in config: a plugin does not have to
// agree with what an operator labelled it, and only one of the two is the code
// that will run.
// A transform that declares ModelPreserving does not count: it reports
// TypeTransform because it rewrites the request or short-circuits the provider
// call, not because it can change which model is routed. The response cache is
// the case that forced this — enabling it used to turn the admission check off
// for the whole process, so an unroutable model spent a rate limiter's tokens
// and a budget's money on its way to a 404 it was always going to get.
//
// The declaration is an opt-OUT on purpose. A transform that says nothing keeps
// the stand-down, so a plugin built outside this repository — which may well
// rewrite the model, and cannot know to declare anything — is unaffected. An
// opt-in would silently reintroduce the alias-404 the stand-down exists to
// prevent.
func (m *Manager) HasBeforeRequestTransform() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.before {
		if p.Type() != TypeTransform {
			continue
		}
		if _, preserving := p.(ModelPreserving); !preserving {
			return true
		}
	}
	return false
}

// HasBeforeRequestGuardrail reports whether any before_request plugin is a
// guardrail that READS REQUEST CONTENT, and so whether this deployment screens
// request content before it reaches a provider.
//
// It is the question a surface has to ask when it cannot show a guardrail the
// content it would screen — the /v1/* pass-through handed a multipart upload,
// an audio payload, or anything else that does not project to text. A guardrail
// with nothing to match on APPROVES, and that approval is indistinguishable
// from one it actually made, so the surface must refuse before the stage rather
// than read the empty pass as consent. See Gateway.RoutePassthrough.
//
// The plugin's own Type() is the authority, exactly as it is for
// HasBeforeRequestTransform: reading plugins[].type from config would let a
// mislabelled guardrail turn the refusal off, which is the one direction that
// is not safe to be wrong in.
//
// A guardrail that declares ContentAgnostic does not count. TypeGuardrail names
// a plugin's enforcement role, not what it reads, so it lumped `word-filter` in
// with `max-token` — and a deployment running only the latter had uninspectable
// bodies refused although nothing would have inspected them. The declaration is
// an opt-OUT for the reason spelled out on ContentAgnostic: an undeclared
// guardrail is assumed to read content and still triggers the refusal.
func (m *Manager) HasBeforeRequestGuardrail() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.before {
		if p.Type() != TypeGuardrail {
			continue
		}
		if agnostic, ok := p.(ContentAgnostic); ok && agnostic.IgnoresRequestContent() {
			continue
		}
		return true
	}
	return false
}
