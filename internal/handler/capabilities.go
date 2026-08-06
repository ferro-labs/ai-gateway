package handler

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Capabilities handles GET /v1/capabilities. It returns the parameter
// capability profile for each provider this gateway serves, so clients can
// discover which OpenAI chat parameters a provider forwards, translates, or does
// not support. Each profile maps a parameter name to "forward", "translate", or
// "unsupported".
//
// The served set is the gateway's (Gateway.ServedProviders) — the same one
// /v1/models answers from. Reading the registered provider set instead
// described providers no target names, and a client that consulted a profile to
// decide whether a parameter would be forwarded was told yes by a provider
// every routed surface then 404'd.
//
// image_response_formats is the separate answer for /v1/images/generations,
// where response_format names an output representation rather than JSON mode.
// A provider appears there only when it is restricted; an absent provider
// honours whatever it is asked for. The restriction is enforced (HTTP 400 on an
// explicitly requested format the provider cannot produce), so publishing it is
// what lets a client pick a format instead of discovering the limit as an error.
func Capabilities(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		names := gw.ServedProviders()
		profiles := make(map[string]map[string]string, len(names))
		imageFormats := make(map[string][]string)
		for _, name := range names {
			profile := capabilities.ProfileOf(name)
			out := make(map[string]string, len(profile))
			for param, support := range profile {
				out[param] = support.String()
			}
			profiles[name] = out
			if formats := core.ImageResponseFormatsFor(name); len(formats) > 0 {
				imageFormats[name] = formats
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"providers":              profiles,
			"image_response_formats": imageFormats,
		})
	}
}
