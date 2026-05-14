package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type EngineConfig struct {
	ServiceName    string
	MaxDiffChars   int
	DebateRounds   int
	Reviewers      []Reviewer
	JudgeID        string
	Publishers     []Publisher
	IgnorePatterns []string
}

type Engine struct {
	cfg    EngineConfig
	logger *slog.Logger

	mu   sync.RWMutex
	jobs map[string]Report
}

func NewEngine(cfg EngineConfig, logger *slog.Logger) *Engine {
	return &Engine{
		cfg:    cfg,
		logger: logger,
		jobs:   map[string]Report{},
	}
}

func (e *Engine) Submit(ctx context.Context, req ChangeRequest) (string, error) {
	if strings.TrimSpace(req.Diff) == "" {
		return "", errors.New("diff is required")
	}
	jobID := newJobID()
	started := time.Now()
	report := Report{
		JobID:     jobID,
		Status:    "queued",
		Request:   req,
		StartedAt: started,
	}
	e.store(report)
	go e.run(context.WithoutCancel(ctx), jobID, req, started)
	return jobID, nil
}

func (e *Engine) RunSync(ctx context.Context, req ChangeRequest) (Report, error) {
	if strings.TrimSpace(req.Diff) == "" {
		return Report{}, errors.New("diff is required")
	}
	jobID := newJobID()
	started := time.Now()
	e.store(Report{JobID: jobID, Status: "running", Request: req, StartedAt: started})
	return e.execute(ctx, jobID, req, started)
}

func (e *Engine) Get(jobID string) (Report, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	report, ok := e.jobs[jobID]
	return report, ok
}

func (e *Engine) run(ctx context.Context, jobID string, req ChangeRequest, started time.Time) {
	report, err := e.execute(ctx, jobID, req, started)
	if err != nil {
		e.logger.Error("review job failed", "job_id", jobID, "error", err)
		report.Status = "failed"
		report.Summary = err.Error()
		report.CompletedAt = time.Now()
		e.store(report)
	}
}

func (e *Engine) execute(ctx context.Context, jobID string, req ChangeRequest, started time.Time) (Report, error) {
	req.Diff = e.prepareDiff(req.Diff)
	report := Report{JobID: jobID, Status: "running", Request: req, StartedAt: started}
	e.store(report)

	rounds := e.runRound(ctx, req, 1, nil, "Review independently. Do not assume other reviewers are correct.")
	report.Rounds = append(report.Rounds, rounds...)
	e.store(report)

	latest := rounds
	for round := 2; round <= e.cfg.DebateRounds+1; round++ {
		instructions := "Reconsider the merge request after reading peer reviews. Confirm real issues, reject likely false positives, and add missed high-signal findings."
		next := e.runRound(ctx, req, round, latest, instructions)
		report.Rounds = append(report.Rounds, next...)
		latest = next
		e.store(report)
	}

	if judge := e.judge(); judge != nil {
		judgeResult := e.runJudge(ctx, judge, req, report.Rounds)
		report.Rounds = append(report.Rounds, judgeResult)
		e.store(report)
	}

	report.Findings = aggregateFindings(report.Rounds)
	report.Summary = buildSummary(e.cfg.ServiceName, report)
	report.Status = "completed"
	report.CompletedAt = time.Now()
	e.store(report)

	for _, pub := range e.cfg.Publishers {
		if err := pub.Publish(ctx, report); err != nil {
			e.logger.Error("publish report", "job_id", jobID, "error", err)
		}
	}
	return report, nil
}

func (e *Engine) runRound(ctx context.Context, req ChangeRequest, round int, peers []ReviewerResult, instructions string) []ReviewerResult {
	var wg sync.WaitGroup
	results := make([]ReviewerResult, len(e.cfg.Reviewers))
	for i, reviewer := range e.cfg.Reviewers {
		wg.Add(1)
		go func(i int, reviewer Reviewer) {
			defer wg.Done()
			start := time.Now()
			result, err := reviewer.Review(ctx, PromptInput{
				Request:      req,
				Round:        round,
				PeerReviews:  peers,
				Instructions: instructions,
			})
			if err != nil {
				result = ReviewerResult{
					ReviewerID:   reviewer.ID(),
					ReviewerName: reviewer.Name(),
					Round:        round,
					Error:        err.Error(),
				}
			}
			result.ReviewerID = firstNonEmpty(result.ReviewerID, reviewer.ID())
			result.ReviewerName = firstNonEmpty(result.ReviewerName, reviewer.Name())
			result.Round = round
			result.Duration = time.Since(start).String()
			result.CreatedAt = time.Now()
			results[i] = result
		}(i, reviewer)
	}
	wg.Wait()
	return results
}

func (e *Engine) judge() Reviewer {
	if e.cfg.JudgeID == "" {
		return nil
	}
	for _, reviewer := range e.cfg.Reviewers {
		if reviewer.ID() == e.cfg.JudgeID {
			return reviewer
		}
	}
	e.logger.Warn("judge reviewer not found", "judge_reviewer_id", e.cfg.JudgeID)
	return nil
}

