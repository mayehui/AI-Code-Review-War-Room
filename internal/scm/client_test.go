package scm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
)

func TestGitLabChangeRequestIncludesMergeState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v4/projects/123/merge_requests/7/changes") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"changes":[{"diff":"@@\n+bad\n","new_path":"main.go","old_path":"main.go"}]}`))
	}))
	defer server.Close()

	client := NewClient(config.SCMConfig{
		GitLab: config.GitLabConfig{BaseURL: server.URL},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, ok, err := client.GitLabChangeRequest(context.Background(), []byte(`{
		"object_kind": "merge_request",
		"user": {"username": "alice"},
		"project": {"id": 123, "path_with_namespace": "group/repo"},
		"object_attributes": {
			"iid": 7,
			"action": "merge",
			"title": "Fix bug",
			"url": "https://gitlab.example.com/group/repo/-/merge_requests/7",
			"state": "merged",
			"source_branch": "feature",
			"target_branch": "main"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected reviewable event")
	}
	if req.State != "merged" {
		t.Fatalf("state = %q", req.State)
	}
	if req.Diff == "" {
		t.Fatal("expected diff")
	}
}
