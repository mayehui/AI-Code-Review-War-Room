package publisher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
)

func Build(configs []config.PublisherConfig, logger *slog.Logger) ([]review.Publisher, error) {
	if len(configs) == 0 {
		return []review.Publisher{stdoutPublisher{logger: logger, events: eventSet([]string{"final_report"})}}, nil
	}
	var publishers []review.Publisher
	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		events := eventSet(cfg.Events)
		switch cfg.Type {
		case "stdout":
			publishers = append(publishers, stdoutPublisher{logger: logger, events: events, reviewerID: cfg.ReviewerID})
		case "webhook":
			if cfg.WebhookURL == "" {
				return nil, fmt.Errorf("publisher %q needs webhook_url", cfg.ID)
			}
			publishers = append(publishers, webhookPublisher{
				id:         cfg.ID,
				style:      firstNonEmpty(cfg.Style, "generic"),
				events:     events,
				reviewerID: cfg.ReviewerID,
				url:        cfg.WebhookURL,
				signSecret: cfg.SignSecret,
				client:     &http.Client{Timeout: cfg.Timeout},
			})
		default:
			return nil, fmt.Errorf("unsupported publisher type %q for %q", cfg.Type, cfg.ID)
		}
	}
	if len(publishers) == 0 {
		publishers = append(publishers, stdoutPublisher{logger: logger, events: eventSet([]string{"final_report"})})
	}
	return publishers, nil
}

type stdoutPublisher struct {
	logger     *slog.Logger
	events     map[review.EventType]struct{}
	reviewerID string
}

func (p stdoutPublisher) Publish(_ context.Context, event review.PublishEvent) error {
	if !accepts(p.events, p.reviewerID, event) {
		return nil
	}
	if event.Type != review.EventFinalReport {
		p.logger.Info("review event",
			"event", event.Type,
			"job_id", event.Report.JobID,
			"reviewer", event.Result.ReviewerName,
			"round", event.Result.Round,
			"summary", firstNonEmpty(event.Result.ConsensusSummary, event.Result.Summary),
		)
		return nil
	}
	report := event.Report
	p.logger.Info("review report", "job_id", report.JobID, "status", report.Status, "summary", report.Summary, "findings", len(report.Findings), "url", report.Request.URL)
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
	id         string
	style      string
	events     map[review.EventType]struct{}
	reviewerID string
	url        string
	signSecret string
	client     *http.Client
}

func (p webhookPublisher) Publish(ctx context.Context, event review.PublishEvent) error {
	if !accepts(p.events, p.reviewerID, event) {
		return nil
	}
	text := renderEventMarkdown(event)
	var payload any
	switch p.style {
	case "feishu":
		body := map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		}
		if p.signSecret != "" {
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			body["timestamp"] = timestamp
			body["sign"] = feishuSign(timestamp, p.signSecret)
		}
		payload = body
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

func renderEventMarkdown(event review.PublishEvent) string {
	switch event.Type {
	case review.EventJobStarted:
		return renderJobStarted(event.Report)
	case review.EventReviewerResult:
		return renderReviewerResult(event.Report, event.Result)
	case review.EventJudgeResult:
		return renderJudgeResult(event.Report, event.Result)
	default:
		return renderMarkdown(event.Report)
	}
}

func renderJobStarted(report review.Report) string {
	var b strings.Builder
	b.WriteString("## AI Code Review War Room started\n")
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
	if report.Request.Repository != "" {
		b.WriteString("**Repository:** ")
		b.WriteString(report.Request.Repository)
		b.WriteString("\n")
	}
	return b.String()
}

func renderReviewerResult(report review.Report, result review.ReviewerResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## %s Round %d\n", firstNonEmpty(result.ReviewerName, result.ReviewerID), result.Round))
	writeRequestHeader(&b, report)
	if result.Error != "" {
		b.WriteString("**Error:** ")
		b.WriteString(result.Error)
		b.WriteString("\n")
		return b.String()
	}
	if result.Summary != "" {
		b.WriteString("**View:** ")
		b.WriteString(result.Summary)
		b.WriteString("\n")
	}
	writeFindings(&b, result.Findings, 5)
	return b.String()
}

func renderJudgeResult(report review.Report, result review.ReviewerResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## %s Judge Round %d\n", firstNonEmpty(result.ReviewerName, result.ReviewerID), result.Round))
	writeRequestHeader(&b, report)
	if result.Error != "" {
		b.WriteString("**Error:** ")
		b.WriteString(result.Error)
		b.WriteString("\n")
		return b.String()
	}
	if result.ConsensusReached {
		b.WriteString("**Consensus:** reached\n")
	} else {
		b.WriteString("**Consensus:** not reached\n")
	}
	if result.ConsensusSummary != "" {
		b.WriteString("**Judge:** ")
		b.WriteString(result.ConsensusSummary)
		b.WriteString("\n")
	}
	if len(result.OpenDisagreements) > 0 {
		b.WriteString("\n**Open disagreements:**\n")
		for _, item := range result.OpenDisagreements {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	if len(result.FinalFindings) > 0 {
		b.WriteString("\n**Final findings candidate:**\n")
		writeFindings(&b, result.FinalFindings, 5)
	}
	return b.String()
}

func renderMarkdown(report review.Report) string {
	var b strings.Builder
	b.WriteString("## AI Code Review War Room\n")
	writeRequestHeader(&b, report)
	b.WriteString("**Status:** ")
	b.WriteString(report.Status)
	b.WriteString("\n")
	if report.ConsensusSummary != "" {
		b.WriteString("**Consensus:** ")
		b.WriteString(report.ConsensusSummary)
		b.WriteString("\n")
	}
	if len(report.OpenDisagreements) > 0 {
		b.WriteString("\n**Open disagreements:**\n")
		for _, item := range report.OpenDisagreements {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	b.WriteString("**Summary:** ")
	b.WriteString(report.Summary)
	b.WriteString("\n")
	writeFindings(&b, report.Findings, 10)
	return b.String()
}

func writeRequestHeader(b *strings.Builder, report review.Report) {
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
}

func writeFindings(b *strings.Builder, findings []review.Finding, max int) {
	if len(findings) == 0 {
		b.WriteString("\nNo confirmed actionable findings.\n")
		return
	}
	limit := len(findings)
	if limit > max {
		limit = max
	}
	for i, finding := range findings[:limit] {
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
	if len(findings) > limit {
		b.WriteString(fmt.Sprintf("\n...and %d more finding(s).\n", len(findings)-limit))
	}
}

func eventSet(events []string) map[review.EventType]struct{} {
	if len(events) == 0 {
		events = []string{"final_report"}
	}
	set := map[review.EventType]struct{}{}
	for _, event := range events {
		set[review.EventType(event)] = struct{}{}
	}
	return set
}

func accepts(events map[review.EventType]struct{}, reviewerID string, event review.PublishEvent) bool {
	if _, ok := events[event.Type]; !ok {
		return false
	}
	if reviewerID == "" {
		return true
	}
	return event.Result.ReviewerID == reviewerID
}

func feishuSign(timestamp string, secret string) string {
	message := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(message))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
