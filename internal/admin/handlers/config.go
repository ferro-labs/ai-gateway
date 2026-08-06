package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/go-chi/chi/v5"
)

// maxConfigHistoryEntries caps the in-memory config history so long-running
// gateways with frequent reloads (e.g. GitOps/CI-driven sync) don't grow the
// history slice — and the full config.Config snapshots it holds — without
// bound.
const maxConfigHistoryEntries = 200

// Audit actions for the config control plane.
//
// Replacing the config is the most consequential thing this API does — it can
// redirect every request to a different provider, disable a guardrail, or drain
// a target — so it belongs in the same trail as credential changes rather than
// only in config_history, which records an actor but no outcome, source address
// or trace id and cannot be read from GET /admin/audit.
const (
	auditConfigCreate   = "config.create"
	auditConfigUpdate   = "config.update"
	auditConfigDelete   = "config.delete"
	auditConfigRollback = "config.rollback"
)

// auditConfigTarget is the target_id recorded for a whole-config replacement.
//
// The AuditEntry contract offers "*" for an action whose subject is everything
// rather than one identified row, which is what replacing the active config is.
// A version number is deliberately not used: the in-memory history numbers
// versions independently of the durable config_history, so a version recorded
// here would name different things depending on which store is configured.
// Rollback is the exception — it is asked for by version, so that version is
// what identifies it.
const auditConfigTarget = "*"

// ConfigHistoryEntry captures a runtime config update snapshot.
type ConfigHistoryEntry struct {
	Version        int           `json:"version"`
	UpdatedAt      time.Time     `json:"updated_at"`
	Config         config.Config `json:"config"`
	RolledBackFrom *int          `json:"rolled_back_from,omitempty"`
	// Actor is who applied this version. Omitted rather than blank when the
	// entry predates actor recording, so a reader never sees an empty string
	// and takes it to mean nobody.
	Actor string `json:"actor,omitempty"`
}

func (h *Handlers) getConfig(w http.ResponseWriter, _ *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(repository.ScrubConfigSecrets(h.Configs.GetConfig()))
}

func (h *Handlers) getConfigHistory(w http.ResponseWriter, r *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	history, _, err := h.configVersions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}

	// Redact secret-bearing config values on each copied entry before encoding.
	// scrubConfigSecrets operates on a copy so the live history is never mutated.
	for i := range history {
		history[i].Config = repository.ScrubConfigSecrets(history[i].Config)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": history,
		"summary": map[string]any{
			"total_versions": len(history),
		},
	})
}

func (h *Handlers) updateConfig(w http.ResponseWriter, r *http.Request) {
	h.applyConfigUpdate(w, r, http.StatusOK, "updated", auditConfigUpdate)
}

func (h *Handlers) createConfig(w http.ResponseWriter, r *http.Request) {
	h.applyConfigUpdate(w, r, http.StatusCreated, "created", auditConfigCreate)
}

func (h *Handlers) applyConfigUpdate(w http.ResponseWriter, r *http.Request, statusCode int, statusText string, action string) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	var cfg config.Config
	if err := decodeConfigBody(r.Body, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "invalid_request")
		return
	}

	// Refuse a config that carries the redaction placeholder back as a value.
	// Reads scrub literal secrets to "[REDACTED]", so an edit-and-resend of a
	// GET response would silently replace a live credential with that text.
	if field := repository.RedactedSecretField(cfg); field != "" {
		h.recordAudit(r, action, auditConfigTarget, model.AuditDenied, "reason", "redacted_secret", "field", field)
		writeError(w, http.StatusBadRequest,
			"config field "+field+" contains the redaction placeholder \"[REDACTED]\" from a previous read; "+
				"supply the real value or a ${VAR} reference. This endpoint replaces the whole config, "+
				"so omitting the field clears it rather than preserving the current value",
			"invalid_request_error", "redacted_secret")
		return
	}

	// Apply and record as one transaction. Decoding stays outside the lock so a
	// slow request body never blocks another operator's config mutation.
	h.configMu.Lock()
	defer h.configMu.Unlock()

	if err := h.Configs.ReloadConfig(r.Context(), cfg); err != nil {
		// A rejected config is recorded too. "Someone tried to point production
		// at a provider that does not exist" is a question the trail is read to
		// answer, and a store holding only accepted changes answers it with a
		// silence indistinguishable from nobody having tried.
		h.recordAudit(r, action, auditConfigTarget, model.AuditError, "error", err.Error())
		writeConfigReloadError(w, err)
		return
	}

	h.appendConfigHistoryLocked(cfg, nil, model.ActorFromContext(r.Context()))
	h.recordAudit(r, action, auditConfigTarget, model.AuditOK,
		"strategy", string(cfg.Strategy.Mode), "targets", len(cfg.Targets), "plugins", len(cfg.Plugins))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": statusText})
}

