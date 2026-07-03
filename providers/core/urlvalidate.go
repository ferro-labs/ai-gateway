package core

import (
	"fmt"
	"net/url"
)

// ValidateBaseURL reports an error when rawURL is not a valid absolute HTTP(S)
// URL with a host. Providers call it from their constructors so a misconfigured
// base URL fails fast at startup instead of producing malformed upstream
// requests. name is the provider identifier, included in the error for
// diagnostics.
func ValidateBaseURL(name, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s: invalid base URL %q: must be http or https with a host", name, rawURL)
	}
	return nil
}
