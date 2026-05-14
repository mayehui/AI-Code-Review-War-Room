package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/scm"
)

type API struct {
	cfg    config.Config
	scm    *scm.Client
	engine *review.Engine
	logger *slog.Logger
}

func NewRouter(cfg config.Config, scmClient *scm.Client, engine *review.Engine, logger *slog.Logger) http.Handler {
	api := &API{cfg: cfg, scm: scmClient, engine: engine, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.healthz)
	mux.HandleFunc("POST /webhooks/github", api.githubWebhook)
	mux.HandleFunc("POST /webhooks/gitlab", api.gitlabWebhook)
	mux.HandleFunc("POST /reviews/manual", api.manualReview)
	mux.HandleFunc("GET /jobs/{id}", api.getJob)
	return requestLog(mux, logger)
}

func (a *API) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := a.verifyGitHub(r, body); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	req, ok, err := a.scm.GitHubChangeRequest(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	a.submit(w, r.Context(), req)
}

func (a *API) gitlabWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := a.verifyGitLab(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	req, ok, err := a.scm.GitLabChangeRequest(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	a.submit(w, r.Context(), req)
}

func (a *API) manualReview(w http.ResponseWriter, r *http.Request) {
	var req review.ChangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 20<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Provider == "" {
		req.Provider = "manual"
	}
	if r.URL.Query().Get("sync") == "true" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		report, err := a.engine.RunSync(ctx, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
		return
	}
	a.submit(w, r.Context(), req)
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	report, ok := a.engine.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (a *API) submit(w http.ResponseWriter, ctx context.Context, req review.ChangeRequest) {
	jobID, err := a.engine.Submit(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "queued"})
}

func (a *API) verifyGitHub(r *http.Request, body []byte) error {
	secret := os.Getenv(a.cfg.Webhook.SecretEnv)
	if secret == "" {
		return nil
	}
	signature := r.Header.Get("X-Hub-Signature-256")
	if signature == "" {
		return errors.New("missing X-Hub-Signature-256")
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return errors.New("invalid signature prefix")
	}
	expectedMAC := hmac.New(sha256.New, []byte(secret))
	expectedMAC.Write(body)
	expected := expectedMAC.Sum(nil)
	actual, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	if !hmac.Equal(expected, actual) {
		return errors.New("invalid github signature")
	}
	return nil
}

func (a *API) verifyGitLab(r *http.Request) error {
	secret := os.Getenv(a.cfg.Webhook.SecretEnv)
	if secret == "" {
		return nil
	}
	if r.Header.Get("X-Gitlab-Token") != secret {
		return errors.New("invalid gitlab token")
	}
	return nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(http.MaxBytesReader(w, r.Body, 20<<20))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func requestLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
