package review

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
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
