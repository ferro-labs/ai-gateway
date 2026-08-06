package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
)

// isolateAWSEnv points the SDK at empty shared-config files and clears the
// ambient credential and CA-bundle variables, so a developer's own ~/.aws does
// not decide what these tests observe.
func isolateAWSEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_CA_BUNDLE", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	return dir
}

// TestCredentialChainClientRefusesRedirects pins VERIFY-006.
//
// resolveHTTPClient runs before resolveCredentials inside LoadDefaultConfig, so
// the IMDS, SSO, STS and ssooidc clients are built during the call. Assigning
// cfg.HTTPClient afterwards reaches the Bedrock data plane only, leaving those
// clients on the SDK's BuildableClient — which follows 307 and 308, and replays
// the custom IMDSv2 x-aws-ec2-metadata-token and SSO x-amz-sso_bearer_token
// headers verbatim to the redirect target, because neither is one of the
// headers Go or the SDK strips across hosts.
func TestCredentialChainClientRefusesRedirects(t *testing.T) {
	isolateAWSEnv(t)

	cfg, err := loadAWSConfig(context.Background(), []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion("us-east-1"),
	})
	if err != nil {
		t.Fatalf("loadAWSConfig: %v", err)
	}

	// This is the value resolveCredentials hands to imds/sso/sts.NewFromConfig.
	client, ok := cfg.HTTPClient.(*http.Client)
	if !ok {
		t.Fatalf("credential chain HTTP client is %T; the gateway's client was not passed into LoadDefaultConfig, so the chain kept the SDK's redirect-following one", cfg.HTTPClient)
	}
	if client != providerhttp.ForProvider(Name) {
		t.Errorf("credential chain HTTP client is not the gateway's client for %q", Name)
	}

	// Behavioural half: a 307 is surfaced, and the custom credential header is
	// not replayed to the redirect target.
	const sentinel = "sentinel-not-a-real-credential-0201"
	var (
		reached   bool
		gotIMDS   string
		gotBearer string
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		gotIMDS = r.Header.Get("x-aws-ec2-metadata-token")
		gotBearer = r.Header.Get("x-amz-sso_bearer_token")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer metadata.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, metadata.URL+"/latest/meta-data/iam/security-credentials/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-aws-ec2-metadata-token", sentinel)
	req.Header.Set("x-amz-sso_bearer_token", sentinel)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want the 307 surfaced rather than followed", resp.StatusCode)
	}
	if reached {
		t.Error("the redirect target was contacted; the credential-chain client followed the 307")
	}
	if gotIMDS != "" || gotBearer != "" {
		t.Errorf("credential replayed to the redirect target (imds token set=%t, sso bearer set=%t)", gotIMDS != "", gotBearer != "")
	}
}

// caBundlePEM is a self-signed test certificate. Its contents are irrelevant —
// it only has to be PEM the SDK accepts, so AWS_CA_BUNDLE is genuinely in play.
const caBundlePEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----
`

// TestCABundleDeploymentStillStarts is the other half of the same change.
//
// resolveCustomCABundle can only add RootCAs to a *awshttp.BuildableClient and
// hard-errors on anything else, so passing our own client unconditionally turns
// every AWS_CA_BUNDLE (or shared-config ca_bundle) deployment into a startup
// failure. Resolving the bundle ourselves would not have avoided it: the SDK
// resolves it independently and fails the same way regardless.
func TestCABundleDeploymentStillStarts(t *testing.T) {
	for _, source := range []string{"AWS_CA_BUNDLE", "shared-config ca_bundle"} {
		t.Run(source, func(t *testing.T) {
			dir := isolateAWSEnv(t)
			pem := filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(pem, []byte(caBundlePEM), 0o600); err != nil {
				t.Fatalf("write bundle: %v", err)
			}
			if source == "AWS_CA_BUNDLE" {
				t.Setenv("AWS_CA_BUNDLE", pem)
			} else {
				if err := os.WriteFile(filepath.Join(dir, "config"),
					[]byte("[default]\nca_bundle = "+pem+"\n"), 0o600); err != nil {
					t.Fatalf("write shared config: %v", err)
				}
			}

			p, err := NewWithOptions(Options{Region: "us-east-1"})
			if err != nil {
				t.Fatalf("a deployment configuring a custom CA bundle must still start: %v", err)
			}
			if p.Region() != "us-east-1" {
				t.Errorf("region = %q, want us-east-1", p.Region())
			}
		})
	}
}
