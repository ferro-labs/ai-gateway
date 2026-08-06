package redact

import (
	"strings"
	"testing"
)

// The DSN values below are synthetic. Nothing here reads the process
// environment for a real credential.
const (
	shortPassword        = "brief1"
	syntheticDSNPassword = "s3cr3t-p4ssw0rd-synthetic"
	syntheticPostgresDSN = "postgres://ferrogw:" + syntheticDSNPassword + "@db.internal:5432/gateway"
)

// reseedFromEnv rebuilds the secret set from the current environment, which
// t.Setenv has already adjusted, and restores the previous set afterwards.
func reseedFromEnv(t *testing.T) {
	t.Helper()
	seed()
	prev := secrets.Load()
	secrets.Store(newValueMatcher(envSecretPairs()))
	t.Cleanup(func() { secrets.Store(prev) })
}

// A DSN naming a file is a path, not a credential. The startup line that tells
// an operator which SQLite files to back up must be able to print it.
func TestCredentialIn_FilesystemDSNIsNotASecret(t *testing.T) {
	paths := []string{
		"/data/ferrogw-keys.db",
		"./var/lib/ferrogw/keys.db",
		// _pragma=busy_timeout(...) is the spelling modernc.org/sqlite honours;
		// the _busy_timeout= form other drivers use is silently ignored by it.
		"file:/data/ferrogw-keys.db?_pragma=busy_timeout(5000)",
		"/data/ferrogw-keys.db?_journal_mode=WAL",
		":memory:",
	}
	for _, path := range paths {
		if got := credentialIn("API_KEY_STORE_DSN", path); got != "" {
			t.Errorf("credentialIn(API_KEY_STORE_DSN, %q) = %q, want no secret", path, got)
		}
	}
}

// The password inside a DSN is still a credential, and is what gets enrolled.
func TestCredentialIn_DSNPasswordOnly(t *testing.T) {
	got := credentialIn("CONFIG_STORE_DSN", syntheticPostgresDSN)
	if got != syntheticDSNPassword {
		t.Fatalf("credentialIn = %q, want the userinfo password %q", got, syntheticDSNPassword)
	}

	// A plain credential variable is still enrolled whole.
	if got := credentialIn("MISTRAL_API_KEY", mistralShaped); got != mistralShaped {
		t.Errorf("credentialIn(MISTRAL_API_KEY) = %q, want the whole value", got)
	}
}

// End to end through the value redactor: the backup line stays readable while
// the DSN password does not survive.
func TestValues_KeepsPathRedactsPassword(t *testing.T) {
	t.Setenv("API_KEY_STORE_DSN", "/data/ferrogw-keys.db")
	t.Setenv("CONFIG_STORE_DSN", syntheticPostgresDSN)
	reseedFromEnv(t)

	line := "sqlite admin stores keys=/data/ferrogw-keys.db sessions=/data/ferrogw-keys-sessions.db"
	if got := Values(line); got != line {
		t.Errorf("Values(%q) = %q, want it unchanged", line, got)
	}

	failure := "connect config store: " + syntheticPostgresDSN
	got := Values(failure)
	if strings.Contains(got, syntheticDSNPassword) {
		t.Fatalf("Values leaked the DSN password: %q", got)
	}
	if !strings.Contains(got, "db.internal:5432") {
		t.Errorf("Values(%q) = %q, want the host to stay readable", failure, got)
	}
}

// URLCredentials is the shape-based half, so it also covers a DSN that never
// passed through an environment variable and a password under the value floor.
func TestURLCredentials_UserinfoPassword(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			// Under MinSecretLength, so value redaction never enrolled it: the
			// shape rule is what closes this one.
			in:   "dial postgres://ferrogw:" + shortPassword + "@db.internal:5432/gateway failed",
			want: "dial postgres://ferrogw:[REDACTED]@db.internal:5432/gateway failed",
		},
		{
			in:   "redis://default:" + syntheticDSNPassword + "@cache:6379",
			want: "redis://default:[REDACTED]@cache:6379",
		},
		{
			// No userinfo: nothing to redact, everything readable.
			in:   "GET https://api.openai.com/v1/models failed",
			want: "GET https://api.openai.com/v1/models failed",
		},
		{
			// A mailto-shaped string is not a URL with userinfo credentials.
			in:   "contact ops@example.com about http://collector:4318/v1/traces",
			want: "contact ops@example.com about http://collector:4318/v1/traces",
		},
	}
	for _, tc := range cases {
		if got := URLCredentials(tc.in); got != tc.want {
			t.Errorf("URLCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
