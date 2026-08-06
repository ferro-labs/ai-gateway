package redact

import "sync"

// shared is the process-wide default Redactor. It is built on first use so the
// policy regexes are compiled once, and never at all in a process that emits no
// error text.
var shared = sync.OnceValue(DefaultRedactor)

// String returns s with every credential the gateway holds removed by value,
// then the default policies applied.
//
// Value redaction covers the configured credentials exactly, whatever their
// shape. What remains best-effort is a credential the gateway never saw — one
// echoed back by an upstream that the operator never configured — which is left
// to the shape-based policies and to keyword_secret. Every implementer of
// value-based redaction documents the same residual: a secret that arrives
// transformed (base64- or URL-encoded, or truncated) is not matched, because
// only the original literal is known. Treat the result as reduced exposure, not
// as text proven free of secrets.
func String(s string) string {
	return shared().Redact(s)
}

// ErrorMessage returns err.Error() with the default policies applied, or "" when
// err is nil. The same best-effort limitation described on String applies.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return shared().Redact(err.Error())
}