func (e *Engine) runJudge(ctx context.Context, judge Reviewer, req ChangeRequest, previous []ReviewerResult) ReviewerResult {
	start := time.Now()
	result, err := judge.Review(ctx, PromptInput{
		Request:      req,
		Round:        99,
		PeerReviews:  previous,
		Instructions: "Act as the final judge. Merge duplicate findings, reject weak or speculative claims, keep only actionable issues, and return strict JSON using the required schema.",
	})
	if err != nil {
		result = ReviewerResult{ReviewerID: judge.ID(), ReviewerName: judge.Name(), Round: 99, Error: err.Error()}
	}
	result.ReviewerID = firstNonEmpty(result.ReviewerID, judge.ID())
	result.ReviewerName = firstNonEmpty(result.ReviewerName, judge.Name())
	result.Round = 99
	result.Duration = time.Since(start).String()
	result.CreatedAt = time.Now()
	return result
}

func (e *Engine) prepareDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	filtered := make([]string, 0, len(lines))
	skipFile := false
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			skipFile = e.shouldIgnoreDiffHeader(line)
		}
		if !skipFile {
			filtered = append(filtered, line)
		}
	}
	joined := strings.Join(filtered, "\n")
	if e.cfg.MaxDiffChars > 0 && len(joined) > e.cfg.MaxDiffChars {
		return joined[:e.cfg.MaxDiffChars] + "\n\n[diff truncated by AI Code Review War Room]"
	}
	return joined
}

func (e *Engine) shouldIgnoreDiffHeader(header string) bool {
	fields := strings.Fields(header)
	if len(fields) < 4 {
		return false
	}
	path := strings.TrimPrefix(fields[3], "b/")
	for _, pattern := range e.cfg.IgnorePatterns {
		if pattern == "" {
			continue
		}
		if strings.Contains(path, pattern) {
			return true
		}
		if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
			return true
		}
	}
	return false
}

func aggregateFindings(results []ReviewerResult) []Finding {
	byKey := map[string]Finding{}
	for _, result := range results {
		if result.Error != "" {
			continue
		}
		for _, finding := range result.Findings {
			finding.Severity = normalizeSeverity(finding.Severity)
			finding.Type = firstNonEmpty(finding.Type, "bug")
			key := strings.ToLower(fmt.Sprintf("%s:%d:%s", finding.File, finding.Line, compact(finding.Title)))
			existing, ok := byKey[key]
			if !ok {
				finding.Models = uniqueAppend(finding.Models, result.ReviewerName)
				byKey[key] = finding
				continue
			}
			existing.Models = uniqueAppend(existing.Models, result.ReviewerName)
			if finding.Confidence > existing.Confidence {
				existing.Confidence = finding.Confidence
			}
			if len(finding.Evidence) > len(existing.Evidence) {
				existing.Evidence = finding.Evidence
			}
			if len(finding.Suggestion) > len(existing.Suggestion) {
				existing.Suggestion = finding.Suggestion
			}
			byKey[key] = existing
		}
	}
	findings := make([]Finding, 0, len(byKey))
	for _, finding := range byKey {
		findings = append(findings, finding)
	}
	sort.Slice(findings, func(i, j int) bool {
		ri, rj := severityRank(findings[i].Severity), severityRank(findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if len(findings[i].Models) != len(findings[j].Models) {
			return len(findings[i].Models) > len(findings[j].Models)
		}
		return findings[i].Confidence > findings[j].Confidence
	})
	return findings
}

func buildSummary(serviceName string, report Report) string {
	totalErrors := 0
	for _, result := range report.Rounds {
		if result.Error != "" {
			totalErrors++
		}
	}
	if len(report.Findings) == 0 {
		if totalErrors > 0 {
			return fmt.Sprintf("%s completed with no confirmed findings; %d reviewer calls failed.", serviceName, totalErrors)
		}
		return fmt.Sprintf("%s completed with no confirmed findings.", serviceName)
	}
	return fmt.Sprintf("%s found %d issue(s) across %d reviewer response(s).", serviceName, len(report.Findings), len(report.Rounds)-totalErrors)
}

func normalizeSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high", "medium", "low", "info":
		return strings.ToLower(strings.TrimSpace(severity))
	case "blocker":
		return "critical"
	case "major":
		return "high"
	case "minor":
		return "low"
	default:
		return "medium"
	}
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func uniqueAppend(values []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return values
	}
	for _, value := range values {
		if value == next {
			return values
		}
	}
	return append(values, next)
}

func compact(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	parts := strings.Fields(value)
	if len(parts) > 8 {
		parts = parts[:8]
	}
	return strings.Join(parts, " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (e *Engine) store(report Report) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobs[report.JobID] = report
}

func newJobID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
