package azureopenai

// BatchBaseURL implements core.BatchProvider. Unlike Azure's chat surface — which
// hangs beneath a per-deployment path — the Files and Batch APIs live on the
// resource's OpenAI-compatible root, /openai/v1, resolved once in New rather than
// appended here, so batch pass-through targets that root rather than the
// deployment path core.ResolveAPIRoot builds for chat.
func (p *Provider) BatchBaseURL() string { return p.batchBaseURL }

// BatchAuthHeaders implements core.BatchProvider: Azure authenticates with the
// api-key header, not an OpenAI-style bearer token.
func (p *Provider) BatchAuthHeaders() map[string]string {
	return map[string]string{"api-key": p.apiKey}
}
