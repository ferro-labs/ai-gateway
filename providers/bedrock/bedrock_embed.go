package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

	// The shared validator accepts exactly the set Bedrock embeddings serve
	// ("" and "float"), and returns the typed 400 the hand-rolled check did not.
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}

	modelID := bedrockModelRoutingID(req.Model)
	switch {
	case isBedrockTitanTextEmbeddingModel(modelID):
		return p.embedTitan(ctx, req, modelID, texts)
	case isBedrockCohereEmbeddingModel(modelID):
		return p.embedCohere(ctx, req, modelID, texts)
	default:
		// The caller named a model this provider does not embed with. That is a
		// 400, not the 500 a bare error classifies as, and no retry changes it.
		return nil, core.StatusError(Name, http.StatusBadRequest,
			"unsupported Bedrock embedding model: "+req.Model)
	}
}

func (p *Provider) embedTitan(ctx context.Context, req core.EmbeddingRequest, modelID string, texts []string) (*core.EmbeddingResponse, error) {
	if req.Dimensions != nil && !strings.HasPrefix(modelID, "amazon.titan-embed-text-v2") {
		return nil, core.StatusError(Name, http.StatusBadRequest,
			"embed: dimensions are only supported for amazon.titan-embed-text-v2 models")
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

// cohereEmbedDefaultInputType is Cohere's document-indexing distribution, used
// when the caller does not specify an input_type.
const cohereEmbedDefaultInputType = "search_document"

// cohereEmbedInputTypes are the input_type values Bedrock's Cohere embedding
// models accept. Mirrors the standalone Cohere provider so both paths reject the
// same unknown values.
var cohereEmbedInputTypes = map[string]bool{
	cohereEmbedDefaultInputType: true,
	"search_query":              true,
	"classification":            true,
	"clustering":                true,
}

// resolveCohereInputType validates a caller-supplied input_type, defaulting to
// "search_document" when unset. Query embeddings must use "search_query" for
// retrieval to work, so honoring the override is what lets that succeed.
func resolveCohereInputType(requested string) (string, error) {
	if requested == "" {
		return cohereEmbedDefaultInputType, nil
	}
	if !cohereEmbedInputTypes[requested] {
		return "", core.StatusError(Name, http.StatusBadRequest,
			fmt.Sprintf("embed: unsupported input_type %q; want one of search_document, search_query, classification, clustering", requested))
	}
	return requested, nil
}

func (p *Provider) embedCohere(ctx context.Context, req core.EmbeddingRequest, modelID string, texts []string) (*core.EmbeddingResponse, error) {
	if req.Dimensions != nil {
		return nil, core.StatusError(Name, http.StatusBadRequest,
			"embed: dimensions are not supported for Bedrock Cohere embeddings")
	}

	inputType, err := resolveCohereInputType(req.InputType)
	if err != nil {
		return nil, err
	}
	cohereReq := bedrockCohereEmbedRequest{
		Texts:     texts,
		InputType: inputType,
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
