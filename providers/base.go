package providers

// Base provides common fields and methods shared by provider implementations.
// Embed this struct to avoid repeating name and baseURL handling across providers.
type Base struct {
	name    string
	baseURL string
}

// Name returns the provider name.
func (b *Base) Name() string { return b.name }

// BaseURL returns the provider base URL, satisfying the ProxiableProvider interface.
func (b *Base) BaseURL() string { return b.baseURL }

// ModelsFromList builds a ModelInfo slice from a list of model IDs.
// Provider Models() implementations call this to avoid repetitive boilerplate.
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
