package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mayehui/AI-Code-Review-War-Room/internal/config"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/httpapi"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/provider"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/publisher"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/review"
	"github.com/mayehui/AI-Code-Review-War-Room/internal/scm"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", envOrDefault("WARROOM_CONFIG", "configs/config.example.yaml"), "path to config yaml")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	reviewers, err := provider.BuildReviewers(cfg.Reviewers, logger)
	if err != nil {
		logger.Error("build reviewers", "error", err)
		os.Exit(1)
	}
	publishers, err := publisher.Build(cfg.Publishers, logger)
	if err != nil {
		logger.Error("build publishers", "error", err)
		os.Exit(1)
	}

	engine := review.NewEngine(review.EngineConfig{
		ServiceName:    cfg.Service.Name,
		MaxDiffChars:   cfg.Review.MaxDiffChars,
		DebateRounds:   cfg.Review.DebateRounds,
		Reviewers:      reviewers,
		JudgeID:        cfg.Review.JudgeReviewerID,
		Publishers:     publishers,
		IgnorePatterns: cfg.Review.IgnorePatterns,
	}, logger)

	server := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           httpapi.NewRouter(cfg, scm.NewClient(cfg.SCM, logger), engine, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("war room server listening", "addr", cfg.Server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
