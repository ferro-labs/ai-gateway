package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// projectionCap bounds how much of a request body the pass-through will buffer
// in order to project it as text for a content guardrail.
//
// It exists because this surface carries bodies the routed surfaces never see:
// a /v1/files upload or a /v1/audio/transcriptions payload can be hundreds of
// megabytes, and buffering one to search it for prose would trade a governance
// gap for an out-of-memory. Past the cap the body is reported UNINSPECTABLE
// rather than partially inspected — a guardrail shown the first megabyte of a
// body has not screened the body, and reporting a partial read as a full one is
// the same silent pass this whole path exists to close.
//
// This is the same bound Envoy's ext_authz draws, and it fails the same way:
// its with_request_body has a max_request_bytes past which the request is
// refused with a 413, explicitly overriding failure_mode_allow.
const projectionCap = 1 << 20 // 1 MiB

// projectBody returns a best-effort text projection of a JSON request body,
// together with whether that projection can be trusted to contain everything a
// content guardrail would want to read.
//
// The body is always restored, so the caller can forward it afterwards exactly
// as it arrived.
//
// What a projection covers: every STRING VALUE anywhere in a JSON body, at any
// depth, joined by newlines. That deliberately over-collects — a model id, a
// file name and a tool name land in it alongside the prose — because a
// guardrail seeing more than the prompt can only produce a false refusal, while
// a guardrail seeing less than the prompt produces a forwarded violation. Keys
// are not collected: they are the schema, not the caller's content.
//
// What it cannot cover, and so reports as uninspectable:
//
//   - a body that is not JSON at all — a multipart /v1/files upload, raw audio
//   - a body larger than projectionCap
//   - a body that is malformed JSON, or that could not be read
//
// A body that is absent or empty is inspectable and projects to nothing: a GET
// /v1/files or a DELETE carries no content, so there is nothing a guardrail was
// prevented from seeing. That is a different fact from "there is content and it
// could not be read", and only the second one has to fail closed.
//
// text-only projection. Base64 inside a JSON string is collected as
// the base64, which no blocklist will match — decoding every string that looks
// like base64 is a decoder, not a projection. Add it when a deployment actually
// needs to screen inline attachments.
func projectBody(r *http.Request) (text string, inspectable bool) {
	if r.Body == nil || r.ContentLength == 0 {
		return "", true
	}

	// One byte past the cap, so a body sitting exactly at the limit is still
	// distinguishable from one that exceeds it.
	buf, readErr := io.ReadAll(io.LimitReader(r.Body, projectionCap+1))
	// Restore before returning down any path: the forward that follows reads
	// this body, and a refusal path may still need it intact for logging.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))

	if readErr != nil {
		// Includes http.MaxBytesError from the body-limit middleware. A body the
		// gateway could not finish reading is a body it did not inspect.
		return "", false
	}
	if len(buf) > projectionCap {
		return "", false
	}

	var doc any
	if err := json.Unmarshal(buf, &doc); err != nil {
		return "", false
	}

	var sb strings.Builder
	collectStrings(doc, &sb)
	return sb.String(), true
}

// collectStrings appends every string value reachable from v to sb, one per
// line. Numbers, booleans and nulls are skipped: a blocklist is a policy about
// prose, and no useful rule matches "3.14".
func collectStrings(v any, sb *strings.Builder) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(t)
	case []any:
		for _, item := range t {
			collectStrings(item, sb)
		}
	case map[string]any:
		// Map iteration order is random, so the projection's ORDER is not
		// stable between two identical requests. Nothing here depends on order:
		// a guardrail matches substrings within a single value, and a value is
		// never split across the join. A plugin that hashed this projection
		// would be the first thing to care, and none does.
		for _, item := range t {
			collectStrings(item, sb)
		}
	}
}
