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
	MaxDiffChars    int      `yaml:"max_diff_chars"`
	DebateRounds    int      `yaml:"debate_rounds"`
	JudgeReviewerID string   `yaml:"judge_reviewer_id"`
	IgnorePatterns  []string `yaml:"ignore_patterns"`
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
	WebhookURL string        `yaml:"webhook_url"`
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
	}
}

func validate(cfg Config) error {
	if len(cfg.Reviewers) == 0 {
		return errors.New("at least one reviewer is required")
	}
	ids := map[string]struct{}{}
	for _, reviewer := range cfg.Reviewers {
		if reviewer.Disabled {
			continue
		}
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
	}
	return nil
}
