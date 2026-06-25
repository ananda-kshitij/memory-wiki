package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/Codex-AK/memory-wiki/internal/models"
)

const systemPrompt = `You are a memory extraction assistant. Given a conversation transcript, extract structured memories organized by category.

For each distinct piece of information worth remembering, output a JSON object with:
- "category": one of "people", "topics", "projects", "events", "preferences"
- "name": a short kebab-case identifier (e.g. "alice-johnson", "machine-learning", "project-phoenix")
- "tags": a list of relevant lowercase tags
- "content": markdown-formatted content summarizing what was learned

Output ONLY a JSON array of memory entries. No explanation. No markdown code fences.`

const reconcileSystemPrompt = `You are a memory management assistant. Your task is to merge an existing memory file with new information into a single coherent, deduplicated profile.

Rules:
- Preserve ALL unique facts from both the existing content and the new entry.
- Remove redundant or duplicate information — do not repeat the same fact twice.
- Write in clear, readable markdown. Use headers and bullet points where appropriate.
- Do NOT include YAML frontmatter — output only the markdown body.
- Do NOT add any preamble or explanation — output only the merged markdown body.`

type Client struct {
	ac anthropic.Client
}

func New() *Client {
	return &Client{ac: anthropic.NewClient()}
}

func (c *Client) ExtractMemories(ctx context.Context, transcript string) ([]models.MemoryEntry, error) {
	msg, err := c.ac.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeOpus4_8,
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(
				fmt.Sprintf("Extract memories from this transcript:\n\n%s", transcript),
			)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude api: %w", err)
	}

	raw := extractText(msg)
	raw = strings.TrimSpace(raw)

	var entries []models.MemoryEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse llm output: %w\n\nraw: %s", err, raw)
	}
	return entries, nil
}

// ReconcileMemory calls the LLM to merge existingContent (the full .md file)
// with the new entry into a single coherent markdown body (no frontmatter).
func (c *Client) ReconcileMemory(ctx context.Context, existingContent string, entry models.MemoryEntry, transcriptID string) (string, error) {
	userMsg := fmt.Sprintf(
		"Existing memory file content:\n\n%s\n\n---\n\nNew information to incorporate (from transcript %s):\n\n%s",
		existingContent, transcriptID, entry.Content,
	)

	msg, err := c.ac.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeOpus4_8,
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: reconcileSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("claude api reconcile: %w", err)
	}

	body := strings.TrimSpace(extractText(msg))
	return body, nil
}

func extractText(msg *anthropic.Message) string {
	var sb strings.Builder
	for _, block := range msg.Content {
		t := block.AsText()
		if t.Text != "" {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}
