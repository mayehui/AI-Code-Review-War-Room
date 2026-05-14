package review

import (
	"context"
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
	Diff        string `json:"diff"`
}

type PromptInput struct {
	Request      ChangeRequest
	Round        int
	PeerReviews  []ReviewerResult
	Instructions string
}

type Reviewer interface {
	ID() string
	Name() string
	Review(ctx context.Context, input PromptInput) (ReviewerResult, error)
}

type Publisher interface {
	Publish(ctx context.Context, report Report) error
}

type ReviewerResult struct {
	ReviewerID   string    `json:"reviewer_id"`
	ReviewerName string    `json:"reviewer_name"`
	Round        int       `json:"round"`
	Summary      string    `json:"summary"`
	Findings     []Finding `json:"findings"`
	Raw          string    `json:"raw,omitempty"`
	Error        string    `json:"error,omitempty"`
	Duration     string    `json:"duration"`
	CreatedAt    time.Time `json:"created_at"`
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
	JobID       string           `json:"job_id"`
	Status      string           `json:"status"`
	Request     ChangeRequest    `json:"request"`
	Summary     string           `json:"summary"`
	Findings    []Finding        `json:"findings"`
	Rounds      []ReviewerResult `json:"rounds"`
	StartedAt   time.Time        `json:"started_at"`
	CompletedAt time.Time        `json:"completed_at"`
}
