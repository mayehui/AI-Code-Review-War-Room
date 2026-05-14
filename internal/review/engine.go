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
	ServiceName        string
	MaxDiffChars       int
	DebateRounds       int
	ConsensusEnabled   bool
	MaxConsensusRounds int
	Reviewers          []Reviewer
	JudgeID            string
	Publishers         []Publisher
	IgnorePatterns     []string
}

type Engine struct {
	cfg    EngineConfig
	logger *slog.Logger

	mu   sync.RWMutex
	jobs map[string]Report

	activeMu sync.Mutex
	active   map[string]activeJob
}

type activeJob struct {
	jobID  string
	cancel context.CancelFunc
}

func NewEngine(cfg EngineConfig, logger *slog.Logger) *Engine {
	return &Engine{
		cfg:    cfg,
		logger: logger,
		jobs:   map[string]Report{},
		active: map[string]activeJob{},
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
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sessionKey := req.sessionKey()
	e.replaceActive(sessionKey, jobID, cancel, req)
	go e.run(runCtx, sessionKey, jobID, req, started)
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

func (e *Engine) run(ctx context.Context, sessionKey string, jobID string, req ChangeRequest, started time.Time) {
	defer e.clearActive(sessionKey, jobID)
	report, err := e.execute(ctx, jobID, req, started)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			e.logger.Info("review job cancelled", "job_id", jobID)
			return
		}
		e.logger.Error("review job failed", "job_id", jobID, "error", err)
		report.Status = "failed"
		report.Summary = err.Error()
		report.CompletedAt = time.Now()
		e.store(report)
	}
}

func (e *Engine) execute(ctx context.Context, jobID string, req ChangeRequest, started time.Time) (Report, error) {
	if isMergedState(req.State) {
		return e.executeMerged(ctx, jobID, req, started)
	}
	if e.cfg.ConsensusEnabled {
		return e.executeConsensus(ctx, jobID, req, started)
	}
	return e.executeLegacy(ctx, jobID, req, started)
}

func (e *Engine) executeMerged(ctx context.Context, jobID string, req ChangeRequest, started time.Time) (Report, error) {
	report := Report{
		JobID:       jobID,
		Status:      "merged",
		Request:     req,
		Summary:     fmt.Sprintf("%s skipped review because the change request is already merged.", e.cfg.ServiceName),
		StartedAt:   started,
		CompletedAt: time.Now(),
	}
	e.store(report)
	e.publish(ctx, PublishEvent{Type: EventFinalReport, Report: report})
	return report, nil
}

func (e *Engine) executeLegacy(ctx context.Context, jobID string, req ChangeRequest, started time.Time) (Report, error) {
	req.Diff = e.prepareDiff(req.Diff)
	report := Report{JobID: jobID, Status: "running", Request: req, StartedAt: started}
	e.store(report)

	if err := ctx.Err(); err != nil {
		return report, err
	}
	rounds := e.runRound(ctx, report, req, 1, nil, "Review independently. Do not assume other reviewers are correct.", false)
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.Rounds = append(report.Rounds, rounds...)
	e.store(report)

	latest := rounds
	for round := 2; round <= e.cfg.DebateRounds+1; round++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		instructions := "Reconsider the merge request after reading peer reviews. Confirm real issues, reject likely false positives, and add missed high-signal findings."
		next := e.runRound(ctx, report, req, round, latest, instructions, false)
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Rounds = append(report.Rounds, next...)
		latest = next
		e.store(report)
	}

	if judge := e.judge(); judge != nil {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		judgeResult := e.runJudge(ctx, judge, req, report.Rounds)
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Rounds = append(report.Rounds, judgeResult)
		e.store(report)
	}

	report.Findings = aggregateFindings(report.Rounds)
	report.Summary = buildSummary(e.cfg.ServiceName, report)
	report.Status = "completed"
	report.CompletedAt = time.Now()
	e.store(report)

	e.publish(ctx, PublishEvent{Type: EventFinalReport, Report: report})
	return report, nil
}

