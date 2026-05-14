package provider

import "testing"

func TestParseModelOutputFencedJSON(t *testing.T) {
	result := ParseModelOutput("```json\n{\"summary\":\"ok\",\"findings\":[{\"severity\":\"high\",\"type\":\"bug\",\"file\":\"a.go\",\"line\":12,\"title\":\"bad\",\"evidence\":\"boom\",\"suggestion\":\"fix it\",\"confidence\":0.9}]}\n```")

	if result.Summary != "ok" {
		t.Fatalf("summary = %q", result.Summary)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings len = %d", len(result.Findings))
	}
	if result.Findings[0].File != "a.go" || result.Findings[0].Line != 12 {
		t.Fatalf("unexpected finding: %+v", result.Findings[0])
	}
}

func TestParseModelOutputFallsBackOnNonJSON(t *testing.T) {
	result := ParseModelOutput("plain review text")

	if result.Summary != "plain review text" {
		t.Fatalf("summary = %q", result.Summary)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("findings len = %d", len(result.Findings))
	}
}

func TestParseModelOutputConsensusJudgeJSON(t *testing.T) {
	result := ParseModelOutput(`{"consensus_reached":true,"consensus_summary":"agreed","open_disagreements":[],"final_findings":[{"severity":"high","type":"bug","file":"a.go","line":12,"title":"bad","evidence":"boom","suggestion":"fix it","confidence":0.9}]}`)

	if !result.ConsensusReached {
		t.Fatal("expected consensus")
	}
	if result.Summary != "agreed" || result.ConsensusSummary != "agreed" {
		t.Fatalf("summary=%q consensus_summary=%q", result.Summary, result.ConsensusSummary)
	}
	if len(result.FinalFindings) != 1 {
		t.Fatalf("final findings len = %d", len(result.FinalFindings))
	}
}
