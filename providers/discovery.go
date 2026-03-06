package providers

import (
	"context"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/internal/discovery"
)

// discoverOpenAICompatibleModels is a package-level convenience wrapper around
// discovery.DiscoverOpenAICompatibleModels. Provider sub-packages should import
// providers/internal/discovery directly; this wrapper exists for the root-package
// implementations that have not yet migrated to sub-packages.
func discoverOpenAICompatibleModels(ctx context.Context, client *http.Client, url, apiKey, providerName string) ([]ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, client, url, apiKey, providerName)
}
