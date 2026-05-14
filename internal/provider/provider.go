package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
)

func BuildReviewers(configs []config.ReviewerConfig, logger *slog.Logger) ([]review.Reviewer, error) {
	var reviewers []review.Reviewer
	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		switch cfg.Type {
		case "chat_model":
			apiKey := strings.TrimSpace(os.Getenv(cfg.APIKeyEnv))
			if apiKey == "" {
				return nil, fmt.Errorf("reviewer %q needs env %s", cfg.ID, cfg.APIKeyEnv)
			}
			reviewers = append(reviewers, &chatReviewer{
				id:          cfg.ID,
				name:        cfg.Name,
				apiStyle:    cfg.APIStyle,
				baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
				model:       cfg.Model,
				apiKey:      apiKey,
				timeout:     cfg.Timeout,
				temperature: cfg.Temperature,
				client:      &http.Client{Timeout: cfg.Timeout},
				logger:      logger,
			})
		case "command":
			if len(cfg.Command) == 0 {
				return nil, fmt.Errorf("command reviewer %q needs command", cfg.ID)
			}
			reviewers = append(reviewers, &commandReviewer{
				id:      cfg.ID,
				name:    cfg.Name,
				command: cfg.Command,
				timeout: cfg.Timeout,
			})
		case "mock":
			reviewers = append(reviewers, &mockReviewer{id: cfg.ID, name: cfg.Name})
		default:
			return nil, fmt.Errorf("unsupported reviewer type %q for %q", cfg.Type, cfg.ID)
		}
	}
	if len(reviewers) == 0 {
		return nil, errors.New("no enabled reviewers")
	}
	return reviewers, nil
}

type chatReviewer struct {
	id          string
	name        string
	apiStyle    string
	baseURL     string
	model       string
	apiKey      string
	timeout     time.Duration
	temperature *float64
	client      *http.Client
	logger      *slog.Logger
}

func (r *chatReviewer) ID() string   { return r.id }
func (r *chatReviewer) Name() string { return r.name }

func (r *chatReviewer) Review(ctx context.Context, input review.PromptInput) (review.ReviewerResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	prompt := BuildPrompt(input)
	var text string
	var err error
	switch r.apiStyle {
	case "openai_chat_completions":
		text, err = r.callOpenAIChat(ctx, prompt)
	case "openai_responses":
		text, err = r.callOpenAIResponses(ctx, prompt)
	case "anthropic_messages":
		text, err = r.callAnthropicMessages(ctx, prompt)
	default:
		return review.ReviewerResult{}, fmt.Errorf("unsupported api_style %q", r.apiStyle)
	}
	if err != nil {
		return review.ReviewerResult{}, err
	}
	result := ParseModelOutput(text)
	result.ReviewerID = r.id
	result.ReviewerName = r.name
	result.Raw = text
	return result, nil
}

func (r *chatReviewer) callOpenAIChat(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"model": r.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt()},
			{"role": "user", "content": prompt},
		},
	}
	if r.temperature != nil {
		body["temperature"] = *r.temperature
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := r.postJSON(ctx, r.baseURL+"/chat/completions", body, nil, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", errors.New("chat completion returned no choices")
	}
	return response.Choices[0].Message.Content, nil
}

func (r *chatReviewer) callOpenAIResponses(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"model": r.model,
		"input": []map[string]string{
			{"role": "system", "content": systemPrompt()},
			{"role": "user", "content": prompt},
		},
	}
	var response struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := r.postJSON(ctx, r.baseURL+"/responses", body, nil, &response); err != nil {
		return "", err
	}
	if response.OutputText != "" {
		return response.OutputText, nil
	}
	var b strings.Builder
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Text != "" {
				b.WriteString(content.Text)
			}
		}
	}
	if b.Len() == 0 {
		return "", errors.New("responses api returned no text")
	}
	return b.String(), nil
}

func (r *chatReviewer) callAnthropicMessages(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"model":      r.model,
		"max_tokens": 4096,
		"system":     systemPrompt(),
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	headers := map[string]string{
		"x-api-key":         r.apiKey,
		"anthropic-version": "2023-06-01",
	}
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := r.postJSON(ctx, r.baseURL+"/messages", body, headers, &response); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, content := range response.Content {
		if content.Text != "" {
			b.WriteString(content.Text)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("anthropic messages returned no text")
	}
	return b.String(), nil
}

func (r *chatReviewer) postJSON(ctx context.Context, url string, body any, headers map[string]string, target any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if _, ok := headers["x-api-key"]; !ok {
		req.Header.Set("authorization", "Bearer "+r.apiKey)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("model api %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if err := json.Unmarshal(respBody, target); err != nil {
		return fmt.Errorf("decode model api response: %w", err)
	}
	return nil
}

type commandReviewer struct {
	id      string
	name    string
	command []string
	timeout time.Duration
}

func (r *commandReviewer) ID() string   { return r.id }
func (r *commandReviewer) Name() string { return r.name }

func (r *commandReviewer) Review(ctx context.Context, input review.PromptInput) (review.ReviewerResult, error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.command[0], r.command[1:]...)
	cmd.Stdin = strings.NewReader(BuildPrompt(input))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return review.ReviewerResult{}, errors.New(msg)
	}
	result := ParseModelOutput(stdout.String())
	result.ReviewerID = r.id
	result.ReviewerName = r.name
	result.Raw = stdout.String()
	return result, nil
}

type mockReviewer struct {
	id   string
	name string
}

func (r *mockReviewer) ID() string   { return r.id }
func (r *mockReviewer) Name() string { return r.name }

func (r *mockReviewer) Review(_ context.Context, input review.PromptInput) (review.ReviewerResult, error) {
	if input.ConsensusJudge {
		return review.ReviewerResult{
			ReviewerID:       r.id,
			ReviewerName:     r.name,
			ConsensusReached: true,
			ConsensusSummary: "Mock reviewers reached consensus.",
			FinalFindings:    nil,
		}, nil
	}
	return review.ReviewerResult{
		ReviewerID:   r.id,
		ReviewerName: r.name,
		Summary:      "Mock reviewer completed. Configure real reviewers before production use.",
		Findings:     nil,
	}, nil
}
