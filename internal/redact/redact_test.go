package redact

import (
	"regexp"
	"strings"
	"testing"
)

// awsKeyFixture returns a synthetic AWS-access-key-shaped string built
// at runtime so the literal never appears in source files (which would
// trip credential-scanning pre-commit hooks).
func awsKeyFixture(tail string) string {
	pad := 16 - len(tail)
	if pad < 0 {
		pad = 0
	}
	return "AKIA" + strings.Repeat("A", pad) + strings.ToUpper(tail)
}

func TestDefaultRedactorEmail(t *testing.T) {
	r := DefaultRedactor()
	got := r.Redact("contact me at jane.doe@example.com please")
	want := "contact me at [REDACTED_EMAIL] please"
	if got != want {
		t.Errorf("Redact email\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorJWT(t *testing.T) {
	r := DefaultRedactor()
	// Valid-shape JWT (header.payload.signature, base64url chars).
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	got := r.Redact("token=" + jwt + " end")
	want := "token=[REDACTED_JWT] end"
	if got != want {
		t.Errorf("Redact JWT\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorAWSKey(t *testing.T) {
	r := DefaultRedactor()
	in := "key=" + awsKeyFixture("example") + " found"
	got := r.Redact(in)
	want := "key=[REDACTED_AWS_KEY] found"
	if got != want {
		t.Errorf("Redact AWS key\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorMultiplePatterns(t *testing.T) {
	r := DefaultRedactor()
	in := "user a@b.co with key " + awsKeyFixture("abcd")
	got := r.Redact(in)
	want := "user [REDACTED_EMAIL] with key [REDACTED_AWS_KEY]"
	if got != want {
		t.Errorf("multi-pattern\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorLeavesCleanTextAlone(t *testing.T) {
	r := DefaultRedactor()
	in := "no sensitive data here"
	if got := r.Redact(in); got != in {
		t.Errorf("clean text mutated\n got: %q\nwant: %q", got, in)
	}
}

func TestNilRedactorIsSafe(t *testing.T) {
	var r *Redactor
	if got := r.Redact("anything"); got != "anything" {
		t.Errorf("expected pass-through, got %q", got)
	}
	if got := r.Policies(); got != nil {
		t.Errorf("expected nil policies, got %v", got)
	}
}

func TestCustomPolicy(t *testing.T) {
	r := New(Policy{
		Name:        "ssn",
		Pattern:     regexp.MustCompile(`\d{3}-\d{2}-\d{4}`),
		Replacement: "[REDACTED_SSN]",
	})
	got := r.Redact("SSN 123-45-6789 on file")
	want := "SSN [REDACTED_SSN] on file"
	if got != want {
		t.Errorf("custom policy\n got: %q\nwant: %q", got, want)
	}
}

func TestPoliciesReturnsConfiguredList(t *testing.T) {
	r := DefaultRedactor()
	got := r.Policies()
	if len(got) != 3 {
		t.Fatalf("expected 3 default policies, got %d", len(got))
	}
	wantNames := map[string]bool{"email": true, "jwt": true, "aws_access_key": true}
	for _, p := range got {
		if !wantNames[p.Name] {
			t.Errorf("unexpected policy name %q", p.Name)
		}
	}
}

func TestAwsKeyFixtureMatchesPattern(t *testing.T) {
	// Sanity: the runtime-built fixture must match the AWS key regex,
	// otherwise the tests above would be vacuously passing.
	fixture := awsKeyFixture("example")
	if !regexp.MustCompile(`AKIA[0-9A-Z]{16}`).MatchString(fixture) {
		t.Fatalf("fixture %q does not match AWS key regex", fixture)
	}
}
