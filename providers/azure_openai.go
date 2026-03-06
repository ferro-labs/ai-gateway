package providers

import azureopenapipkg "github.com/ferro-labs/ai-gateway/providers/azure_openai"

// AzureOpenAIProvider is the Azure OpenAI provider.
type AzureOpenAIProvider = azureopenapipkg.Provider

// NewAzureOpenAI creates a new Azure OpenAI provider.
func NewAzureOpenAI(apiKey, baseURL, deploymentName, apiVersion string) (*AzureOpenAIProvider, error) {
return azureopenapipkg.New(apiKey, baseURL, deploymentName, apiVersion)
}
