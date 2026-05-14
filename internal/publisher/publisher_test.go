package publisher

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
)

func TestWebhookPublisherRoutesReviewerEvents(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts["a"]++
		mu.Unlock()
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts["b"]++
		mu.Unlock()
	}))
	defer serverB.Close()
	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts["final"]++
		mu.Unlock()
	}))
	defer finalServer.Close()

	publishers, err := Build([]config.PublisherConfig{
		{ID: "a", Type: "webhook", Style: "feishu", Events: []string{"reviewer_result"}, ReviewerID: "a", WebhookURL: serverA.URL},
		{ID: "b", Type: "webhook", Style: "feishu", Events: []string{"reviewer_result"}, ReviewerID: "b", WebhookURL: serverB.URL},
		{ID: "final", Type: "webhook", Style: "feishu", Events: []string{"final_report"}, WebhookURL: finalServer.URL},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	for _, publisher := range publishers {
		if err := publisher.Publish(context.Background(), review.PublishEvent{
			Type:   review.EventReviewerResult,
			Report: review.Report{JobID: "job-1"},
			Result: review.ReviewerResult{ReviewerID: "a", ReviewerName: "A", Round: 1, Summary: "done"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := publisher.Publish(context.Background(), review.PublishEvent{
			Type:   review.EventFinalReport,
			Report: review.Report{JobID: "job-1", Status: "completed", Summary: "done"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if counts["a"] != 1 || counts["b"] != 0 || counts["final"] != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestFeishuPublisherAddsOptionalSign(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	publishers, err := Build([]config.PublisherConfig{{
		ID:         "feishu",
		Type:       "webhook",
		Style:      "feishu",
		Events:     []string{"final_report"},
		WebhookURL: server.URL,
		SignSecret: "secret",
	}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := publishers[0].Publish(context.Background(), review.PublishEvent{
		Type:   review.EventFinalReport,
		Report: review.Report{JobID: "job-1", Status: "completed", Summary: "done"},
	}); err != nil {
		t.Fatal(err)
	}

	if payload["timestamp"] == "" || payload["sign"] == "" {
		t.Fatalf("missing sign fields: %+v", payload)
	}
	if payload["msg_type"] != "text" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestRenderJudgeResultTemplate(t *testing.T) {
	text := renderEventMarkdown(review.PublishEvent{
		Type:   review.EventJudgeResult,
		Report: review.Report{Request: review.ChangeRequest{Title: "MR"}},
		Result: review.ReviewerResult{
			ReviewerName:      "Judge",
			Round:             2,
			ConsensusReached:  false,
			ConsensusSummary:  "still split",
			OpenDisagreements: []string{"nil risk"},
		},
	})
	if !strings.Contains(text, "Consensus:** not reached") || !strings.Contains(text, "nil risk") {
		t.Fatalf("text = %s", text)
	}
}
