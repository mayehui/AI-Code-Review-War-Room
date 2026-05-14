package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresJudgeWhenConsensusEnabled(t *testing.T) {
	path := writeConfig(t, `
review:
  consensus_enabled: true
reviewers:
  - id: a
    type: mock
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "judge_reviewer_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadDefaultsMaxConsensusRounds(t *testing.T) {
	path := writeConfig(t, `
review:
  consensus_enabled: true
  judge_reviewer_id: judge
  max_consensus_rounds: 0
reviewers:
  - id: a
    type: mock
  - id: judge
    type: mock
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.MaxConsensusRounds != 3 {
		t.Fatalf("max consensus rounds = %d", cfg.Review.MaxConsensusRounds)
	}
}

func TestLoadExampleConfigs(t *testing.T) {
	for _, path := range []string{
		"../../configs/config.example.yaml",
		"../../configs/config.mock.yaml",
	} {
		if _, err := Load(path); err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
