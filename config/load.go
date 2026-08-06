package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/tracingpolicy"
	pubmcp "github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"go.yaml.in/yaml/v3"
)

// LoadConfig reads and parses a config file from the given path.
// Supported formats: JSON (.json), YAML (.yaml, .yml).
//
// Decoding is strict: an unknown or misspelled key against the typed schema is
// rejected rather than silently ignored, so a typo cannot quietly disable the
// setting it was meant to change. Free-form config blocks (plugin and exporter
// "config" maps) accept arbitrary keys and are preserved verbatim. Exactly one
// document is permitted: trailing JSON data or a second YAML document (after
// "---") is rejected rather than silently dropped. The returned Config is
// Normalize-d so it carries its effective defaults.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: config path is an operator-supplied startup argument, not request input
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		// An empty document decodes to io.EOF; treat it as an empty config so
		// validation (not decoding) reports the missing required fields.
		if err := dec.Decode(&cfg); err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("parsing YAML config: %w", err)
			}
		} else if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
			// A valid first document must be the only one. Reject a trailing
			// second document rather than silently ignoring it, mirroring the
			// JSON path's trailing-data rejection.
			return nil, fmt.Errorf("parsing YAML config: unexpected additional document; only one is allowed")
		}
	case ".json":
		if err := DecodeJSONStrict(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing JSON config: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config file extension %q: use .json, .yaml, or .yml", ext)
	}

	cfg.Normalize()
	if cfg.APIVersion != CurrentAPIVersion {
		logger.Default().Warn("config: unrecognized apiVersion; proceeding for forward compatibility",
			"apiVersion", cfg.APIVersion, "expected", CurrentAPIVersion)
	}

	return &cfg, nil
}

// DecodeJSONStrict decodes one JSON config document into cfg, rejecting unknown
// keys and data trailing the top-level object. It is the single decoder for
// every way a JSON config enters the gateway — a file read by LoadConfig and a
// request body applied through PUT/POST /admin/config — so the two cannot
// disagree about which configs are valid.
//
// It reports EVERY unknown key, not the first. encoding/json's
// DisallowUnknownFields returns on the first one, so a config with three typos
// cost three edit-and-restart cycles while the same config written in YAML named
// all three at once. On the unknown-field path only, the document is decoded a
// second time with the YAML decoder purely to enumerate the rest: JSON is valid
// YAML, and every json tag in the schema has an identical yaml tag, which
// TestConfigSchemaTagsMatchAcrossFormats holds true. encoding/json stays the
// decoder, so JSON value semantics are untouched and the second pass never runs
// on a config that decodes.
//
// Known ceiling: encoding/json matches keys case-insensitively and yaml.v3 does
// not, so a document mixing a genuine typo with a case-variant spelling of a
// real key ("Targets") lists the variant too. That is deliberately not filtered
// out. The second pass only ever runs on a document encoding/json has already
// rejected, so this cannot turn an accepted config into a rejected one; it can
// only add a line to a rejection, and the correction it invites — the canonical
// lower-case key — is the right edit either way. Suppressing it would mean
// re-resolving every key against the schema at its own nesting depth, which is
// the decoder's job twice over for a degenerate input.
func DecodeJSONStrict(data []byte, cfg *Config) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return withEveryUnknownField(data, err)
	}
	// Reject trailing data after the top-level object, matching the strictness
	// of a whole-document json.Unmarshal.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after top-level object")
	}
	return nil
}

// withEveryUnknownField upgrades encoding/json's first-unknown-key error to the
// full list, and returns the original error unchanged for every other failure
// (a type mismatch, malformed JSON) so those keep their stdlib wording.
func withEveryUnknownField(data []byte, jsonErr error) error {
	if !strings.HasPrefix(jsonErr.Error(), "json: unknown field ") {
		return jsonErr
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var typeErr *yaml.TypeError
	if err := dec.Decode(new(Config)); errors.As(err, &typeErr) {
		if msg := unknownFieldsMessage(typeErr.Errors); msg != "" {
			return errors.New(msg)
		}
	}
	// The second pass failed differently than the first (a duplicate key, say).
	// The first error is the one that describes this document.
	return jsonErr
}

// unknownFieldsMessage renders yaml.v3's accumulated decode errors as one
// unknown-key list, keeping the quoted key of encoding/json's own message and
// adding the line number the YAML pass supplies.
//
// yaml.v3 emits "line N: field X not found in type T" for an unknown key and
// accumulates type mismatches into the same TypeError; only the former belongs
// under this heading, so parsing that shape is also what filters the rest out.
// Returns "" when nothing matched, leaving the caller with the original error.
func unknownFieldsMessage(errs []string) string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		rest, ok := strings.CutPrefix(e, "line ")
		if !ok {
			continue
		}
		line, rest, ok := strings.Cut(rest, ": field ")
		if !ok {
			continue
		}
		name, _, ok := strings.Cut(rest, " not found in type ")
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%q (line %s)", name, line))
	}
	if len(parts) == 0 {
		return ""
	}
	return "json: unknown fields: " + strings.Join(parts, ", ")
}

