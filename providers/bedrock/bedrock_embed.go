package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// bedrockTitanEmbedConcurrency bounds the number of Titan embedding calls
// issued in parallel for a single batch request, since Titan's API accepts
// one input per call. Kept small to avoid Bedrock throttling.
const bedrockTitanEmbedConcurrency = 4

type bedrockTitanEmbedRequest struct {
	InputText  string `json:"inputText"`
	Dimensions *int   `json:"dimensions,omitempty"`
}

type bedrockTitanEmbedResponse struct {
	Embedding           []float64 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

type bedrockCohereEmbedRequest struct {
	Texts          []string `json:"texts"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types,omitempty"`
}

type bedrockCohereEmbeddingVectors [][]float64

func (v *bedrockCohereEmbeddingVectors) UnmarshalJSON(data []byte) error {
	var vectors [][]float64
	if err := json.Unmarshal(data, &vectors); err == nil {
		*v = vectors
		return nil
	}

	var typed map[string][][]float64
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}
	if vectors, ok := typed["float"]; ok {
		*v = vectors
		return nil
	}
	return fmt.Errorf("cohere embedding response did not include float embeddings")
}

type bedrockCohereEmbedResponse struct {
	Embeddings bedrockCohereEmbeddingVectors `json:"embeddings"`
	Meta       struct {
		BilledUnits struct {
			InputTokens int `json:"input_tokens"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// Embed sends a text embedding request to AWS Bedrock.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	texts, err := core.CoerceEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}

	switch req.EncodingFormat {
	case "", "float":
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; Bedrock embeddings return float vectors", req.EncodingFormat)
	}

	modelID := bedrockModelRoutingID(req.Model)
	switch {
	case isBedrockTitanTextEmbeddingModel(modelID):
		return p.embedTitan(ctx, req, modelID, texts)
	case isBedrockCohereEmbeddingModel(modelID):
		return p.embedCohere(ctx, req, modelID, texts)
	default:
		return nil, fmt.Errorf("unsupported Bedrock embedding model: %s", req.Model)
	}
}

func (p *Provider) embedTitan(ctx context.Context, req core.EmbeddingRequest, modelID string, texts []string) (*core.EmbeddingResponse, error) {
	if req.Dimensions != nil && !strings.HasPrefix(modelID, "amazon.titan-embed-text-v2") {
		return nil, fmt.Errorf("embed: dimensions are only supported for amazon.titan-embed-text-v2 models")
	}

	data := make([]core.Embedding, len(texts))
	tokenCounts := make([]int, len(texts))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(bedrockTitanEmbedConcurrency)
	for i, text := range texts {
		g.Go(func() error {
			titanReq := bedrockTitanEmbedRequest{
				InputText:  text,
				Dimensions: req.Dimensions,
			}
			var titanResp bedrockTitanEmbedResponse
			if err := p.invokeModelJSON(gCtx, req.Model, titanReq, &titanResp); err != nil {
				return err
			}
			data[i] = core.Embedding{
				Object:    "embedding",
				Embedding: titanResp.Embedding,
				Index:     i,
			}
			tokenCounts[i] = titanResp.InputTextTokenCount
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	promptTokens := 0
	for _, c := range tokenCounts {
		promptTokens += c
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: promptTokens,
			TotalTokens:  promptTokens,
		},
	}, nil
}

func (p *Provider) embedCohere(ctx context.Context, req core.EmbeddingRequest, modelID string, texts []string) (*core.EmbeddingResponse, error) {
	if req.Dimensions != nil {
		return nil, fmt.Errorf("embed: dimensions are not supported for Bedrock Cohere embeddings")
	}

	cohereReq := bedrockCohereEmbedRequest{
		Texts:     texts,
		InputType: "search_document",
	}
	if strings.HasPrefix(modelID, "cohere.embed-v4") {
		cohereReq.EmbeddingTypes = []string{"float"}
	}

	var cohereResp bedrockCohereEmbedResponse
	if err := p.invokeModelJSON(ctx, req.Model, cohereReq, &cohereResp); err != nil {
		return nil, err
	}
	if len(cohereResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("bedrock cohere embed response returned %d embeddings for %d inputs", len(cohereResp.Embeddings), len(texts))
	}

	data := make([]core.Embedding, len(cohereResp.Embeddings))
	for i, emb := range cohereResp.Embeddings {
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: emb,
			Index:     i,
		}
	}
	inputTokens := cohereResp.Meta.BilledUnits.InputTokens
	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: inputTokens,
			TotalTokens:  inputTokens,
		},
	}, nil
}

func isBedrockTitanTextEmbeddingModel(model string) bool {
	return strings.HasPrefix(model, "amazon.titan-embed-text-")
}

func isBedrockCohereEmbeddingModel(model string) bool {
	return strings.HasPrefix(model, "cohere.embed-")
}
