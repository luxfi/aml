package engine

import (
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

func TestSeverityMultiplier(t *testing.T) {
	tests := []struct {
		severity string
		want     float64
	}{
		{types.SeverityCritical, 2.0},
		{types.SeverityHigh, 1.5},
		{types.SeverityMedium, 1.0},
		{types.SeverityLow, 0.5},
		{"unknown", 1.0},
	}
	for _, tt := range tests {
		got := severityMultiplier(tt.severity)
		if got != tt.want {
			t.Errorf("severityMultiplier(%q) = %f, want %f", tt.severity, got, tt.want)
		}
	}
}

func TestClamp01(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{-1, 0},
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{2, 1},
	}
	for _, tt := range tests {
		got := clamp01(tt.in)
		if got != tt.want {
			t.Errorf("clamp01(%f) = %f, want %f", tt.in, got, tt.want)
		}
	}
}

func TestScore(t *testing.T) {
	hits := []types.RuleHit{
		{Rule: types.Rule{ID: "r1", Weight: 0.3, Severity: types.SeverityHigh}, Match: true},
		{Rule: types.Rule{ID: "r2", Weight: 0.2, Severity: types.SeverityMedium}, Match: true},
		{Rule: types.Rule{ID: "r3", Weight: 0.1, Severity: types.SeverityLow}, Match: false}, // not matched
	}

	score, breakdown := Score(hits)

	// r1: 0.3 * 1.5 = 0.45
	// r2: 0.2 * 1.0 = 0.20
	// r3: skipped (Match=false)
	// total = 0.65
	const eps = 1e-9
	if score < 0.65-eps || score > 0.65+eps {
		t.Errorf("Score = %f, want 0.65", score)
	}
	if v := breakdown["r1"]; v < 0.45-eps || v > 0.45+eps {
		t.Errorf("breakdown[r1] = %f, want 0.45", v)
	}
	if v := breakdown["r2"]; v < 0.2-eps || v > 0.2+eps {
		t.Errorf("breakdown[r2] = %f, want 0.2", v)
	}
	if _, ok := breakdown["r3"]; ok {
		t.Error("breakdown should not contain r3")
	}
}

func TestScoreClamps(t *testing.T) {
	hits := []types.RuleHit{
		{Rule: types.Rule{ID: "r1", Weight: 0.5, Severity: types.SeverityCritical}, Match: true},
		{Rule: types.Rule{ID: "r2", Weight: 0.5, Severity: types.SeverityCritical}, Match: true},
	}

	score, _ := Score(hits)
	// 0.5*2.0 + 0.5*2.0 = 2.0, clamped to 1.0
	if score != 1.0 {
		t.Errorf("Score = %f, want 1.0 (clamped)", score)
	}
}

func TestScoreEmpty(t *testing.T) {
	score, breakdown := Score(nil)
	if score != 0 {
		t.Errorf("Score = %f, want 0", score)
	}
	if len(breakdown) != 0 {
		t.Errorf("breakdown should be empty, got %d entries", len(breakdown))
	}
}
