package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLM roles as defined by the OpenAI chat API.
const (
	LLMRoleSystem    = "system"
	LLMRoleUser      = "user"
	LLMRoleAssistant = "assistant"
)

// LLMMessage is one chat turn.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMUsage reports token accounting from a completion, when the provider sends it.
type LLMUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// LLMChatOptions configures an OpenAI-compatible chat client.
type LLMChatOptions struct {
	// BaseURL is the API root, e.g. https://api.deepseek.com or
	// https://dashscope.aliyuncs.com/compatible-mode/v1. A full endpoint URL
	// ending in /chat/completions is also accepted.
	BaseURL     string
	APIKey      string
	Model       string
	Proxy       string
	Timeout     time.Duration
	MaxTokens   int
	Temperature *float64
}

// LLMChatClient calls any OpenAI-compatible /chat/completions endpoint.
//
// Streaming is deliberately not used: the caller turns the answer into speech,
// so nothing can be spoken until the full reply exists anyway.
type LLMChatClient struct {
	endpoint    string
	apiKey      string
	model       string
	maxTokens   int
	temperature *float64
	client      *http.Client
}

// NewLLMChatClient validates options and builds a client.
func NewLLMChatClient(opts LLMChatOptions) (*LLMChatClient, error) {
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("llm: base_url is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	hc, err := httpClient(opts.Proxy, timeout)
	if err != nil {
		return nil, err
	}
	return &LLMChatClient{
		endpoint:    llmCompletionsURL(base),
		apiKey:      strings.TrimSpace(opts.APIKey),
		model:       strings.TrimSpace(opts.Model),
		maxTokens:   opts.MaxTokens,
		temperature: opts.Temperature,
		client:      hc,
	}, nil
}

// llmCompletionsURL appends the chat path unless the caller already gave one.
// Providers disagree on whether /v1 belongs in the base URL, so both
// "https://api.deepseek.com" and "https://api.openai.com/v1" must work.
func llmCompletionsURL(base string) string {
	base = strings.TrimSpace(base)
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/chat/completions"
}

// Model returns the configured model id.
func (c *LLMChatClient) Model() string { return c.model }

// Endpoint returns the resolved completions URL (used in logs and CLI output).
func (c *LLMChatClient) Endpoint() string { return c.endpoint }

type llmChatRequest struct {
	Model       string       `json:"model"`
	Messages    []LLMMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	Stream      bool         `json:"stream"`
}

type llmChatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *LLMUsage `json:"usage"`
	// Providers report failures in either shape.
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
	// DeepSeek/Qwen style flat message on error.
	Message string `json:"msg"`
}

// Chat sends the conversation and returns the assistant reply.
func (c *LLMChatClient) Chat(ctx context.Context, messages []LLMMessage) (string, *LLMUsage, error) {
	if len(messages) == 0 {
		return "", nil, fmt.Errorf("llm: no messages to send")
	}

	payload := llmChatRequest{
		Model:       c.model,
		Messages:    messages,
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
		Stream:      false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", nil, fmt.Errorf("read llm response: %w", err)
	}

	var parsed llmChatResponse
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			if resp.StatusCode >= 400 {
				return "", nil, fmt.Errorf("llm HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
			}
			return "", nil, fmt.Errorf("decode llm response: %w", err)
		}
	}

	if resp.StatusCode >= 400 || parsed.Error != nil {
		return "", nil, fmt.Errorf("llm error: %s", llmErrorText(resp.StatusCode, &parsed, raw))
	}
	if len(parsed.Choices) == 0 {
		return "", nil, fmt.Errorf("llm returned no choices: %s", truncate(string(raw), 300))
	}

	text := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if text == "" {
		return "", nil, fmt.Errorf("llm returned an empty reply (finish_reason=%s)",
			parsed.Choices[0].FinishReason)
	}
	return text, parsed.Usage, nil
}

// llmErrorText assembles the most specific message the provider gave us.
func llmErrorText(status int, parsed *llmChatResponse, raw []byte) string {
	if parsed != nil && parsed.Error != nil {
		msg := strings.TrimSpace(parsed.Error.Message)
		if msg == "" {
			msg = "unspecified provider error"
		}
		if parsed.Error.Code != nil {
			return fmt.Sprintf("HTTP %d code=%v: %s", status, parsed.Error.Code, msg)
		}
		return fmt.Sprintf("HTTP %d: %s", status, msg)
	}
	if parsed != nil && strings.TrimSpace(parsed.Message) != "" {
		return fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(parsed.Message))
	}
	return fmt.Sprintf("HTTP %d: %s", status, truncate(string(raw), 300))
}
