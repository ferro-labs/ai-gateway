package providers

import azurefoundrypkg "github.com/ferro-labs/ai-gateway/providers/azure_foundry"

// AzureFoundryProvider is the Azure AI Foundry provider implementation.
type AzureFoundryProvider = azurefoundrypkg.Provider

// NewAzureFoundry creates a new Azure AI Foundry provider.
func NewAzureFoundry(apiKey, baseURL, apiVersion string) (*AzureFoundryProvider, error) {
	return azurefoundrypkg.New(apiKey, baseURL, apiVersion)
}