// ValidateConfig validates a Config for correctness.
func ValidateConfig(cfg Config) error {
	// Validate against normalized defaults (e.g. an omitted strategy mode means
	// single). cfg is passed by value, so normalizing this copy leaves the
	// caller's Config untouched while keeping validation consistent with the
	// Config LoadConfig returns.
	cfg.Normalize()

	if len(cfg.Targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}

	for _, t := range cfg.Targets {
		if err := validateTargetConcurrency(t); err != nil {
			return err
		}
		if err := validateTargetModels(t); err != nil {
			return err
		}
		if err := validateTargetRetry(t); err != nil {
			return err
		}
		if err := validateTargetCircuitBreaker(t); err != nil {
			return err
		}
	}

	if err := validateStrategy(cfg.Strategy, cfg.Targets); err != nil {
		return err
	}

	if err := validateNamedTarget("batch_target", cfg.BatchTarget, cfg.Targets); err != nil {
		return err
	}

	if err := validateNamedTarget("responses_target", cfg.ResponsesTarget, cfg.Targets); err != nil {
		return err
	}

	if cfg.RequestTimeout != "" {
		d, err := time.ParseDuration(cfg.RequestTimeout)
		if err != nil {
			return fmt.Errorf("invalid request_timeout %q: %w", cfg.RequestTimeout, err)
		}
		if d <= 0 {
			return fmt.Errorf("request_timeout must be positive, got %q", cfg.RequestTimeout)
		}
	}

	// Validate observability.tracing.privacy_level against the single source of
	// truth in the internal tracingpolicy package (shared with internal/otel).
	if err := tracingpolicy.ValidatePrivacyLevel(cfg.Observability.Tracing.PrivacyLevel); err != nil {
		return fmt.Errorf("observability.tracing: %w", err)
	}

	// Validate compatibility.on_unsupported_param: "" (⇒ warn), warn, drop, reject.
	if _, ok := core.ParseUnsupportedParamMode(cfg.Compatibility.OnUnsupportedParam); !ok {
		return fmt.Errorf("compatibility.on_unsupported_param must be one of warn, drop, reject")
	}

	// Validate aliases: no alias may point to another alias (no cycles/chains).
	for name, target := range cfg.Aliases {
		if name == "" {
			return fmt.Errorf("alias name must not be empty")
		}
		if target == "" {
			return fmt.Errorf("alias %q must not map to an empty string", name)
		}
		if name == target {
			return fmt.Errorf("alias %q must not point to itself", name)
		}
		if _, chainedAlias := cfg.Aliases[target]; chainedAlias {
			return fmt.Errorf("alias %q points to another alias %q; chained aliases are not supported", name, target)
		}
	}

	if err := validateMCPServers(cfg.MCPServers); err != nil {
		return err
	}

	if err := ValidateMultiStagePlugins(cfg.Plugins); err != nil {
		return err
	}

	return nil
}

// PluginSharingKey identifies the instance a plugin config entry needs.
//
// A plugin whose work spans the request lifecycle — a cache that looks up
// before and stores after, a budget that checks before and records spend
// after — is listed once per stage, and those entries describe one plugin, not
// one per stage: two instances would each hold their own state, so the reader
// would never see what the writer stored. Entries agreeing on name and config
// therefore share a single instance registered at each stage they name, while
// entries differing in config stay independent — two rate limiters with
// different ceilings remain two limiters.
//
// Config equality is decided on the JSON encoding rather than a deep-equality
// walk because encoding/json sorts map keys, which gives a map[string]any a
// stable form. Type and stage are deliberately not part of the key: stage is
// what is being spread across, and the declared type is never consumed at
// construction — a plugin reports its own Type.
//
// A config that cannot be encoded (only reachable for a config assembled in Go
// rather than parsed from YAML or JSON) is reported unshareable, so it gets its
// own instance instead of silently merging with another entry it may differ from.
//
// ValidateMultiStagePlugins is what makes this identity safe to rely on: entries
// that name one plugin across several stages must produce one key, or the config
// is rejected. Without that, disagreeing entries quietly became separate instances.
func PluginSharingKey(pc PluginConfig) (string, bool) {
	encoded, err := json.Marshal(pc.Config)
	if err != nil {
		return "", false
	}
	return pc.Name + "\x00" + string(encoded), true
}

