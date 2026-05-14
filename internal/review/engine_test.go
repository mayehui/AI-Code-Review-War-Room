package review

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type staticReviewer struct {
	id       string
	name     string
	findings []Finding
}

func (r staticReviewer) ID() string   { return r.id }
func (r staticReviewer) Name() string { return r.name }

func (r staticReviewer) Review(context.Context, PromptInput) (ReviewerResult, error) {
	return ReviewerResult{ReviewerID: r.id, ReviewerName: r.name, Summary: "done", Findings: r.findings}, nil
}

type scriptedReviewer struct {
	id     string
	name   string
	review func(PromptInput) ReviewerResult
}

func (r scriptedReviewer) ID() string   { return r.id }
func (r scriptedReviewer) Name() string { return r.name }

func (r scriptedReviewer) Review(_ context.Context, input PromptInput) (ReviewerResult, error) {
	if r.review == nil {
		return ReviewerResult{ReviewerID: r.id, ReviewerName: r.name, Summary: "done"}, nil
	}
	result := r.review(input)
	result.ReviewerID = r.id
	result.ReviewerName = r.name
	return result, nil
}

type blockingReviewer struct {
	id      string
	name    string
	started chan struct{}
	release chan struct{}
}

func (r blockingReviewer) ID() string   { return r.id }
func (r blockingReviewer) Name() string { return r.name }

func (r blockingReviewer) Review(ctx context.Context, input PromptInput) (ReviewerResult, error) {
	close(r.started)
	select {
	case <-ctx.Done():
		return ReviewerResult{}, ctx.Err()
	case <-r.release:
		return ReviewerResult{ReviewerID: r.id, ReviewerName: r.name, Summary: "done"}, nil
	}
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []PublishEvent
}

