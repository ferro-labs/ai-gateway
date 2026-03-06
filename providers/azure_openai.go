package providers

import azureopenapipkg "github.com/ferro-labs/ai-gateway/providers/azure_openai"

// AzureOpenAIProvider is the Azure OpenAI provider.
type AzureOpenAIProvider = azureopenapipkg.Provider

// NewAzureOpenAI creates a new Azure OpenAI provider.
//
// Deprecated: Import providers/azure_openai and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewAzureOpenAI(apiKey, baseURL, deploymentName, apiVersion string) (*AzureOpenAIProvider, error) {
return azureopenapipkg.New(apiKey, baseURL, deploymentName, apiVersion)
}