// decodeConfigBody decodes a whole-config request body as strictly as
// config.LoadConfig decodes a config file: an unknown key is an error rather
// than a discard, and data trailing the top-level object is an error rather
// than an ignore.
//
// This endpoint carries a hand-edited config, which is the case a lenient
// decoder serves worst. A misspelled key — targets[].models written as "model"
// — decodes to a Config that simply lacks the setting the operator wrote, and a
// lenient decode then answers "updated" for a config that does something else.
// The same file rejected by `ferrogw validate` has to be rejected here, or the
// two ways of applying a config disagree about which ones are valid. It is the
// same decoder rather than a matching one — config.DecodeJSONStrict is what
// LoadConfig's JSON arm calls too — so a hand-edited body carrying several
// typos names all of them in one response, exactly as the file path does.
//
// The body is buffered because enumerating those keys needs a second pass over
// it. Admin routes are mounted behind middleware.MaxRequestBody, so its size is
// already bounded before this reads it.
func decodeConfigBody(body io.Reader, cfg *config.Config) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return config.DecodeJSONStrict(data, cfg)
}

func (h *Handlers) deleteConfig(w http.ResponseWriter, r *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	resetter, ok := h.Configs.(repository.ConfigResetter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "config reset is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	h.configMu.Lock()
	defer h.configMu.Unlock()

	if err := resetter.ResetConfig(r.Context()); err != nil {
		h.recordAudit(r, auditConfigDelete, auditConfigTarget, model.AuditError, "error", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}

	// Reading the config back after the reset is only safe because configMu is
	// still held: no concurrent mutation can replace it before it is recorded.
	h.appendConfigHistoryLocked(h.Configs.GetConfig(), nil, model.ActorFromContext(r.Context()))
	h.recordAudit(r, auditConfigDelete, auditConfigTarget, model.AuditOK)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func (h *Handlers) rollbackConfig(w http.ResponseWriter, r *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	requestedVersion, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || requestedVersion <= 0 {
		writeError(w, http.StatusBadRequest, "invalid version: must be a positive integer", "invalid_request_error", "invalid_request")
		return
	}

	// Held across the version lookup, the apply, and the history append so
	// latestVersion — the provenance recorded as rolled_back_from — cannot go
	// stale between being read and being written.
	h.configMu.Lock()
	defer h.configMu.Unlock()

	versions, durable, err := h.configVersions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}

	var target *ConfigHistoryEntry
	latestVersion := 0
	if n := len(versions); n > 0 {
		latestVersion = versions[n-1].Version
	}
	for i := range versions {
		if versions[i].Version == requestedVersion {
			target = &versions[i]
			break
		}
	}

	requested := strconv.Itoa(requestedVersion)

	if target == nil {
		h.recordAudit(r, auditConfigRollback, requested, model.AuditDenied, "reason", "version_not_found")
		writeError(w, http.StatusNotFound, "config version not found", "not_found_error", "resource_not_found")
		return
	}

	// The provenance travels with the apply so the durable trail records it in
	// the same transaction as the config, rather than only in the in-memory
	// list — where it was invisible on every deployment that keeps a config
	// store, which is every production one.
	if err := h.Configs.ReloadConfig(repository.WithRollbackFrom(r.Context(), latestVersion), target.Config); err != nil {
		h.recordAudit(r, auditConfigRollback, requested, model.AuditError, "error", err.Error())
		writeConfigReloadError(w, err)
		return
	}

	rollbackFrom := latestVersion
	historySize := h.appendConfigHistoryLocked(target.Config, &rollbackFrom, model.ActorFromContext(r.Context()))
	h.recordAudit(r, auditConfigRollback, requested, model.AuditOK, "rolled_back_from", latestVersion)
	if durable {
		// The apply persisted a version of its own; the in-memory record above is
		// not what was served, so it must not be what is counted.
		historySize = len(versions) + 1
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":               "rolled_back",
		"rolled_back_to":       requestedVersion,
		"current_history_size": historySize,
	})
}

// appendConfigHistoryLocked records cfg as the newest history version and
// returns the resulting history size.
//
// The caller must already hold h.configMu: the entry is only a truthful record
// of the active config if no other mutation can run between applying cfg and
// appending it. h.historyMu is taken here, for the slice write alone.
func (h *Handlers) appendConfigHistoryLocked(cfg config.Config, rolledBackFrom *int, actor string) int {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()

	// Derive the next version from the last entry's Version rather than the
	// slice length: once old entries are evicted below, length no longer
	// tracks the cumulative version count.
	nextVersion := 1
	if n := len(h.configHistory); n > 0 {
		nextVersion = h.configHistory[n-1].Version + 1
	}

	h.configHistory = append(h.configHistory, ConfigHistoryEntry{
		Version:        nextVersion,
		UpdatedAt:      time.Now().UTC(),
		Config:         cfg,
		RolledBackFrom: rolledBackFrom,
		Actor:          actor,
	})

	if len(h.configHistory) > maxConfigHistoryEntries {
		h.configHistory = h.configHistory[len(h.configHistory)-maxConfigHistoryEntries:]
	}

	return len(h.configHistory)
}

func writeConfigReloadError(w http.ResponseWriter, err error) {
	if errors.Is(err, repository.ErrConfigPersistence) {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_config")
}

// configVersions returns the version trail to serve and to roll back to, and
// whether it came from the durable store.
//
// The durable trail wins whenever one is kept: it is written in the same
// transaction as the config it records, so it survives a restart and numbers
// versions from a single counter. Serving the in-memory list instead would
// renumber from 1 on every boot and hide — and refuse rollback to — every
// version applied before it. The in-memory list stays as the fallback for a
// deployment with no persistent config store, where nothing else records what
// was applied.
func (h *Handlers) configVersions(ctx context.Context) ([]ConfigHistoryEntry, bool, error) {
	loader, ok := h.Configs.(repository.ConfigHistoryLoader)
	if !ok {
		return h.getConfigHistorySnapshot(), false, nil
	}
	persisted, durable, err := loader.LoadHistory(ctx)
	if err != nil {
		return nil, false, err
	}
	if !durable {
		return h.getConfigHistorySnapshot(), false, nil
	}

	entries := make([]ConfigHistoryEntry, 0, len(persisted))
	for _, p := range persisted {
		entries = append(entries, ConfigHistoryEntry{
			Version:   p.Version,
			UpdatedAt: p.UpdatedAt,
			Config:    p.Config,
			Actor:     p.Actor,
			// Nil for an ordinary update and for a rollback recorded before the
			// column existed. The two are the same answer here on purpose: the
			// column exists to mark the exception, and an update is what a row
			// with nothing recorded almost always was.
			RolledBackFrom: p.RolledBackFrom,
		})
	}
	return entries, true, nil
}

func (h *Handlers) getConfigHistorySnapshot() []ConfigHistoryEntry {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()
	history := make([]ConfigHistoryEntry, len(h.configHistory))
	copy(history, h.configHistory)
	return history
}