// ValidateMultiStagePlugins refuses a config in which one plugin is listed at
// several stages under entries that do not agree.
//
// A plugin spread across stages is one plugin: the cache that looks up before the
// request is the one that stores after it, and the budget that checks a key's spend
// is the one that records it. Sharing is decided on name plus config, so a single
// character of disagreement — max_age 300 against 301, or a store_id that differs —
// used to produce two instances that never saw each other's state. Both logged
// "plugin registered" and neither did its job: the cache never served a hit, and
// the budget's ceiling never fired.
//
// It also refuses the same entry twice at ONE stage, which the cross-stage rule
// alone let through: identical entries share an instance, so the plugin is
// registered twice at that stage and executes twice per request.
//
// It lives in this package, and runs from ValidateConfig, because the answer is a
// property of the CONFIG ALONE — two entries, their stages, and the bytes of their
// config blocks. Nothing about it depends on which plugins the binary was built
// with, so unlike the plugin-name and stage checks (which ask the factory registry
// and therefore live in the CLI) it can be asked here, once, on behalf of every
// caller. That is what makes `ferrogw validate` reject it as well as `serve`:
// both go through ValidateConfig, and neither had to be taught the rule.
func ValidateMultiStagePlugins(configs []PluginConfig) error {
	type entry struct {
		stage string
		key   string
	}
	byName := make(map[string][]entry, len(configs))
	for _, pc := range configs {
		if !pc.Enabled {
			continue
		}
		key, shareable := PluginSharingKey(pc)
		if !shareable {
			continue
		}
		byName[pc.Name] = append(byName[pc.Name], entry{stage: pc.Stage, key: key})
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		entries := byName[name]
		stages := make(map[string]struct{}, len(entries))
		keys := make(map[string]string, len(entries))
		seen := make(map[entry]struct{}, len(entries))
		for _, e := range entries {
			// Two entries agreeing on name, stage AND config are not two
			// plugins: sharing resolves them to ONE instance, which is then
			// registered twice at that stage and runs twice on every request.
			// A limiter spends two tokens per request, a budget bills each
			// completion twice, a logger writes every row twice — and nothing
			// says so, because both registrations log success. Entries that
			// differ in config at one stage stay legal: those really are two
			// instances, which is how two limiters with different ceilings are
			// spelled.
			if _, dup := seen[e]; dup {
				return fmt.Errorf(
					"plugin %s is listed more than once at stage %s with identical config; the entries share one instance, which would run twice per request — remove the duplicate",
					name, e.stage)
			}
			seen[e] = struct{}{}
			stages[e.stage] = struct{}{}
			keys[e.key] = e.stage
		}
		if len(stages) > 1 && len(keys) > 1 {
			return fmt.Errorf(
				"plugin %s is listed at %d stages with %d different configs; entries for one plugin across stages must carry identical config so they share one instance",
				name, len(stages), len(keys))
		}
	}
	return nil
}

// validateStrategy checks everything about the routing strategy that can be
// known from the config alone, which — because no strategy is pluggable — is
// everything strategy construction can reject.
//
// It runs at LOAD, not at first request. Strategies are built lazily, on the
// first request that needs one, and a construction error there becomes a 500 on
// a request no provider was ever asked to serve: the SDK retries it, so a
// permanent config fault turns into a retry storm. The cost of getting this
// wrong is not symmetric — a rejected startup is loud and immediate, a silently
// misrouted request is neither — which is why Envoy validates a STATICALLY
// defined route table by default (`validate_clusters`, default true for
// route_config) and only relaxes it for xDS, where a route may legitimately
// arrive before the cluster it names. This gateway's config is entirely static,
// so the strict half of that split is the only one that applies.
//
// It stays in this package rather than moving to the CLI alongside the
// provider-id and plugin-name checks. Those answer questions about the BINARY —
// an embedder registers its own providers and plugins, so config can only ask
// them of a build it cannot see. A strategy has no such seam: the mode set is a
// constant in this file, every matcher switch is closed, and target_key is
// resolved against targets in the same document. Nothing here can be made valid
// by a different binary.
// validateNamedTarget checks that a configured surface target (batch_target,
// responses_target) names one of the targets. Whether that target's provider
// actually supports the surface is a runtime property — like routability, it
// depends on which credentials the environment holds — so it is not checked
// here: a pipeline runs `validate` without secrets. An unknown name is a load
// error because it can never route.
func validateNamedTarget(field, value string, targets []Target) error {
	if value == "" {
		return nil
	}
	for _, t := range targets {
		if t.VirtualKey == value {
			return nil
		}
	}
	return fmt.Errorf("%s %q does not name any configured target", field, value)
}

