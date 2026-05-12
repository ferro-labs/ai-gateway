//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestEmbeddings_Success(t *testing.T) {
	env := newTestServer(t)

	body := `{
		"model": "` + stubEmbedModel + `",
		"input": "Hello world"
	}`

	req, _ := http.NewRequest("POST", env.Server.URL+"/v1/embeddings", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/embeddings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Object    string    `json:"object"`
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result.Object != "list" {
		t.Errorf("expected object=list, got %q", result.Object)
	}

	if len(result.Data) == 0 {
		t.Fatal("expected at least one embedding")
	}

	embedding := result.Data[0]
	if embedding.Object != "embedding" {
		t.Errorf("expected embedding object=embedding, got %q", embedding.Object)
	}
	if len(embedding.Embedding) == 0 {
		t.Error("expected non-empty embedding vector")
	}

	if result.Usage.TotalTokens == 0 {
		t.Error("expected non-zero usage")
	}
}

func TestEmbeddings_MissingModel_Returns400(t *testing.T) {
	env := newTestServer(t)

	body := `{"input": "Hello world"}`

	req, _ := http.NewRequest("POST", env.Server.URL+"/v1/embeddings", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestEmbeddings_MissingInput_Returns400(t *testing.T) {
	env := newTestServer(t)

	body := `{"model": "` + stubEmbedModel + `"}`

	req, _ := http.NewRequest("POST", env.Server.URL+"/v1/embeddings", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
