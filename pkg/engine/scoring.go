package engine

import (
	"math"

	"github.com/luxfi/aml/pkg/types"
)

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
//
// Not a number clamps to 1, not to 0. A score arrives here from arithmetic over
// weights, and once a Scorer computes a weight rather than reading a constant one,
// arithmetic can produce NaN. NaN compares false against every bound, so the
// obvious two-comparison clamp returns it unchanged: it would then travel as a
// transaction's risk score, fail to marshal into the response, and read as
// harmless in any test written with < or >. Sending it to the top of the range
// puts a broken score in front of a person.
func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 1
	}
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

// Scorer contributes evidence the rule library cannot express: a statistical
// judgement about whether behaviour is unusual for the entity, where a rule can
// only ask whether it matches a pattern someone has already named.
//
// The evidence is an ordinary RuleHit, so it is scored by the same
// weight-of-evidence sum, becomes the same Alert, and reaches the same review
// queue. Nothing downstream distinguishes it, which is deliberate: an alert a
// model raised is reviewed by a person on the same terms as any other, and there
// is one scoring path rather than two.
//
// ok is false whenever the Scorer has nothing to contribute — it cannot score, or
// it can and the transaction is unremarkable. The engine then evaluates on rules
// alone. A Scorer must not signal "normal" by returning a hit with no weight, and
// the engine must not treat ok=false as a clean result; a Scorer owes its own
// account of what it declined to score, because that count is the difference
// between a control that is quiet and one that is not running.
type Scorer interface {
	Assess(tx types.Transaction, entity types.Entity) (types.RuleHit, bool)
}
