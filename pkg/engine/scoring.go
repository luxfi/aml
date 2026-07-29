package engine

import "github.com/luxfi/aml/pkg/types"

// severityMultiplier returns a weight multiplier for a severity level.
func severityMultiplier(severity string) float64 {
	switch severity {
	case types.SeverityCritical:
		return 2.0
	case types.SeverityHigh:
		return 1.5
	case types.SeverityMedium:
		return 1.0
	case types.SeverityLow:
		return 0.5
	default:
		return 1.0
	}
}

// clamp01 clamps v to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Score computes a weight-of-evidence risk score from rule hits.
// Returns a score in [0,1] and a breakdown per rule ID.
func Score(hits []types.RuleHit) (float64, map[string]float64) {
	breakdown := make(map[string]float64, len(hits))
	var total float64
	for _, h := range hits {
		if !h.Match {
			continue
		}
		s := h.Rule.Weight * severityMultiplier(h.Rule.Severity)
		breakdown[h.Rule.ID] = s
		total += s
	}
	return clamp01(total), breakdown
}

// ScorerPlugin is the interface for pluggable ML scoring.
// v1 uses weight-of-evidence only. This interface is the hook point
// for later Hanzo Zen gateway integration.
type ScorerPlugin interface {
	Score(tx types.Transaction, entity types.Entity, hits []types.RuleHit) (float64, map[string]float64, error)
}
