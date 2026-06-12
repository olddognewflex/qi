// Package embed is a thin Ollama embeddings client used by `qi embed` (build)
// and `qi search --semantic` (query). It depends only on the stdlib; the index
// stores and cosine-ranks the raw vectors it returns.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// DefaultModel is the embedding model used when none is configured. It must
// already be pulled into the local Ollama install.
const DefaultModel = "nomic-embed-text"

// DefaultOllamaURL is the default base URL for a locally-running Ollama daemon.
const DefaultOllamaURL = "http://localhost:11434"

// Embedder turns text into a dense vector. Implemented by OllamaEmbedder; the
// interface lets callers (and tests) swap the backend.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// OllamaEmbedder calls Ollama's /api/embed endpoint.
type OllamaEmbedder struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewOllamaEmbedder constructs an embedder against baseURL using model. Pass ""
// for either to use DefaultOllamaURL / DefaultModel, and nil for httpClient to
// use http.DefaultClient.
func NewOllamaEmbedder(baseURL, model string, httpClient *http.Client) *OllamaEmbedder {
	if baseURL == "" {
		baseURL = DefaultOllamaURL
	}
	if model == "" {
		model = DefaultModel
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OllamaEmbedder{baseURL: baseURL, model: model, http: httpClient}
}

// Model returns the embedding model name, so callers can stamp it on stored
// vectors (the index keys embeddings by model).
func (e *OllamaEmbedder) Model() string {
	return e.model
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns the embedding vector for text. Ollama returns an array-of-
// arrays (one per input); we send a single input and take [0].
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("embed: %s: %s", resp.Status, string(buf))
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(out.Embeddings) == 0 || len(out.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("embed: empty embeddings in response")
	}
	return out.Embeddings[0], nil
}