func validateStrategy(s StrategyConfig, targets []Target) error {
	switch s.Mode {
	case ModeSingle, ModeFallback, ModeLatency:
	case ModeLoadBalance:
		return validateWeights("target", targetWeights(targets))
	case ModeCostOptimized:
		switch s.UnpricedStrategy {
		case "", UnpricedStrategyFallback, UnpricedStrategySkip, UnpricedStrategyAllow:
		default:
			return fmt.Errorf("cost-optimized unpriced_strategy must be one of fallback, skip, allow")
		}
	case ModeConditional:
		return validateConditions(s.Conditions, targets)
	case ModeContentBased:
		return validateContentConditions(s.ContentConditions, targets)
	case ModeABTest:
		return validateABVariants(s.ABVariants, targets)
	default:
		return fmt.Errorf("unknown strategy mode: %q", s.Mode)
	}
	return nil
}

func validateConditions(conditions []Condition, targets []Target) error {
	if len(conditions) == 0 {
		return fmt.Errorf("conditional strategy requires at least one condition")
	}
	for i, c := range conditions {
		if !slices.Contains(ConditionKeys(), c.Key) {
			return fmt.Errorf("conditions[%d]: unknown key %q; valid keys: %s",
				i, c.Key, strings.Join(ConditionKeys(), ", "))
		}
		if err := requireDeclaredTarget(fmt.Sprintf("conditions[%d]", i), c.TargetKey, targets); err != nil {
			return err
		}
	}
	return nil
}

func validateContentConditions(conditions []ContentCondition, targets []Target) error {
	if len(conditions) == 0 {
		return fmt.Errorf("content-based strategy requires at least one content_condition")
	}
	for i, c := range conditions {
		if !slices.Contains(ContentConditionTypes(), c.Type) {
			return fmt.Errorf("content_conditions[%d]: unknown type %q; valid types: %s",
				i, c.Type, strings.Join(ContentConditionTypes(), ", "))
		}
		if c.Type == ContentConditionPromptRegex {
			if _, err := regexp.Compile(c.Value); err != nil {
				return fmt.Errorf("content_conditions[%d]: invalid regex %q: %w", i, c.Value, err)
			}
		}
		if err := requireDeclaredTarget(fmt.Sprintf("content_conditions[%d]", i), c.TargetKey, targets); err != nil {
			return err
		}
	}
	return nil
}

func validateABVariants(variants []ABVariantConfig, targets []Target) error {
	if len(variants) == 0 {
		return fmt.Errorf("ab-test strategy requires at least one ab_variant")
	}
	weights := make([]namedWeight, 0, len(variants))
	for i, v := range variants {
		if err := requireDeclaredTarget(fmt.Sprintf("ab_variants[%d]", i), v.TargetKey, targets); err != nil {
			return err
		}
		name := v.Label
		if name == "" {
			name = v.TargetKey
		}
		weights = append(weights, namedWeight{name: name, weight: v.Weight})
	}
	return validateWeights("ab_variant", weights)
}

// namedWeight pairs a weight with whatever the operator would recognise it by,
// so a rejection names the offending entry rather than its index.
type namedWeight struct {
	name   string
	weight float64
}

func targetWeights(targets []Target) []namedWeight {
	weights := make([]namedWeight, 0, len(targets))
	for _, t := range targets {
		weights = append(weights, namedWeight{name: t.VirtualKey, weight: t.Weight})
	}
	return weights
}

