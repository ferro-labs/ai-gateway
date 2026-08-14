package fixture

// The gateway's wire vocabulary, in one place.
//
// This package is the in-repo definition of the gateway's HTTP contract, and
// the same field names recur across handlers that were written months apart —
// `status` alone appears in the health surface, the key surface, the plugin
// catalog and the model catalog. Spelled inline, one of them can drift from
// the rest and still compile, and the fixture then teaches every downstream
// test the wrong name for a field.
//
// The rule the file follows: a spelling written in three or more places gets a
// constant. A spelling below that gets one only when it completes a set already
// named here — the other provider, the other plugin type, the other stage — so
// no vocabulary ends up half constant and half literal. Everything a single
// handler writes once stays inline, where the wire shape reads as the wire.

// JSON field names served on the wire. /admin/logs and /admin/audit name their
// query parameters after the fields they filter on, deliberately, so the
// filters share these constants with the rows they select.
const (
	fieldAction          = "action"
	fieldActor           = "actor"
	fieldActorID         = "actor_id"
	fieldAPIKeyID        = "api_key_id"
	fieldCapabilities    = "capabilities"
	fieldCircuit         = "circuit"
	fieldContextWindow   = "context_window"
	fieldCount           = "count"
	fieldCreated         = "created"
	fieldData            = "data"
	fieldDeprecated      = "deprecated"
	fieldDetail          = "detail"
	fieldEnabled         = "enabled"
	fieldError           = "error"
	fieldFailsOpen       = "fails_open"
	fieldLimit           = "limit"
	fieldMaxOutputTokens = "max_output_tokens"
	fieldMessage         = "message"
	fieldMode            = "mode"
	fieldModel           = "model"
	fieldModels          = "models"
	fieldName            = "name"
	fieldObject          = "object"
	fieldOccurredAt      = "occurred_at"
	fieldOffset          = "offset"
	fieldOutcome         = "outcome"
	fieldOwnedBy         = "owned_by"
	fieldProvider        = "provider"
	fieldProviders       = "providers"
	fieldReady           = "ready"
	fieldScopes          = "scopes"
	fieldSettings        = "settings"
	fieldSince           = "since"
	fieldSourceIP        = "source_ip"
	fieldStage           = "stage"
	fieldStatus          = "status"
	fieldSummary         = "summary"
	fieldTotalEntries    = "total_entries"
	fieldType            = "type"
)

// The error envelope's `type`/`code` vocabulary, as writeErr serves it.
const (
	kindAuthentication = "authentication_error"
	kindInvalidRequest = "invalid_request_error"
	kindNotFound       = "not_found_error"
	kindServer         = "server_error"
)

// msgKeyNotFound is the one message three key routes share.
const msgKeyNotFound = "key not found"

// Provider and model identity, repeated across /health, /readyz, /admin/health,
// /admin/providers, /admin/logs and /v1/models.
const (
	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"

	modelSonnet          = "claude-sonnet-4-6"
	modelOpus            = "claude-opus-4-6"
	modelGPT4o           = "gpt-4o"
	modelGPT4oMini       = "gpt-4o-mini"
	modelEmbedding3Small = "text-embedding-3-small"

	// objectModel is the OpenAI object type a /v1/models row reports. It shares
	// a spelling with fieldModel and means something else: one is the name of a
	// field, the other is a value that field never carries.
	objectModel = "model"
)

// The closed vocabularies the gateway serves: key scopes, plugin stages, plugin
// types, the health words the CLI branches on, and the audit outcomes
// /admin/audit validates its query against. statusOK and outcomeOK share a
// spelling and nothing else — a health status and an audit outcome are separate
// sets that happen to have both settled on "ok".
const (
	scopeAdmin    = "admin"
	scopeReadOnly = "read_only"

	stageBeforeRequest = "before_request"
	stageAfterRequest  = "after_request"
	stageOnError       = "on_error"

	typeGuardrail = "guardrail"
	typeLogging   = "logging"
	typeRateLimit = "ratelimit"

	// Every value the fixture ever serves under `status` is named here, so a
	// reader can see the whole vocabulary the CLI branches on in one place and
	// a new one cannot be added inline without joining it.
	statusOK          = "ok"
	statusHealthy     = "healthy"
	statusDegraded    = "degraded"
	statusDisabled    = "disabled"
	statusAvailable   = "available"
	statusStable      = "stable"
	statusReady       = "ready"
	statusNotReady    = "not_ready"
	statusNoProviders = "no_providers"
	statusRevoked     = "revoked"

	circuitClosed   = "closed"
	circuitHalfOpen = "half_open"

	outcomeOK     = "ok"
	outcomeDenied = "denied"
	outcomeError  = "error"

	// modalityChat and modalityEmbedding are each both a model's mode and one
	// of its capabilities — the same word for the same thing, so each is one
	// constant rather than two.
	modalityChat        = "chat"
	modalityEmbedding   = "embedding"
	capabilityStreaming = "streaming"
	capabilityTools     = "tools"
	capabilityVision    = "vision"
)

// Seed identity: the ids and address the seeded rows cross-reference, so a key
// row, a session, an audit entry and a log row agree on which credential and
// which host they are talking about.
const (
	keyIDOps      = "key_01"
	keyIDCI       = "key_02"
	actorOps      = "ops-laptop"
	sourceIPLocal = "127.0.0.1"
)
