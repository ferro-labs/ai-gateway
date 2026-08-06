package openai

// BatchBaseURL implements core.BatchProvider: OpenAI serves /files and /batches
// directly beneath its API root.
func (p *Provider) BatchBaseURL() string { return p.baseURL }

// BatchAuthHeaders implements core.BatchProvider.
func (p *Provider) BatchAuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}
