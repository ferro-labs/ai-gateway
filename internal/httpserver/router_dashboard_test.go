package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// The login page is the entry point to the whole dashboard, and it is served
// with the same strict CSP as every other response. Its script must therefore be
// an external file the policy allows, not an inline block the policy kills.
func TestDashboardLoginPageIsServableUnderCSP(t *testing.T) {
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	router := buildTestRouter(t, gw)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dashboard/login", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login page status = %d, want 200", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("login page CSP = %q, want a strict script-src", csp)
	}

	page := w.Body.String()
	if strings.Contains(page, "<script>") {
		t.Fatal("login page carries an inline <script>, which script-src 'self' blocks")
	}
	const scriptPath = "/dashboard/static/pages/login.js"
	if !strings.Contains(page, scriptPath) {
		t.Fatalf("login page does not reference %s", scriptPath)
	}

	// And that script must actually be served, or the form never wires up.
	scriptReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, scriptPath, nil)
	scriptW := httptest.NewRecorder()
	router.ServeHTTP(scriptW, scriptReq)

	if scriptW.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200", scriptPath, scriptW.Code)
	}
	if !strings.Contains(scriptW.Body.String(), "login-form") {
		t.Fatalf("%s does not contain the login form handler", scriptPath)
	}
}
