package review

import (
	"context"
	"strings"
	"time"
)

type ChangeRequest struct {
	Provider    string `json:"provider"`
	Repository  string `json:"repository"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Author      string `json:"author"`
	SourceRef   string `json:"source_ref"`
	TargetRef   string `json:"target_ref"`
	State       string `json:"state,omitempty"`
	Diff        string `json:"diff"`
}

func (r ChangeRequest) sessionKey() string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(r.Provider)),
		strings.ToLower(strings.TrimSpace(r.Repository)),
		strings.TrimSpace(r.URL),
	}
	if parts[2] == "" {
		return ""
	}
	return strings.Join(parts, "\x00")
}

type PromptInput struct {
	Request        ChangeRequest
	Round          int
	PeerReviews    []ReviewerResult
	Instructions   string
	ConsensusJudge bool
}

type Reviewer interface {
	ID() string
	Name() string
	Review(ctx context.Context, input PromptInput) (ReviewerResult, error)
}

type Publisher interface {
	Publish(ctx context.Context, event PublishEvent) error
}

type EventType string

const (
	EventJobStarted     EventType = "job_started"
	EventReviewerResult EventType = "reviewer_result"
	EventJudgeResult    EventType = "judge_result"
	EventFinalReport    EventType = "final_report"
)

type PublishEvent struct {
	Type   EventType      `json:"type"`
	Report Report         `json:"report"`
	Result ReviewerResult `json:"result,omitempty"`
}

type ReviewerResult struct {
	ReviewerID        string    `json:"reviewer_id"`
	ReviewerName      string    `json:"reviewer_name"`
	Round             int       `json:"round"`
	Summary           string    `json:"summary"`
	Findings          []Finding `json:"findings"`
	ConsensusReached  bool      `json:"consensus_reached,omitempty"`
	ConsensusSummary  string    `json:"consensus_summary,omitempty"`
	OpenDisagreements []string  `json:"open_disagreements,omitempty"`
	FinalFindings     []Finding `json:"final_findings,omitempty"`
	Raw               string    `json:"raw,omitempty"`
	Error             string    `json:"error,omitempty"`
	Duration          string    `json:"duration"`
	CreatedAt         time.Time `json:"created_at"`
}

type Finding struct {
	Severity   string   `json:"severity"`
	Type       string   `json:"type"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Title      string   `json:"title"`
	Evidence   string   `json:"evidence"`
	Suggestion string   `json:"suggestion"`
	Confidence float64  `json:"confidence"`
	Models     []string `json:"models"`
}

type Report struct {
	JobID             string           `json:"job_id"`
	Status            string           `json:"status"`
	Request           ChangeRequest    `json:"request"`
	Summary           string           `json:"summary"`
	Findings          []Finding        `json:"findings"`
	Rounds            []ReviewerResult `json:"rounds"`
	ConsensusReached  bool             `json:"consensus_reached,omitempty"`
	ConsensusSummary  string           `json:"consensus_summary,omitempty"`
	OpenDisagreements []string         `json:"open_disagreements,omitempty"`
	StartedAt         time.Time        `json:"started_at"`
	CompletedAt       time.Time        `json:"completed_at"`
}
