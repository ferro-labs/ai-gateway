package novita

// BatchBaseURL implements core.BatchProvider: Novita serves /files and /batches
// beneath its OpenAI-compatible root (…/openai/v1).
func (p *Provider) BatchBaseURL() string { return p.baseURL }

// BatchAuthHeaders implements core.BatchProvider.
func (p *Provider) BatchAuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}