func (p *recordingPublisher) Publish(_ context.Context, event PublishEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *recordingPublisher) count(eventType EventType) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, event := range p.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func TestRunSyncAggregatesDuplicateFindings(t *testing.T) {
	engine := NewEngine(EngineConfig{
		ServiceName: "test",
		Reviewers: []Reviewer{
			staticReviewer{id: "a", name: "A", findings: []Finding{{
				Severity: "major", Type: "bug", File: "main.go", Line: 10, Title: "nil pointer", Evidence: "short", Confidence: 0.5,
			}}},
			staticReviewer{id: "b", name: "B", findings: []Finding{{
				Severity: "high", Type: "bug", File: "main.go", Line: 10, Title: "nil pointer", Evidence: "longer evidence", Suggestion: "guard nil", Confidence: 0.8,
			}}},
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report, err := engine.RunSync(context.Background(), ChangeRequest{Diff: "diff --git a/main.go b/main.go\n+bad"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" {
		t.Fatalf("status = %q", report.Status)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings len = %d", len(report.Findings))
	}
	finding := report.Findings[0]
	if finding.Severity != "high" {
		t.Fatalf("severity = %q", finding.Severity)
	}
	if finding.Confidence != 0.8 {
		t.Fatalf("confidence = %v", finding.Confidence)
	}
	if len(finding.Models) != 2 {
		t.Fatalf("models = %+v", finding.Models)
	}
}

func TestConsensusCompletesWhenJudgeAgreesFirstRound(t *testing.T) {
	pub := &recordingPublisher{}
	engine := NewEngine(EngineConfig{
		ServiceName:        "test",
		ConsensusEnabled:   true,
		MaxConsensusRounds: 3,
		JudgeID:            "judge",
		Reviewers: []Reviewer{
			staticReviewer{id: "a", name: "A", findings: []Finding{{
				Severity: "high", Type: "bug", File: "main.go", Line: 10, Title: "nil pointer", Confidence: 0.8,
			}}},
			scriptedReviewer{id: "judge", name: "Judge", review: func(input PromptInput) ReviewerResult {
				if !input.ConsensusJudge {
					t.Fatalf("judge should only run as consensus judge")
				}
				return ReviewerResult{
					ConsensusReached: true,
					ConsensusSummary: "all reviewers agree",
					FinalFindings: []Finding{{
						Severity: "critical", Type: "bug", File: "main.go", Line: 10, Title: "nil pointer", Confidence: 0.9,
					}},
				}
			}},
		},
		Publishers: []Publisher{pub},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report, err := engine.RunSync(context.Background(), ChangeRequest{Diff: "diff --git a/main.go b/main.go\n+bad"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || !report.ConsensusReached {
		t.Fatalf("status=%q consensus=%v", report.Status, report.ConsensusReached)
	}
	if len(report.Findings) != 1 || report.Findings[0].Severity != "critical" {
		t.Fatalf("findings = %+v", report.Findings)
	}
	if pub.count(EventReviewerResult) != 1 || pub.count(EventJudgeResult) != 1 || pub.count(EventFinalReport) != 1 {
		t.Fatalf("events = %+v", pub.events)
	}
}

func TestConsensusContinuesUntilJudgeAgrees(t *testing.T) {
	pub := &recordingPublisher{}
	engine := NewEngine(EngineConfig{
		ServiceName:        "test",
		ConsensusEnabled:   true,
		MaxConsensusRounds: 3,
		JudgeID:            "judge",
		Reviewers: []Reviewer{
			staticReviewer{id: "a", name: "A"},
			scriptedReviewer{id: "judge", name: "Judge", review: func(input PromptInput) ReviewerResult {
				return ReviewerResult{
					ConsensusReached: input.Round == 2,
					ConsensusSummary: "round status",
					OpenDisagreements: []string{
						"round one needs more discussion",
					},
				}
			}},
		},
		Publishers: []Publisher{pub},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report, err := engine.RunSync(context.Background(), ChangeRequest{Diff: "diff --git a/main.go b/main.go\n+bad"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || !report.ConsensusReached {
		t.Fatalf("status=%q consensus=%v", report.Status, report.ConsensusReached)
	}
	if pub.count(EventReviewerResult) != 2 || pub.count(EventJudgeResult) != 2 {
		t.Fatalf("events = %+v", pub.events)
	}
}

func TestConsensusUsesEmptyJudgeFinalFindingsAsNoIssues(t *testing.T) {
	engine := NewEngine(EngineConfig{
		ServiceName:        "test",
		ConsensusEnabled:   true,
		MaxConsensusRounds: 1,
		JudgeID:            "judge",
		Reviewers: []Reviewer{
			staticReviewer{id: "a", name: "A", findings: []Finding{{
				Severity: "high", Type: "bug", File: "main.go", Line: 10, Title: "nil pointer", Confidence: 0.8,
			}}},
			scriptedReviewer{id: "judge", name: "Judge", review: func(input PromptInput) ReviewerResult {
				return ReviewerResult{ConsensusReached: true, ConsensusSummary: "false positive"}
			}},
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report, err := engine.RunSync(context.Background(), ChangeRequest{Diff: "diff --git a/main.go b/main.go\n+bad"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestMergedChangeRequestSkipsReviewerDiscussion(t *testing.T) {
	pub := &recordingPublisher{}
	engine := NewEngine(EngineConfig{
		ServiceName:      "test",
		ConsensusEnabled: true,
		JudgeID:          "judge",
		Reviewers: []Reviewer{
			scriptedReviewer{id: "a", name: "A", review: func(input PromptInput) ReviewerResult {
				t.Fatal("reviewer should not run for merged change request")
				return ReviewerResult{}
			}},
			scriptedReviewer{id: "judge", name: "Judge", review: func(input PromptInput) ReviewerResult {
				t.Fatal("judge should not run for merged change request")
				return ReviewerResult{}
			}},
		},
		Publishers: []Publisher{pub},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report, err := engine.RunSync(context.Background(), ChangeRequest{
		State: "merged",
		Diff:  "diff --git a/main.go b/main.go\n+bad",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "merged" {
		t.Fatalf("status = %q", report.Status)
	}
	if len(report.Rounds) != 0 {
		t.Fatalf("rounds = %+v", report.Rounds)
	}
	if pub.count(EventFinalReport) != 1 || pub.count(EventReviewerResult) != 0 || pub.count(EventJudgeResult) != 0 {
		t.Fatalf("events = %+v", pub.events)
	}
}

func TestMergedEventCancelsActiveDiscussionForSameChangeRequest(t *testing.T) {
	pub := &recordingPublisher{}
	started := make(chan struct{})
	release := make(chan struct{})
	engine := NewEngine(EngineConfig{
		ServiceName:        "test",
		ConsensusEnabled:   true,
		MaxConsensusRounds: 3,
		JudgeID:            "judge",
		Reviewers: []Reviewer{
			blockingReviewer{id: "a", name: "A", started: started, release: release},
			scriptedReviewer{id: "judge", name: "Judge", review: func(input PromptInput) ReviewerResult {
				return ReviewerResult{ConsensusReached: true, ConsensusSummary: "done"}
			}},
		},
		Publishers: []Publisher{pub},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := ChangeRequest{
		Provider:   "gitlab",
		Repository: "group/repo",
		URL:        "https://gitlab.example.com/group/repo/-/merge_requests/1",
		Diff:       "diff --git a/main.go b/main.go\n+bad",
	}
	oldJobID, err := engine.Submit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	<-started

	mergedReq := req
	mergedReq.State = "merged"
	mergedJobID, err := engine.Submit(context.Background(), mergedReq)
	if err != nil {
		t.Fatal(err)
	}
	close(release)

	oldReport := waitForStatus(t, engine, oldJobID, "cancelled_due_to_merged")
	if oldReport.Request.State != "merged" {
		t.Fatalf("old state = %q", oldReport.Request.State)
	}
	mergedReport := waitForStatus(t, engine, mergedJobID, "merged")
	if mergedReport.Status != "merged" {
		t.Fatalf("merged status = %q", mergedReport.Status)
	}
	if pub.count(EventFinalReport) != 1 {
		t.Fatalf("events = %+v", pub.events)
	}
}

func TestConsensusReportsDisagreementsAfterMaxRounds(t *testing.T) {
	pub := &recordingPublisher{}
	engine := NewEngine(EngineConfig{
		ServiceName:        "test",
		ConsensusEnabled:   true,
		MaxConsensusRounds: 2,
		JudgeID:            "judge",
		Reviewers: []Reviewer{
			staticReviewer{id: "a", name: "A"},
			scriptedReviewer{id: "judge", name: "Judge", review: func(input PromptInput) ReviewerResult {
				return ReviewerResult{
					ConsensusReached:  false,
					ConsensusSummary:  "still split",
					OpenDisagreements: []string{"A and B disagree on nil risk"},
				}
			}},
		},
		Publishers: []Publisher{pub},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report, err := engine.RunSync(context.Background(), ChangeRequest{Diff: "diff --git a/main.go b/main.go\n+bad"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed_with_disagreements" || report.ConsensusReached {
		t.Fatalf("status=%q consensus=%v", report.Status, report.ConsensusReached)
	}
	if len(report.OpenDisagreements) != 1 {
		t.Fatalf("open disagreements = %+v", report.OpenDisagreements)
	}
	if pub.count(EventFinalReport) != 1 {
		t.Fatalf("events = %+v", pub.events)
	}
}

func waitForStatus(t *testing.T, engine *Engine, jobID string, status string) Report {
	t.Helper()
	for i := 0; i < 100; i++ {
		report, ok := engine.Get(jobID)
		if ok && report.Status == status {
			return report
		}
		time.Sleep(10 * time.Millisecond)
	}
	report, _ := engine.Get(jobID)
	t.Fatalf("job %s did not reach status %q: %+v", jobID, status, report)
	return Report{}
}

func TestPrepareDiffIgnoresConfiguredPathsAndTruncates(t *testing.T) {
	engine := NewEngine(EngineConfig{
		MaxDiffChars:   80,
		IgnorePatterns: []string{"go.sum", "dist/"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	diff := "diff --git a/go.sum b/go.sum\n+ignored\n" +
		"diff --git a/app.go b/app.go\n+" + "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"

	prepared := engine.prepareDiff(diff)
	if strings.Contains(prepared, "go.sum") || strings.Contains(prepared, "ignored") {
		t.Fatalf("ignored diff still present: %s", prepared)
	}
	if !strings.Contains(prepared, "diff truncated") {
		t.Fatalf("expected truncation marker: %s", prepared)
	}
}
