package providers

import replicatepkg "github.com/ferro-labs/ai-gateway/providers/replicate"

// ReplicateProvider is the Replicate provider.
type ReplicateProvider = replicatepkg.Provider

// NewReplicate creates a new Replicate provider.
//
// Deprecated: Import providers/replicate and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewReplicate(apiToken, baseURL string, textModels, imageModels []string) (*ReplicateProvider, error) {
	return replicatepkg.New(apiToken, baseURL, textModels, imageModels)
}

// re-export private helpers used by tests
var modelVersion = replicatepkg.ModelVersion

type replicatePrediction = replicatepkg.Prediction