func (e *Engine) executeConsensus(ctx context.Context, jobID string, req ChangeRequest, started time.Time) (Report, error) {
	judge := e.judge()
	if judge == nil {
		return Report{JobID: jobID, Status: "failed", Request: req, StartedAt: started}, errors.New("consensus review requires a configured judge reviewer")
	}

	req.Diff = e.prepareDiff(req.Diff)
	report := Report{JobID: jobID, Status: "running", Request: req, StartedAt: started}
	e.store(report)
	e.publish(ctx, PublishEvent{Type: EventJobStarted, Report: report})

	maxRounds := e.cfg.MaxConsensusRounds
	if maxRounds <= 0 {
		maxRounds = 3
	}

	var latest []ReviewerResult
	var lastJudge ReviewerResult
	for round := 1; round <= maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		instructions := consensusRoundInstructions(round)
		results := e.runRound(ctx, report, req, round, latest, instructions, true)
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Rounds = append(report.Rounds, results...)
		e.store(report)

		lastJudge = e.runConsensusJudge(ctx, judge, req, report.Rounds, round)
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Rounds = append(report.Rounds, lastJudge)
		report.ConsensusSummary = lastJudge.ConsensusSummary
		report.OpenDisagreements = lastJudge.OpenDisagreements
		e.store(report)
		e.publish(ctx, PublishEvent{Type: EventJudgeResult, Report: report, Result: lastJudge})

		if lastJudge.ConsensusReached {
			report.ConsensusReached = true
			report.ConsensusSummary = firstNonEmpty(lastJudge.ConsensusSummary, lastJudge.Summary)
			report.OpenDisagreements = nil
			report.Findings = normalizeFindings(lastJudge.FinalFindings, lastJudge.ReviewerName)
			report.Summary = buildConsensusSummary(e.cfg.ServiceName, report)
			report.Status = "completed"
			report.CompletedAt = time.Now()
			e.store(report)
			e.publish(ctx, PublishEvent{Type: EventFinalReport, Report: report})
			return report, nil
		}

		latest = append([]ReviewerResult{}, results...)
		latest = append(latest, lastJudge)
	}

	report.ConsensusReached = false
	report.ConsensusSummary = firstNonEmpty(lastJudge.ConsensusSummary, lastJudge.Summary)
	report.OpenDisagreements = lastJudge.OpenDisagreements
	report.Findings = aggregateFindings(report.Rounds)
	report.Summary = buildDisagreementSummary(e.cfg.ServiceName, report, maxRounds)
	report.Status = "completed_with_disagreements"
	report.CompletedAt = time.Now()
	e.store(report)
	e.publish(ctx, PublishEvent{Type: EventFinalReport, Report: report})
	return report, nil
}

func (e *Engine) runRound(ctx context.Context, report Report, req ChangeRequest, round int, peers []ReviewerResult, instructions string, publishResults bool) []ReviewerResult {
	var wg sync.WaitGroup
	reviewers := e.roundReviewers()
	results := make([]ReviewerResult, len(reviewers))
	for i, reviewer := range reviewers {
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
			if publishResults {
				e.publish(ctx, PublishEvent{Type: EventReviewerResult, Report: report, Result: result})
			}
		}(i, reviewer)
	}
	wg.Wait()
	return results
}

