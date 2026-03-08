// Package embedding provides a pluggable interface for text embedding providers.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"
)

// Vector is a float32 embedding vector.
type Vector = []float32

// Embedder generates embedding vectors from text.
type Embedder interface {
	Embed(ctx context.Context, text string) (Vector, error)
	Dims() int
}

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b Vector) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// --- Ollama Provider ---

// OllamaEmbedder uses a local Ollama instance for embeddings.
type OllamaEmbedder struct {
	baseURL string
	model   string
	dims    int
	client  *http.Client
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaResponse struct {
	Embedding []float32 `json:"embedding"`
}

// NewOllamaEmbedder creates an embedder using Ollama's API.
// Default model: nomic-embed-text (768 dims), all-minilm (384 dims).
func NewOllamaEmbedder(model string) *OllamaEmbedder {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	dims := 768 // default for nomic-embed-text
	if model == "all-minilm" {
		dims = 384
	}
	return &OllamaEmbedder{
		baseURL: baseURL,
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *OllamaEmbedder) Embed(ctx context.Context, text string) (Vector, error) {
	body, _ := json.Marshal(ollamaRequest{Model: e.model, Prompt: text})
	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(b))
	}

	var result ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Embedding, nil
}

func (e *OllamaEmbedder) Dims() int { return e.dims }

// --- OpenAI-compatible Provider ---

// OpenAIEmbedder uses any OpenAI-compatible embedding API.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dims    int
	client  *http.Client
}

type openaiEmbedRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type openaiEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// NewOpenAIEmbedder creates an embedder using an OpenAI-compatible API.
func NewOpenAIEmbedder(baseURL, apiKey, model string, dims int) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if dims == 0 {
		dims = 1536
	}
	return &OpenAIEmbedder{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) (Vector, error) {
	body, _ := json.Marshal(openaiEmbedRequest{Input: text, Model: e.model})
	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai error %d: %s", resp.StatusCode, string(b))
	}

	var result openaiEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return result.Data[0].Embedding, nil
}

func (e *OpenAIEmbedder) Dims() int { return e.dims }

// --- Factory ---

// NewFromEnv creates an embedder from environment variables.
// GHOST_EMBED_PROVIDER: "local" (default) | "ollama" | "openai" | "none"
// GHOST_EMBED_MODEL: model name (for ollama/openai)
// GHOST_EMBED_URL: base URL override (for ollama/openai)
// OPENAI_API_KEY: for openai provider
// Legacy AGENT_MEMORY_EMBED_* vars are checked as fallbacks.
//
// When no provider is set, defaults to "local" which runs all-MiniLM-L6-v2
// in pure Go. The model is downloaded on first use (~86MB) to ~/.ghost/models/.
// Set GHOST_EMBED_PROVIDER=none to disable embeddings entirely.
func NewFromEnv() Embedder {
	provider := envWithFallback("GHOST_EMBED_PROVIDER", "AGENT_MEMORY_EMBED_PROVIDER")
	model := envWithFallback("GHOST_EMBED_MODEL", "AGENT_MEMORY_EMBED_MODEL")

	switch provider {
	case "ollama":
		if model == "" {
			model = "nomic-embed-text"
		}
		return NewOllamaEmbedder(model)
	case "openai":
		url := envWithFallback("GHOST_EMBED_URL", "AGENT_MEMORY_EMBED_URL")
		key := os.Getenv("OPENAI_API_KEY")
		return NewOpenAIEmbedder(url, key, model, 0)
	case "none":
		return nil // embeddings explicitly disabled
	default:
		// Default to local embeddings (all-MiniLM-L6-v2, pure Go)
		return NewLocalEmbedder()
	}
}

// envWithFallback returns the value of the primary env var, falling back to
// the legacy env var for backward compatibility during the rename transition.
func envWithFallback(primary, legacy string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(legacy)
}
