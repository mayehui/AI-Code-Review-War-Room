package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
)

func Build(configs []config.PublisherConfig, logger *slog.Logger) ([]review.Publisher, error) {
	if len(configs) == 0 {
		return []review.Publisher{stdoutPublisher{logger: logger}}, nil
	}
	var publishers []review.Publisher
	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		switch cfg.Type {
		case "stdout":
			publishers = append(publishers, stdoutPublisher{logger: logger})
		case "webhook":
			if cfg.WebhookURL == "" {
				return nil, fmt.Errorf("publisher %q needs webhook_url", cfg.ID)
			}
			publishers = append(publishers, webhookPublisher{
				id:     cfg.ID,
				style:  firstNonEmpty(cfg.Style, "generic"),
				url:    cfg.WebhookURL,
				client: &http.Client{Timeout: cfg.Timeout},
			})
		default:
			return nil, fmt.Errorf("unsupported publisher type %q for %q", cfg.Type, cfg.ID)
		}
	}
	if len(publishers) == 0 {
		publishers = append(publishers, stdoutPublisher{logger: logger})
	}
	return publishers, nil
}

type stdoutPublisher struct {
	logger *slog.Logger
}

func (p stdoutPublisher) Publish(_ context.Context, report review.Report) error {
	p.logger.Info("review report", "job_id", report.JobID, "summary", report.Summary, "findings", len(report.Findings), "url", report.Request.URL)
	for _, finding := range report.Findings {
		p.logger.Info("review finding",
			"severity", finding.Severity,
			"file", finding.File,
			"line", finding.Line,
			"title", finding.Title,
			"models", strings.Join(finding.Models, ","),
		)
	}
	return nil
}

type webhookPublisher struct {
	id     string
	style  string
	url    string
	client *http.Client
}

func (p webhookPublisher) Publish(ctx context.Context, report review.Report) error {
	text := renderMarkdown(report)
	var payload any
	switch p.style {
	case "feishu":
		payload = map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		}
	case "wecom":
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"content": text},
		}
	case "slack":
		payload = map[string]string{"text": text}
	default:
		payload = map[string]string{"text": text}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("publisher %s returned %d: %s", p.id, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func renderMarkdown(report review.Report) string {
	var b strings.Builder
	b.WriteString("## AI Code Review War Room\n")
	if report.Request.Title != "" {
		b.WriteString("**MR:** ")
		b.WriteString(report.Request.Title)
		b.WriteString("\n")
	}
	if report.Request.URL != "" {
		b.WriteString("**URL:** ")
		b.WriteString(report.Request.URL)
		b.WriteString("\n")
	}
	b.WriteString("**Summary:** ")
	b.WriteString(report.Summary)
	b.WriteString("\n")
	if len(report.Findings) == 0 {
		b.WriteString("\nNo confirmed actionable findings.\n")
		return b.String()
	}
	limit := len(report.Findings)
	if limit > 10 {
		limit = 10
	}
	for i, finding := range report.Findings[:limit] {
		b.WriteString(fmt.Sprintf("\n%d. [%s] %s", i+1, strings.ToUpper(finding.Severity), finding.Title))
		if finding.File != "" {
			b.WriteString(fmt.Sprintf(" (`%s:%d`)", finding.File, finding.Line))
		}
		if len(finding.Models) > 0 {
			b.WriteString("\n   Models: ")
			b.WriteString(strings.Join(finding.Models, ", "))
		}
		if finding.Evidence != "" {
			b.WriteString("\n   Evidence: ")
			b.WriteString(finding.Evidence)
		}
		if finding.Suggestion != "" {
			b.WriteString("\n   Fix: ")
			b.WriteString(finding.Suggestion)
		}
		b.WriteString("\n")
	}
	if len(report.Findings) > limit {
		b.WriteString(fmt.Sprintf("\n...and %d more finding(s).\n", len(report.Findings)-limit))
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
