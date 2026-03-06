package core

// ModelInfo describes a single model offered by a provider.
// Fields match the OpenAI /v1/models response schema.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelsFromList builds a ModelInfo slice from a list of model IDs.
// Provider subpackages call this to avoid repeating boilerplate in Models().
func ModelsFromList(providerName string, ids []string) []ModelInfo {
	models := make([]ModelInfo, len(ids))
	for i, id := range ids {
		models[i] = ModelInfo{
			ID:      id,
			Object:  "model",
			OwnedBy: providerName,
		}
	}
	return models
}
