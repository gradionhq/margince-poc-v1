package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// ollamaClient is the local/self-host adapter (B-EP06.3): an Ollama
// endpoint on the workspace's own infrastructure. LocalOnly=true makes
// it eligible for the sovereign zero-egress profile — the router
// refuses to bind a sovereign deployment to anything else.
type ollamaClient struct {
	http         *http.Client
	baseURL      string
	defaultModel string
}

type ollamaWire struct {
	Model    string           `json:"model"`
	Messages []wireMessage    `json:"messages"`
	Tools    []ollamaToolWire `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Options  *ollamaOptions   `json:"options,omitempty"`
}

type ollamaOptions struct {
	NumPredict int `json:"num_predict"`
}

type ollamaToolWire struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type ollamaChatEvent struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count"`
	EvalCount       int  `json:"eval_count"`
}

func (c *ollamaClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.chat(ctx, req, false)
	if err != nil {
		return model.Response{}, err
	}
	defer func() { _ = body.Close() }()
	var out ollamaChatEvent
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: ollama: decode response: %w", err)
	}
	return model.Response{
		Text:         out.Message.Content,
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
	}, nil
}

func (c *ollamaClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.chat(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return &ollamaStream{body: body, scanner: bufio.NewScanner(body)}, nil
}

func (c *ollamaClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	embedModel := req.Model
	if embedModel == "" {
		embedModel = c.defaultModel
	}
	payload, _, err := sendablePayload(ctx, map[string]any{"model": embedModel, "input": req.Inputs}, nil)
	if err != nil {
		return model.Embeddings{}, err
	}
	body, err := c.post(ctx, "/api/embed", payload)
	if err != nil {
		return model.Embeddings{}, err
	}
	defer func() { _ = body.Close() }()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Embeddings{}, fmt.Errorf("ai: ollama: decode embeddings: %w", err)
	}
	dims := 0
	if len(out.Embeddings) > 0 {
		dims = len(out.Embeddings[0])
	}
	return model.Embeddings{Vectors: out.Embeddings, Dims: dims}, nil
}

func (c *ollamaClient) Caps() model.Capabilities {
	// EmbedDims stays 0 (unknown): the width is a property of whichever
	// model the deployment pulled, discovered from the first Embed call.
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: true}
}

func (c *ollamaClient) chat(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	wire := ollamaWire{Model: req.Model, Stream: stream}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	if req.MaxTokens > 0 {
		wire.Options = &ollamaOptions{NumPredict: req.MaxTokens}
	}
	// Ollama has no top-level system field; the system prompt travels as
	// the leading message.
	wire.Messages = wireMessages(req.System, req.Messages)
	for _, tool := range req.Tools {
		var tw ollamaToolWire
		tw.Type = "function"
		tw.Function.Name = tool.Name
		tw.Function.Description = tool.Description
		tw.Function.Parameters = tool.InputSchema
		wire.Tools = append(wire.Tools, tw)
	}
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, err
	}
	return c.post(ctx, "/api/chat", payload)
}

func (c *ollamaClient) post(ctx context.Context, path string, payload []byte) (io.ReadCloser, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: ollama: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ai: ollama: http %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	return resp.Body, nil
}

// ollamaStream reads the JSON-lines chat stream.
type ollamaStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (s *ollamaStream) Next(ctx context.Context) (string, bool, error) {
	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		var ev ollamaChatEvent
		if err := json.Unmarshal(s.scanner.Bytes(), &ev); err != nil {
			return "", false, fmt.Errorf("ai: ollama: stream event: %w", err)
		}
		if ev.Done {
			return "", false, nil
		}
		if ev.Message.Content != "" {
			return ev.Message.Content, true, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: ollama: stream: %w", err)
	}
	return "", false, nil
}

func (s *ollamaStream) Close() error { return s.body.Close() }
