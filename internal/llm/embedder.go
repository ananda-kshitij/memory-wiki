package llm

import (
	"context"
	"fmt"
	"os"

	openai "github.com/sashabaranov/go-openai"
)

// Embedder converts text into a vector embedding.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// OpenAIEmbedder uses OpenAI text-embedding-3-small (1536 dims).
type OpenAIEmbedder struct {
	client *openai.Client
}

// NewOpenAIEmbedder returns nil if OPENAI_API_KEY is not set, so callers can
// treat a nil embedder as "semantic search unavailable, fall back to keyword".
func NewOpenAIEmbedder() *OpenAIEmbedder {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil
	}
	return &OpenAIEmbedder{client: openai.NewClient(key)}
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input: []string{text},
		Model: openai.SmallEmbedding3,
	})
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("openai embed: empty response")
	}
	return resp.Data[0].Embedding, nil
}
