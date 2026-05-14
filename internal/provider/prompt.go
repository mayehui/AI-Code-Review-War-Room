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
	if input.ConsensusJudge {
		b.WriteString("Judge whether this merge/pull request review discussion has reached consensus. Return strict JSON only.\n\n")
		b.WriteString("Output schema:\n")
		b.WriteString(`{"consensus_reached":true,"consensus_summary":"short consensus status","open_disagreements":["remaining disagreement"],"final_findings":[{"severity":"critical|high|medium|low|info","type":"bug|security|performance|maintainability|test","file":"path","line":123,"title":"specific issue","evidence":"why this is a real issue","suggestion":"concrete fix","confidence":0.0}]}` + "\n\n")
		b.WriteString("Rules:\n")
		b.WriteString("- Set consensus_reached=true only when the reviewers agree on the actionable findings or agree there are none.\n")
		b.WriteString("- If consensus is not reached, keep final_findings empty and list the open disagreements.\n")
		b.WriteString("- If consensus is reached, final_findings is the final result to publish.\n\n")
	} else {
		b.WriteString("Review this merge/pull request and return strict JSON only.\n\n")
		b.WriteString("Output schema:\n")
		b.WriteString(`{"summary":"short summary","findings":[{"severity":"critical|high|medium|low|info","type":"bug|security|performance|maintainability|test","file":"path","line":123,"title":"specific issue","evidence":"why this is a real issue","suggestion":"concrete fix","confidence":0.0}]}` + "\n\n")
		b.WriteString("Rules:\n")
		b.WriteString("- Only report issues grounded in the diff or provided context.\n")
		b.WriteString("- Prefer fewer high-confidence findings over broad guesses.\n")
		b.WriteString("- Include file and line when possible. Use line 0 only when no line is available.\n")
		b.WriteString("- If there are no actionable issues, return an empty findings array.\n\n")
	}
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
		Summary           string           `json:"summary"`
		Findings          []review.Finding `json:"findings"`
		ConsensusReached  bool             `json:"consensus_reached"`
		ConsensusSummary  string           `json:"consensus_summary"`
		OpenDisagreements []string         `json:"open_disagreements"`
		FinalFindings     []review.Finding `json:"final_findings"`
	}
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return review.ReviewerResult{
			Summary:  fallbackSummary(text),
			Findings: nil,
			Raw:      text,
		}
	}
	return review.ReviewerResult{
		Summary:           firstNonEmpty(parsed.Summary, parsed.ConsensusSummary),
		Findings:          parsed.Findings,
		ConsensusReached:  parsed.ConsensusReached,
		ConsensusSummary:  parsed.ConsensusSummary,
		OpenDisagreements: parsed.OpenDisagreements,
		FinalFindings:     parsed.FinalFindings,
		Raw:               text,
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
