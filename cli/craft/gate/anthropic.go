package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// AnthropicClient is the interim cloud binding for the review seam (stands in for
// B-EP06.2's full model.Client adapter). It speaks the Messages API with the
// stdlib only — no new dependency (T9). The pinned model id is part of the gate
// identity tuple, so it is explicit, never a floating "latest" alias.
type AnthropicClient struct {
	apiKey string
	model  string
	http   *http.Client
}

const anthropicMessagesURL = "https://api.anthropic.com/v1/messages"

// NewAnthropicClient builds the client for the given pinned model id (the single
// source of truth is gate-version.json, read by the caller). The API key comes
// from the environment because it is a secret, not part of the gate identity.
func NewAnthropicClient(model string) (*AnthropicClient, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is unset")
	}
	if model == "" {
		return nil, fmt.Errorf("model id is empty (it is pinned in gate-version.json and is part of gate_version)")
	}
	return &AnthropicClient{apiKey: key, model: model, http: &http.Client{Timeout: 5 * time.Minute}}, nil
}

// Complete sends the prompt to the Messages API and returns the first text block
// of the completion, or an error on a non-200 response or empty content.
func (c *AnthropicClient) Complete(ctx context.Context, prompt string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":      c.model,
		"max_tokens": 8192,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode messages response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != nil {
			return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, out.Error.Message)
		}
		return "", fmt.Errorf("anthropic %d", resp.StatusCode)
	}
	if len(out.Content) == 0 {
		return "", fmt.Errorf("empty completion")
	}
	return out.Content[0].Text, nil
}
