package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
)

func systemPrompt() string {
	return "You are a senior code reviewer. Focus on correctness, security, data loss, concurrency, API compatibility, and maintainability. Avoid style-only comments unless they hide real risk."
}

func BuildPrompt(input review.PromptInput) string {
	var b strings.Builder
	b.WriteString("Review this merge/pull request and return strict JSON only.\n\n")
	b.WriteString("Output schema:\n")
	b.WriteString(`{"summary":"short summary","findings":[{"severity":"critical|high|medium|low|info","type":"bug|security|performance|maintainability|test","file":"path","line":123,"title":"specific issue","evidence":"why this is a real issue","suggestion":"concrete fix","confidence":0.0}]}` + "\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Only report issues grounded in the diff or provided context.\n")
	b.WriteString("- Prefer fewer high-confidence findings over broad guesses.\n")
	b.WriteString("- Include file and line when possible. Use line 0 only when no line is available.\n")
	b.WriteString("- If there are no actionable issues, return an empty findings array.\n\n")
	if input.Instructions != "" {
		b.WriteString("Round instructions:\n")
		b.WriteString(input.Instructions)
		b.WriteString("\n\n")
	}
	req := input.Request
	b.WriteString(fmt.Sprintf("Provider: %s\nRepository: %s\nTitle: %s\nURL: %s\nAuthor: %s\nSource: %s\nTarget: %s\n\n",
		req.Provider, req.Repository, req.Title, req.URL, req.Author, req.SourceRef, req.TargetRef))
	if req.Description != "" {
		b.WriteString("Description:\n")
		b.WriteString(req.Description)
		b.WriteString("\n\n")
	}
	if len(input.PeerReviews) > 0 {
		raw, _ := json.MarshalIndent(input.PeerReviews, "", "  ")
		b.WriteString("Peer reviews from previous round:\n")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	b.WriteString("Unified diff:\n```diff\n")
	b.WriteString(req.Diff)
	b.WriteString("\n```\n")
	return b.String()
}

func ParseModelOutput(text string) review.ReviewerResult {
	clean := extractJSON(text)
	var parsed struct {
		Summary  string           `json:"summary"`
		Findings []review.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return review.ReviewerResult{
			Summary:  fallbackSummary(text),
			Findings: nil,
			Raw:      text,
		}
	}
	return review.ReviewerResult{
		Summary:  parsed.Summary,
		Findings: parsed.Findings,
		Raw:      text,
	}
}

var fencedJSONPattern = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

func extractJSON(text string) string {
	text = strings.TrimSpace(text)
	if matches := fencedJSONPattern.FindStringSubmatch(text); len(matches) == 2 {
		return strings.TrimSpace(matches[1])
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return strings.TrimSpace(text[start : end+1])
	}
	return text
}

func fallbackSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Model returned empty output."
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 4 {
		lines = lines[:4]
	}
	return strings.Join(lines, "\n")
}
