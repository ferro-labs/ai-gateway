package redact

import "regexp"

// DefaultPolicies returns the redaction rules applied when no custom
// policy set is supplied.
//
// Coverage in this scaffolding revision:
//   - email addresses
//   - JWTs (header.payload.signature)
//   - AWS access key IDs (AKIA…)
//
// Coverage planned for a future release:
//   - credit card numbers (Luhn-validated)
//   - phone numbers (E.164 + common national formats)
//   - operator-supplied custom regex policies
func DefaultPolicies() []Policy {
	return []Policy{
		{
			Name:        "email",
			Pattern:     regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
			Replacement: "[REDACTED_EMAIL]",
		},
		{
			Name:        "jwt",
			Pattern:     regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
			Replacement: "[REDACTED_JWT]",
		},
		{
			Name:        "aws_access_key",
			Pattern:     regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
			Replacement: "[REDACTED_AWS_KEY]",
		},
	}
}
