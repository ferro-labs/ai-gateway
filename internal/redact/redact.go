package redact

import "regexp"

// Policy is a named regex replacement rule.
type Policy struct {
	// Name is a short identifier used in logs and tests.
	Name string
	// Pattern is the compiled regex that matches sensitive substrings.
	Pattern *regexp.Regexp
	// Replacement is substituted for every match. Use stable, recognisable
	// tokens like "[REDACTED_EMAIL]" so downstream consumers can detect
	// redaction.
	Replacement string
}

// Redactor applies an ordered list of Policy rules to strings. A nil
// *Redactor is safe to use and returns input unchanged — callers may
// keep redaction optional without nil checks at every callsite.
type Redactor struct {
	policies []Policy
}

// New constructs a Redactor with the given policies applied in order.
// Pass DefaultPolicies()... to use the built-in rules.
func New(policies ...Policy) *Redactor {
	return &Redactor{policies: policies}
}

// DefaultRedactor returns a Redactor pre-populated with the default
// policy set (see DefaultPolicies).
func DefaultRedactor() *Redactor {
	return New(DefaultPolicies()...)
}

// Redact returns s with the gateway's own credential values replaced first,
// then every policy applied in order. A nil *Redactor returns s unchanged.
//
// Value replacement runs first because it is exact: a credential the gateway
// holds is removed whatever its shape, including the prefix-less formats no
// pattern can safely match. The policies then act as a backstop for credentials
// the gateway never saw — one echoed by an upstream that the operator never
// configured.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	s = active().redact(s)
	for _, p := range r.policies {
		s = p.Pattern.ReplaceAllString(s, p.Replacement)
	}
	return s
}

// Policies returns the active policy list. The returned slice MUST
// NOT be mutated by callers.
func (r *Redactor) Policies() []Policy {
	if r == nil {
		return nil
	}
	return r.policies
}
