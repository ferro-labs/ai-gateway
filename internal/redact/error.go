package redact

import "sync"

// shared is the process-wide default Redactor. It is built on first use so the
// policy regexes are compiled once, and never at all in a process that emits no
// error text.
var shared = sync.OnceValue(DefaultRedactor)

// String returns s with the default policies applied.
//
// Redaction is best-effort mitigation, not a guarantee. The default policies
// recognise a fixed set of credential shapes (see DefaultPolicies); a secret
// whose format is not among them — for example a provider API key carrying no
// distinguishing prefix, as issued by Mistral, Together, Cohere, Fireworks, and
// DeepInfra — passes through unchanged. Treat the result as reduced exposure,
// not as text proven free of secrets.
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