// validateWeights rejects a negative weight and an all-zero set.
//
// Zero on a single entry is legal and means zero — that is the drain. Zero
// across ALL of them is not the same statement: it leaves nothing selectable, so
// the gateway would answer every request 404 while /readyz still reported it
// ready, which is a total outage wearing a healthy badge. Envoy draws the line in
// exactly the same place: a zero-weight cluster is a valid, permanently
// unselected entry, while "Sum of weights in the weighted_cluster must be greater
// than 0" is a config-time rejection. Draining everything is spelled by removing
// the targets or stopping the process, both of which say so out loud.
func validateWeights(kind string, weights []namedWeight) error {
	var sum float64
	for _, w := range weights {
		if w.weight < 0 {
			return fmt.Errorf("%s %q has negative weight %v", kind, w.name, w.weight)
		}
		sum += w.weight
	}
	if sum <= 0 {
		return fmt.Errorf("%s weights sum to zero; at least one must be positive", kind)
	}
	return nil
}

// requireDeclaredTarget resolves a rule's target_key against the declared
// targets. A key that names a provider not in targets would bypass that target's
// retry, concurrency and circuit-breaker config even when it happened to
// resolve, and a key that names nothing at all fails every request the rule
// matches — as a 404 on one surface and, until the surfaces converged, as a
// silent reroute to a different provider on another. Envoy's validate_clusters
// makes the same demand of a route's cluster.
func requireDeclaredTarget(where, targetKey string, targets []Target) error {
	if targetKey == "" {
		return fmt.Errorf("%s: target_key is required", where)
	}
	declared := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.VirtualKey == targetKey {
			return nil
		}
		declared = append(declared, t.VirtualKey)
	}
	return fmt.Errorf("%s: target_key %q names no configured target; declared targets: %s",
		where, targetKey, strings.Join(declared, ", "))
}

// validateMCPServers checks each server's transport selection: exactly one of
// URL (Streamable HTTP) or Command (stdio) must be set. Leaving both empty fails
// later during async initialization with a confusing error; leaving both set
// silently prefers stdio and drops Headers, which is surprising.
func validateMCPServers(servers []pubmcp.ServerConfig) error {
	seen := make(map[string]struct{}, len(servers))
	for i, mcpCfg := range servers {
		// Name is the registry key. An empty one produces a server nothing can
		// refer to in logs or metrics; a duplicate silently replaces the earlier
		// server, taking its tool mappings with it.
		if strings.TrimSpace(mcpCfg.Name) == "" {
			return fmt.Errorf("mcp server at index %d: name is required", i)
		}
		if _, dup := seen[mcpCfg.Name]; dup {
			return fmt.Errorf("mcp server %q: duplicate name; each server needs a unique name", mcpCfg.Name)
		}
		seen[mcpCfg.Name] = struct{}{}

		hasURL := mcpCfg.URL != ""
		hasCommand := mcpCfg.Command != ""
		switch {
		case hasURL && hasCommand:
			return fmt.Errorf("mcp server %q: url and command are mutually exclusive; set exactly one to select transport", mcpCfg.Name)
		case !hasURL && !hasCommand:
			return fmt.Errorf("mcp server %q: either url (Streamable HTTP) or command (stdio) is required", mcpCfg.Name)
		}
	}
	return nil
}

// validateTargetConcurrency bounds a target's concurrency block. The ceiling is
// what keeps a mistyped max_concurrency from silently turning the limiter into a
// no-op: the value becomes the in-flight slot count, so an absurd one admits
// every request and the cap the operator asked for never applies.
func validateTargetConcurrency(t Target) error {
	if t.Concurrency == nil {
		return nil
	}
	if t.Concurrency.MaxConcurrency <= 0 {
		return fmt.Errorf("target %q: concurrency.max_concurrency must be positive (omit the concurrency block to leave the target unlimited)", t.VirtualKey)
	}
	if t.Concurrency.MaxConcurrency > MaxTargetConcurrency {
		return fmt.Errorf("target %q: concurrency.max_concurrency exceeds the limit of %d", t.VirtualKey, MaxTargetConcurrency)
	}
	if t.Concurrency.QueueSize < 0 {
		return fmt.Errorf("target %q: concurrency.queue_size cannot be negative", t.VirtualKey)
	}
	if t.Concurrency.QueueSize > MaxTargetConcurrency {
		return fmt.Errorf("target %q: concurrency.queue_size exceeds the limit of %d", t.VirtualKey, MaxTargetConcurrency)
	}
	return nil
}

