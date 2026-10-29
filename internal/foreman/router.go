package foreman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const intentRouterSystem = "Classify the user's message. Reply with exactly one word: \"light\" if it is small talk or a casual greeting with no real task, otherwise \"agent\"."

// classifyWithModel asks the configured provider's model whether a chat message
// is light small talk or real agent work. Any failure (network, provider, bad
// protocol) returns an error so the caller falls back to the heuristic.
func classifyWithModel(ctx context.Context, config ProviderConfig, secret, content string) (chatKind, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if config.Protocol == "anthropic" {
		return classifyWithAnthropic(ctx, config, secret, content)
	}
	return classifyWithOpenAICompatible(ctx, config, secret, content)
}

func classifyWithOpenAICompatible(ctx context.Context, config ProviderConfig, secret, content string) (chatKind, error) {
	payload, err := json.Marshal(map[string]any{
		"model":       config.DefaultModel,
		"max_tokens":  8,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": intentRouterSystem},
			{"role": "user", "content": content},
		},
	})
	if err != nil {
		return chatAgent, err
	}
	url := strings.TrimRight(config.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return chatAgent, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+secret)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return chatAgent, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return chatAgent, err
	}
	if resp.StatusCode >= 400 {
		return chatAgent, fmt.Errorf("router provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return chatAgent, err
	}
	if len(parsed.Choices) == 0 {
		return chatAgent, fmt.Errorf("router provider returned no choices")
	}
	return kindFromVerdict(parsed.Choices[0].Message.Content), nil
}

func classifyWithAnthropic(ctx context.Context, config ProviderConfig, secret, content string) (chatKind, error) {
	payload, err := json.Marshal(map[string]any{
		"model":      config.DefaultModel,
		"max_tokens": 8,
		"messages": []map[string]string{
			{"role": "user", "content": intentRouterSystem + "\n\n" + content},
		},
	})
	if err != nil {
		return chatAgent, err
	}
	url := strings.TrimRight(config.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return chatAgent, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", secret)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return chatAgent, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return chatAgent, err
	}
	if resp.StatusCode >= 400 {
		return chatAgent, fmt.Errorf("router provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return chatAgent, err
	}
	if len(parsed.Content) == 0 {
		return chatAgent, fmt.Errorf("router provider returned no content")
	}
	return kindFromVerdict(parsed.Content[0].Text), nil
}

func kindFromVerdict(text string) chatKind {
	low := strings.ToLower(text)
	if strings.Contains(low, "light") {
		return chatLight
	}
	return chatAgent
}
