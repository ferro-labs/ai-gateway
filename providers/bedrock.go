package providers

import bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"

// BedrockOptions configures AWS Bedrock provider initialization.
type BedrockOptions = bedrockpkg.Options

// BedrockProvider is the AWS Bedrock provider.
type BedrockProvider = bedrockpkg.Provider

// NewBedrock creates a new AWS Bedrock provider.
// Region defaults to us-east-1.
//
// Deprecated: Import providers/bedrock and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewBedrock(region string) (*BedrockProvider, error) {
return bedrockpkg.New(region)
}

// NewBedrockWithOptions creates a new AWS Bedrock provider from options.
//
// Deprecated: Import providers/bedrock and call NewWithOptions directly.
// This compatibility wrapper will be removed in a future major version.
func NewBedrockWithOptions(opts BedrockOptions) (*BedrockProvider, error) {
return bedrockpkg.NewWithOptions(opts)
}