func (e *Engine) roundReviewers() []Reviewer {
	if !e.cfg.ConsensusEnabled || e.cfg.JudgeID == "" {
		return e.cfg.Reviewers
	}
	reviewers := make([]Reviewer, 0, len(e.cfg.Reviewers))
	for _, reviewer := range e.cfg.Reviewers {
		if reviewer.ID() != e.cfg.JudgeID {
			reviewers = append(reviewers, reviewer)
		}
	}
	return reviewers
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

func (e *Engine) runConsensusJudge(ctx context.Context, judge Reviewer, req ChangeRequest, previous []ReviewerResult, round int) ReviewerResult {
	start := time.Now()
	result, err := judge.Review(ctx, PromptInput{
		Request:        req,
		Round:          round,
		PeerReviews:    previous,
		ConsensusJudge: true,
		Instructions:   "Act as the consensus judge. Decide whether reviewers now agree on the actionable findings. If consensus is reached, return the final findings. If not, summarize the open disagreements that the next round must resolve.",
	})
	if err != nil {
		result = ReviewerResult{ReviewerID: judge.ID(), ReviewerName: judge.Name(), Round: round, Error: err.Error()}
	}
	result.ReviewerID = firstNonEmpty(result.ReviewerID, judge.ID())
	result.ReviewerName = firstNonEmpty(result.ReviewerName, judge.Name())
	result.Round = round
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

func buildConsensusSummary(serviceName string, report Report) string {
	if len(report.Findings) == 0 {
		return fmt.Sprintf("%s reached reviewer consensus with no confirmed findings.", serviceName)
	}
	return fmt.Sprintf("%s reached reviewer consensus with %d confirmed issue(s).", serviceName, len(report.Findings))
}

func buildDisagreementSummary(serviceName string, report Report, maxRounds int) string {
	if len(report.OpenDisagreements) == 0 {
		return fmt.Sprintf("%s did not reach reviewer consensus after %d round(s).", serviceName, maxRounds)
	}
	return fmt.Sprintf("%s did not reach reviewer consensus after %d round(s); %d disagreement(s) remain.", serviceName, maxRounds, len(report.OpenDisagreements))
}

func consensusRoundInstructions(round int) string {
	if round <= 1 {
		return "Review independently. Return grounded findings only. Do not assume other reviewers are correct."
	}
	return "Read the peer reviews and judge feedback from the previous round. State what you now agree with, what you reject as weak or false positive, what disagreement remains, and return only the findings you still believe are actionable."
}

func normalizeFindings(findings []Finding, model string) []Finding {
	normalized := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		finding.Severity = normalizeSeverity(finding.Severity)
		finding.Type = firstNonEmpty(finding.Type, "bug")
		finding.Models = uniqueAppend(finding.Models, model)
		normalized = append(normalized, finding)
	}
	return normalized
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

func isMergedState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "merged", "merge":
		return true
	default:
		return false
	}
}

func (e *Engine) replaceActive(sessionKey string, jobID string, cancel context.CancelFunc, req ChangeRequest) {
	if sessionKey == "" {
		return
	}
	e.activeMu.Lock()
	previous, ok := e.active[sessionKey]
	e.active[sessionKey] = activeJob{jobID: jobID, cancel: cancel}
	e.activeMu.Unlock()
	if !ok || previous.jobID == jobID {
		return
	}
	if isMergedState(req.State) {
		e.cancelDueToMerged(previous.jobID, req)
	} else {
		e.cancelDueToSuperseded(previous.jobID, req)
	}
	previous.cancel()
}

func (e *Engine) clearActive(sessionKey string, jobID string) {
	if sessionKey == "" {
		return
	}
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	active, ok := e.active[sessionKey]
	if ok && active.jobID == jobID {
		delete(e.active, sessionKey)
	}
}

func (e *Engine) cancelDueToMerged(jobID string, req ChangeRequest) {
	report, ok := e.Get(jobID)
	if !ok {
		return
	}
	report.Status = "cancelled_due_to_merged"
	report.Request.State = firstNonEmpty(req.State, report.Request.State)
	report.Summary = fmt.Sprintf("%s stopped review because the change request was merged.", e.cfg.ServiceName)
	report.CompletedAt = time.Now()
	e.store(report)
}

func (e *Engine) cancelDueToSuperseded(jobID string, req ChangeRequest) {
	report, ok := e.Get(jobID)
	if !ok {
		return
	}
	report.Status = "cancelled"
	report.Summary = fmt.Sprintf("%s stopped review because a newer event arrived for this change request.", e.cfg.ServiceName)
	report.Request.State = firstNonEmpty(req.State, report.Request.State)
	report.CompletedAt = time.Now()
	e.store(report)
}

func (e *Engine) store(report Report) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobs[report.JobID] = report
}

func (e *Engine) publish(ctx context.Context, event PublishEvent) {
	for _, pub := range e.cfg.Publishers {
		if err := pub.Publish(ctx, event); err != nil {
			e.logger.Error("publish event", "job_id", event.Report.JobID, "event", event.Type, "error", err)
		}
	}
}

func newJobID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
