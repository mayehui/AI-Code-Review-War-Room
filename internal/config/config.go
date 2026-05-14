package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Service    ServiceConfig     `yaml:"service"`
	Server     ServerConfig      `yaml:"server"`
	Webhook    WebhookConfig     `yaml:"webhook"`
	SCM        SCMConfig         `yaml:"scm"`
	Review     ReviewConfig      `yaml:"review"`
	Reviewers  []ReviewerConfig  `yaml:"reviewers"`
	Publishers []PublisherConfig `yaml:"publishers"`
}

type ServiceConfig struct {
	Name string `yaml:"name"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type WebhookConfig struct {
	SecretEnv string `yaml:"secret_env"`
}

type SCMConfig struct {
	GitHub GitHubConfig `yaml:"github"`
	GitLab GitLabConfig `yaml:"gitlab"`
}

type GitHubConfig struct {
	TokenEnv string `yaml:"token_env"`
}

type GitLabConfig struct {
	BaseURL  string `yaml:"base_url"`
	TokenEnv string `yaml:"token_env"`
}

type ReviewConfig struct {
	MaxDiffChars       int      `yaml:"max_diff_chars"`
	DebateRounds       int      `yaml:"debate_rounds"`
	ConsensusEnabled   bool     `yaml:"consensus_enabled"`
	MaxConsensusRounds int      `yaml:"max_consensus_rounds"`
	JudgeReviewerID    string   `yaml:"judge_reviewer_id"`
	IgnorePatterns     []string `yaml:"ignore_patterns"`
}

type ReviewerConfig struct {
	ID          string        `yaml:"id"`
	Name        string        `yaml:"name"`
	Type        string        `yaml:"type"`
	Provider    string        `yaml:"provider"`
	APIStyle    string        `yaml:"api_style"`
	BaseURL     string        `yaml:"base_url"`
	Model       string        `yaml:"model"`
	APIKeyEnv   string        `yaml:"api_key_env"`
	Command     []string      `yaml:"command"`
	Timeout     time.Duration `yaml:"timeout"`
	Temperature *float64      `yaml:"temperature"`
	Disabled    bool          `yaml:"disabled"`
}

type PublisherConfig struct {
	ID         string        `yaml:"id"`
	Type       string        `yaml:"type"`
	Style      string        `yaml:"style"`
	Events     []string      `yaml:"events"`
	ReviewerID string        `yaml:"reviewer_id"`
	WebhookURL string        `yaml:"webhook_url"`
	SignSecret string        `yaml:"sign_secret"`
	Timeout    time.Duration `yaml:"timeout"`
	Disabled   bool          `yaml:"disabled"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Service.Name == "" {
		cfg.Service.Name = "AI Code Review War Room"
	}
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	if cfg.Review.MaxDiffChars <= 0 {
		cfg.Review.MaxDiffChars = 120000
	}
	if cfg.Review.DebateRounds < 0 {
		cfg.Review.DebateRounds = 0
	}
	if cfg.Review.MaxConsensusRounds <= 0 {
		cfg.Review.MaxConsensusRounds = 3
	}
	if cfg.SCM.GitLab.BaseURL == "" {
		cfg.SCM.GitLab.BaseURL = "https://gitlab.com"
	}
	for i := range cfg.Reviewers {
		if cfg.Reviewers[i].Name == "" {
			cfg.Reviewers[i].Name = cfg.Reviewers[i].ID
		}
		if cfg.Reviewers[i].Timeout <= 0 {
			cfg.Reviewers[i].Timeout = 90 * time.Second
		}
	}
	for i := range cfg.Publishers {
		if cfg.Publishers[i].Timeout <= 0 {
			cfg.Publishers[i].Timeout = 15 * time.Second
		}
		if len(cfg.Publishers[i].Events) == 0 {
			cfg.Publishers[i].Events = []string{"final_report"}
		}
	}
}

func validate(cfg Config) error {
	if len(cfg.Reviewers) == 0 {
		return errors.New("at least one reviewer is required")
	}
	ids := map[string]struct{}{}
	enabledReviewers := 0
	nonJudgeReviewers := 0
	for _, reviewer := range cfg.Reviewers {
		if reviewer.Disabled {
			continue
		}
		enabledReviewers++
		if reviewer.ID == "" {
			return errors.New("reviewer id is required")
		}
		if _, ok := ids[reviewer.ID]; ok {
			return fmt.Errorf("duplicate reviewer id %q", reviewer.ID)
		}
		ids[reviewer.ID] = struct{}{}
		if reviewer.Type == "" {
			return fmt.Errorf("reviewer %q type is required", reviewer.ID)
		}
		if reviewer.ID != cfg.Review.JudgeReviewerID {
			nonJudgeReviewers++
		}
	}
	if enabledReviewers == 0 {
		return errors.New("at least one enabled reviewer is required")
	}
	if cfg.Review.ConsensusEnabled {
		if cfg.Review.JudgeReviewerID == "" {
			return errors.New("review.judge_reviewer_id is required when consensus_enabled is true")
		}
		if _, ok := ids[cfg.Review.JudgeReviewerID]; !ok {
			return fmt.Errorf("judge reviewer %q is not enabled", cfg.Review.JudgeReviewerID)
		}
		if nonJudgeReviewers == 0 {
			return errors.New("at least one non-judge reviewer is required when consensus_enabled is true")
		}
	}
	for _, publisher := range cfg.Publishers {
		if publisher.Disabled {
			continue
		}
		for _, event := range publisher.Events {
			switch event {
			case "job_started", "reviewer_result", "judge_result", "final_report":
			default:
				return fmt.Errorf("publisher %q has unsupported event %q", publisher.ID, event)
			}
			if event == "reviewer_result" && publisher.Type == "webhook" && publisher.ReviewerID == "" {
				return fmt.Errorf("publisher %q needs reviewer_id for reviewer_result events", publisher.ID)
			}
		}
	}
	return nil
}
