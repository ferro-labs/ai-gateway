package handler

import (
	"encoding/json"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
)

// Capabilities handles GET /v1/capabilities. It returns the parameter
// capability profile for each registered provider, so clients can discover
// which OpenAI chat parameters a provider forwards, translates, or does not
// support. Each profile maps a parameter name to "forward", "translate", or
// "unsupported".
func Capabilities(reg *providers.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		names := reg.List()
		profiles := make(map[string]map[string]string, len(names))
		for _, name := range names {
			profile := capabilities.ProfileOf(name)
			out := make(map[string]string, len(profile))
			for param, support := range profile {
				out[param] = support.String()
			}
			profiles[name] = out
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": profiles})
	}
}
