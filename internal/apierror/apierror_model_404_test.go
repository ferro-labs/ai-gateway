package apierror

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Two different 404s reach a caller and they had one message between them. When
// the catalog advertises a model the provider has since retired — gemini-2.5-flash,
// imagen-4.0-fast-generate-001 — routing succeeds, the owner is called, and the
// PROVIDER answers 404. Reporting that as "no configured provider serves the
// requested model" sends the operator to a targets list that is correct.
//
// Status and code stay identical on purpose: clients switch on the code and both
// cases really are "not available here". Only the sentence a human reads differs.
func TestWriteRouteError_UpstreamAndRoutingModel404sReadDifferently(t *testing.T) {
	routing := fmt.Errorf("no provider supports model gemini-2.5-flash: %w", core.ErrNoCapableProvider)
	upstream := fmt.Errorf("target gemini: %w",
		core.APIError("gemini", http.StatusNotFound,
			[]byte(`{"error":{"message":"model is no longer available to new users"}}`)))

	got := map[string]string{}
	for label, err := range map[string]error{"routing": routing, "upstream": upstream} {
		w := httptest.NewRecorder()
		WriteRouteError(w, err)

		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want %d", label, w.Code, http.StatusNotFound)
		}
		message, errType, code := decodeError(t, w)
		if errType != errTypeInvalidRequest || code != codeModelNotFound {
			t.Errorf("%s: type/code = %q/%q, want %q/%q", label, errType, code, errTypeInvalidRequest, codeModelNotFound)
		}
		got[label] = message
	}

	if got["routing"] != msgModelNotFound {
		t.Errorf("routing message = %q, want %q", got["routing"], msgModelNotFound)
	}
	if got["upstream"] == got["routing"] {
		t.Errorf("both 404s answered %q; the upstream case must say the provider retired the model, "+
			"not that no provider is configured for it", got["upstream"])
	}
	if strings.Contains(got["upstream"], "no configured provider") {
		t.Errorf("upstream message = %q; it blames the operator's config for a model the "+
			"gateway routed correctly", got["upstream"])
	}
}
