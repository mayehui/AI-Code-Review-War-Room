package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
)

type Client struct {
	cfg    config.SCMConfig
	http   *http.Client
	logger *slog.Logger
}

func NewClient(cfg config.SCMConfig, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, http: http.DefaultClient, logger: logger}
}

func (c *Client) GitHubChangeRequest(ctx context.Context, payload []byte) (review.ChangeRequest, bool, error) {
	var event githubPREvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return review.ChangeRequest{}, false, err
	}
	if event.PullRequest.HTMLURL == "" {
		return review.ChangeRequest{}, false, errors.New("not a github pull_request payload")
	}
	if !isGitHubReviewAction(event.Action) {
		return review.ChangeRequest{}, false, nil
	}
	diff, err := c.fetchGitHubDiff(ctx, event.PullRequest.DiffURL)
	if err != nil {
		return review.ChangeRequest{}, false, err
	}
	return review.ChangeRequest{
		Provider:    "github",
		Repository:  event.Repository.FullName,
		Title:       event.PullRequest.Title,
		Description: event.PullRequest.Body,
		URL:         event.PullRequest.HTMLURL,
		Author:      event.PullRequest.User.Login,
		SourceRef:   event.PullRequest.Head.Ref,
		TargetRef:   event.PullRequest.Base.Ref,
		Diff:        diff,
	}, true, nil
}

func (c *Client) GitLabChangeRequest(ctx context.Context, payload []byte) (review.ChangeRequest, bool, error) {
	var event gitlabMREvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return review.ChangeRequest{}, false, err
	}
	if event.ObjectKind != "merge_request" || event.ObjectAttributes.URL == "" {
		return review.ChangeRequest{}, false, errors.New("not a gitlab merge_request payload")
	}
	if !isGitLabReviewAction(event.ObjectAttributes.Action) {
		return review.ChangeRequest{}, false, nil
	}
	diff, err := c.fetchGitLabDiff(ctx, event.Project.ID, event.ObjectAttributes.IID)
	if err != nil {
		return review.ChangeRequest{}, false, err
	}
	return review.ChangeRequest{
		Provider:    "gitlab",
		Repository:  firstNonEmpty(event.Project.PathWithNamespace, event.Project.Name),
		Title:       event.ObjectAttributes.Title,
		Description: event.ObjectAttributes.Description,
		URL:         event.ObjectAttributes.URL,
		Author:      event.User.Username,
		SourceRef:   event.ObjectAttributes.SourceBranch,
		TargetRef:   event.ObjectAttributes.TargetBranch,
		Diff:        diff,
	}, true, nil
}

func (c *Client) fetchGitHubDiff(ctx context.Context, diffURL string) (string, error) {
	if diffURL == "" {
		return "", errors.New("github diff_url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, diffURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "application/vnd.github.v3.diff")
	if token := os.Getenv(c.cfg.GitHub.TokenEnv); token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	return c.doText(req)
}

func (c *Client) fetchGitLabDiff(ctx context.Context, projectID int, iid int) (string, error) {
	if projectID == 0 || iid == 0 {
		return "", errors.New("gitlab project id and merge request iid are required")
	}
	base := strings.TrimRight(c.cfg.GitLab.BaseURL, "/")
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/changes", base, url.PathEscape(fmt.Sprintf("%d", projectID)), iid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if token := os.Getenv(c.cfg.GitLab.TokenEnv); token != "" {
		req.Header.Set("private-token", token)
	}
	var response struct {
		Changes []struct {
			Diff        string `json:"diff"`
			NewPath     string `json:"new_path"`
			OldPath     string `json:"old_path"`
			RenamedFile bool   `json:"renamed_file"`
			NewFile     bool   `json:"new_file"`
			DeletedFile bool   `json:"deleted_file"`
		} `json:"changes"`
	}
	if err := c.doJSON(req, &response); err != nil {
		return "", err
	}
	var b bytes.Buffer
	for _, change := range response.Changes {
		oldPath := firstNonEmpty(change.OldPath, change.NewPath)
		newPath := firstNonEmpty(change.NewPath, change.OldPath)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", oldPath, newPath)
		if change.NewFile {
			fmt.Fprintf(&b, "new file mode 100644\n")
		}
		if change.DeletedFile {
			fmt.Fprintf(&b, "deleted file mode 100644\n")
		}
		if change.RenamedFile {
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", oldPath, newPath)
		}
		b.WriteString(change.Diff)
		if !strings.HasSuffix(change.Diff, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (c *Client) doText(req *http.Request) (string, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s returned %d: %s", req.URL.String(), resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func (c *Client) doJSON(req *http.Request, target any) error {
	text, err := c.doText(req)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(text), target)
}

func isGitHubReviewAction(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize", "ready_for_review":
		return true
	default:
		return false
	}
}

func isGitLabReviewAction(action string) bool {
	switch action {
	case "open", "reopen", "reopened", "update", "approved", "merge":
		return true
	default:
		return action == ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type githubPREvent struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		DiffURL string `json:"diff_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

type gitlabMREvent struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Project struct {
		ID                int    `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int    `json:"iid"`
		Action       string `json:"action"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		URL          string `json:"url"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
	} `json:"object_attributes"`
}