// validateTargetRetry checks a target's retry block.
//
// Both fields are counts, and both are normalised downstream — retryPolicyFor
// reads attempts <= 0 as one attempt, strategies.NormalizeBackoffMs reads a
// backoff <= 0 as the 100 ms default. Normalisation is right for an OMITTED
// value and wrong for a written one: `attempts: -1` is not a request for the
// default, it is a request nothing can serve, and the deployment that wrote it
// gets the retries it did not ask for with nothing logged. Zero stays legal on
// both, because zero is what an unset field decodes to and cannot be told apart
// from one.
func validateTargetRetry(t Target) error {
	if t.Retry == nil {
		return nil
	}
	if t.Retry.Attempts < 0 {
		return fmt.Errorf("target %q: retry.attempts cannot be negative, got %d (omit the field, or set 1, for a single attempt)", t.VirtualKey, t.Retry.Attempts)
	}
	if t.Retry.InitialBackoffMs < 0 {
		return fmt.Errorf("target %q: retry.initial_backoff_ms cannot be negative, got %d", t.VirtualKey, t.Retry.InitialBackoffMs)
	}
	return nil
}

// validateTargetCircuitBreaker checks a target's circuit_breaker block.
//
// timeout is the field that pays for this: circuitbreaker.New reads an
// unparseable duration as zero and substitutes 30s, so `timeout: "banana"` and
// `timeout: "30 minutes"` both produced a breaker that reopened on a schedule
// nobody configured. The gateway warns when it builds one, which is a line in a
// startup log an operator reads once; validation is where they were looking.
//
// A negative duration is refused for the same reason a negative threshold is:
// it cannot be honoured, so it can only be substituted, and substituting a
// written value silently is the failure being fixed. Zero remains legal — it is
// what an omitted field decodes to.
func validateTargetCircuitBreaker(t Target) error {
	cb := t.CircuitBreaker
	if cb == nil {
		return nil
	}
	for _, f := range []struct {
		name  string
		value int
	}{
		{"failure_threshold", cb.FailureThreshold},
		{"success_threshold", cb.SuccessThreshold},
		{"max_half_threshold", cb.MaxHalfThreshold},
	} {
		if f.value < 0 {
			return fmt.Errorf("target %q: circuit_breaker.%s cannot be negative, got %d (omit the field to take the default)", t.VirtualKey, f.name, f.value)
		}
	}
	if cb.Timeout == "" {
		return nil
	}
	d, err := time.ParseDuration(cb.Timeout)
	if err != nil {
		return fmt.Errorf("target %q: invalid circuit_breaker.timeout %q: %w", t.VirtualKey, cb.Timeout, err)
	}
	if d < 0 {
		return fmt.Errorf("target %q: circuit_breaker.timeout cannot be negative, got %q", t.VirtualKey, cb.Timeout)
	}
	return nil
}

// modelWildcardChars are the glob metacharacters validateTargetModels refuses in
// a declared model id.
const modelWildcardChars = "*?"

// validateTargetModels checks a target's declared model list.
//
// Each entry becomes a key in the exact-match routing index, so anything the
// index cannot look up verbatim is a declaration that reads as effective and
// routes nothing. That is the whole cost of getting this wrong: a rejected
// config is one line of startup output, an accepted-but-inert one is a 404 on a
// model the operator can see written in their own config file.
//
//   - An empty entry, or one carrying surrounding whitespace, is a typo or a
//     stray list item. Trimming it instead would make the config and the index
//     disagree about what was declared.
//   - A wildcard is refused rather than expanded. Prefix matching is precisely
//     what let a provider claim models it does not serve and receive prompt
//     bodies for them; the index is exact-match so that cannot recur, and a
//     pattern accepted here would match nothing while looking authoritative.
//     An upstream that really does accept unenumerable ids is declared with
//     core.AnyModelProvider, on the provider, on purpose.
//   - A duplicate within one target is de-duplicated by the index anyway, so
//     rejecting it costs nothing and catches the copy-paste that meant to name
//     two different models.
//
// Repeating a model across DIFFERENT targets is not an error: that is how a
// model gets a fallback.
func validateTargetModels(t Target) error {
	seen := make(map[string]struct{}, len(t.Models))
	for i, m := range t.Models {
		if m == "" || strings.TrimSpace(m) != m {
			return fmt.Errorf("target %q: models[%d] must be a non-empty model id with no surrounding whitespace, got %q", t.VirtualKey, i, m)
		}
		if strings.ContainsAny(m, modelWildcardChars) {
			return fmt.Errorf("target %q: models[%d] %q contains a wildcard; declare each model id exactly", t.VirtualKey, i, m)
		}
		if _, dup := seen[m]; dup {
			return fmt.Errorf("target %q: models[%d] %q is listed more than once", t.VirtualKey, i, m)
		}
		seen[m] = struct{}{}
	}
	return nil
}
