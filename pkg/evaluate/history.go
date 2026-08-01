package evaluate

// history.go replays a candidate calibration and policy over recorded history:
// what WOULD this have decided, on the events that already happened.
//
// WHAT IS REPLAYED, AND WHAT DELIBERATELY IS NOT. The recorded SCORE is
// replayed. The model is not re-run. Two reasons, and both are the difference
// between a backtest that means something and one that does not:
//
//	The rings have moved on. A score is a statement about how unusual an event
//	was against the tenant's traffic AT THAT MOMENT. Recomputing it today against
//	today's aggregates answers a different question and calls it the same one.
//
//	The model has since learned from these very events. Re-scoring history
//	through a detector that folded that history into its own reference is the
//	purest form of a backtest knowing the future — and it produces a beautiful
//	number.
//
// So the score is a recorded fact and the candidate is a pure function of it.
// That is also what makes the replay DETERMINISTIC: no clock, no random source,
// no model state, no map iteration in anything that reaches the output. Two runs
// over the same rows return the same digest, which is what lets a replay be
// evidence rather than an anecdote.
//
// WHAT IT CANNOT TELL YOU, STATED RATHER THAN HIDDEN. A replay cannot know what
// would have happened to an event the live system blocked, because a blocked
// event has no outcome — the counterfactual is unobserved and no arithmetic
// recovers it. That is the feedback loop every scoring plane has, and the only
// honest treatment is to report how much of the judged set came from the
// below-the-line sample arm, which is the part of the stream the incumbent
// policy did NOT choose. Report.Explore is that number, and a replay whose
// evidence is entirely the incumbent's own alerts is measuring agreement with
// the incumbent.

import (
	"sort"
	"time"

	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/replay"
)

// Recorded extends an Observation with where its judgement came from, which is
// what makes the exploration share computable.
type Recorded struct {
	Observation
	// Source is how this observation came to be judged: a dispute, a case
	// outcome, an analyst review, or the below-the-line sample. Free-form here;
	// Sample is the one value this package reads.
	Source string
}

// Sample is the source name for a judgement that came from the below-the-line
// reproducible sample — the arm that is chosen by hash rather than by the
// incumbent policy, and therefore the only evidence about what the policy is
// missing.
const Sample = "sample"

// Tally is one action and how often it was reached. A slice of these rather than
// a map: a map's iteration order is randomised per process, so a report built
// from one cannot have a stable digest and cannot be compared byte-for-byte
// between two runs.
type Tally struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// Move is one observation the candidate would have decided differently.
type Move struct {
	// ID is the decision.
	ID string `json:"id"`
	// At is when it happened.
	At time.Time `json:"at,omitzero"`
	// Was is what the live system did; Would is what the candidate would do.
	Was   string `json:"was"`
	Would string `json:"would"`
	// Score is what the model said, and Probability what the candidate
	// calibration makes of it.
	Score       float64  `json:"score"`
	Probability *float64 `json:"probability,omitempty"`
	// Disposition is what turned out to be true, when anyone judged it. It is
	// what turns a list of differences into a list of improvements or of
	// regressions.
	Disposition replay.Disposition `json:"disposition,omitempty"`
}

// Report is the answer.
type Report struct {
	// Rows is how many observations were replayed, and From/To the period.
	Rows int       `json:"rows"`
	From time.Time `json:"from,omitzero"`
	To   time.Time `json:"to,omitzero"`

	// Was and Would are the action distributions, ascending by action name.
	Was   []Tally `json:"was"`
	Would []Tally `json:"would"`

	// Moved is every observation that would be decided differently, in the order
	// they happened, cut at Listed. Changed is the exact count and is never cut.
	Changed int    `json:"changed"`
	Moved   []Move `json:"moved,omitempty"`

	// Tightened and Loosened split Changed by direction against the caller's own
	// action ranking: how many the candidate would act harder on, and how many it
	// would let through that the incumbent did not.
	Tightened int `json:"tightened"`
	Loosened  int `json:"loosened"`

	// Metrics is the candidate measured against the judged subset, at the
	// probability the policy's FIRST rung sits at — the point where the candidate
	// stops doing nothing, which is the operating point a policy actually
	// implies.
	Metrics Metrics `json:"metrics"`

	// Explore is the share of the judged observations whose judgement came from
	// the below-the-line sample rather than from something the incumbent policy
	// chose to look at. Near zero means this report is measuring agreement with
	// the incumbent, and that is a property of the evidence, not of the
	// candidate.
	Explore float64 `json:"explore"`

	// Policy and Calibration are the digests of what was replayed, so a report
	// names exactly the candidate it describes.
	Policy      string `json:"policy"`
	Calibration string `json:"calibration,omitempty"`

	// Digest is the identity of this report. Two replays of one candidate over
	// one history agree on it; if they do not, the inputs moved.
	Digest string `json:"digest"`

	// Refusal names why a report is empty when it is. An empty history and a
	// candidate that changes nothing are opposite facts that render identically.
	Refusal string `json:"refusal,omitempty"`
}

// Listed bounds the Moved list. The counts are exact whatever this is; the list
// is evidence for a person, and a person reading a decision does not need the
// ten-thousandth row.
const Listed = 500

// Replay runs a candidate calibration and policy over recorded history.
//
// rank orders the caller's own action vocabulary so Tightened and Loosened can
// be counted without this package knowing what the actions mean — the same
// separation policy.Policy makes, for the same reason. A nil rank counts every
// difference as changed and neither tightened nor loosened, which is honest for
// a caller that has no ordering.
//
// The history is taken in the order given, which is the order it happened. It is
// not sorted here: sorting would silently repair a caller that handed rows in
// the wrong order, and a replay over shuffled history is a different question
// that would then be indistinguishable from this one.
func Replay(history []Recorded, cal calibrate.Map, pol policy.Policy, rank func(string) int) Report {
	r := Report{Rows: len(history), Policy: pol.Digest}
	if cal.Fitted() {
		r.Calibration = cal.Digest
	}
	if len(history) == 0 {
		r.Refusal = "no history to replay, so the result would be indistinguishable from a candidate that decides nothing"
		r.Digest = fingerprint("evaluate.replay.v1", r.Refusal)
		return r
	}
	if err := pol.Validate(); err != nil {
		r.Refusal = "the candidate policy is not a ladder: " + err.Error()
		r.Digest = fingerprint("evaluate.replay.v1", r.Refusal)
		return r
	}

	was := map[string]int{}
	would := map[string]int{}
	obs := make([]Observation, 0, len(history))
	var judged, explored int

	for _, h := range history {
		if r.From.IsZero() || h.At.Before(r.From) {
			r.From = h.At
		}
		if h.At.After(r.To) {
			r.To = h.At
		}
		obs = append(obs, h.Observation)
		if h.judged() {
			judged++
			if h.Source == Sample {
				explored++
			}
		}

		var prob *float64
		action := pol.Floor
		if p, err := cal.P(h.Score, cal.Shape); err == nil {
			v := round6(p)
			prob = &v
			action = pol.Action(p)
		}
		// An uncalibrated candidate cannot reach any rung: the ladder is over
		// probabilities and there is no probability. The floor is the honest
		// answer, and Refusal below says why every row landed on it.

		was[h.Action]++
		would[action]++
		if action == h.Action {
			continue
		}
		r.Changed++
		if rank != nil {
			switch {
			case rank(action) > rank(h.Action):
				r.Tightened++
			case rank(action) < rank(h.Action):
				r.Loosened++
			}
		}
		if len(r.Moved) < Listed {
			r.Moved = append(r.Moved, Move{
				ID: h.ID, At: h.At, Was: h.Action, Would: action,
				Score: round6(h.Score), Probability: prob, Disposition: h.Disposition,
			})
		}
	}

	if !cal.Fitted() {
		r.Refusal = "no calibration, so no probability, so no rung of the ladder is reachable and every row takes the floor"
	}
	r.Was, r.Would = tally(was), tally(would)
	if judged > 0 {
		r.Explore = round6(float64(explored) / float64(judged))
	}

	// The operating point a policy IMPLIES is its first rung: below it the policy
	// does nothing, so that is where it starts to act and the only point at which
	// its precision and recall are its own. Measured on the score, by inverting
	// the calibration through a sweep rather than analytically — the map is
	// piecewise linear and monotone, so the first score whose probability reaches
	// the rung is exactly the boundary.
	r.Metrics = Measure(obs, scoreAt(cal, pol.Rungs[0].At), pol.Cost, cal)

	r.Digest = digestOf(r)
	return r
}

// scoreAt inverts a monotone calibration: the lowest score whose probability
// reaches p. A binary search over the unit interval rather than over the knots,
// so it works identically for isotonic and for platt and there is one
// implementation instead of two.
//
// Returns a threshold above one when no score reaches p, which flags nothing —
// the correct reading of a rung the candidate can never reach.
func scoreAt(cal calibrate.Map, p float64) float64 {
	if !cal.Fitted() {
		// Without a calibration the sweep has no probability to invert; a
		// threshold above every score flags nothing, which is what a policy with
		// no reachable rung does.
		return 1.0000001
	}
	lo, hi := 0.0, 1.0
	if v, err := cal.P(hi, cal.Shape); err != nil || v < p {
		return 1.0000001
	}
	// 40 halvings resolves the unit interval to about 1e-12, far finer than the
	// six places a score is recorded at, so the answer is exact at the recorded
	// precision and the loop is bounded.
	for range 40 {
		mid := (lo + hi) / 2
		v, err := cal.P(mid, cal.Shape)
		if err != nil {
			return 1.0000001
		}
		if v >= p {
			hi = mid
		} else {
			lo = mid
		}
	}
	return round6(hi)
}

// tally renders a count map as an ascending slice. This is the ONE place a map
// becomes output in this package, and it sorts — which is the whole reason the
// digest is stable.
func tally(m map[string]int) []Tally {
	out := make([]Tally, 0, len(m))
	for k, v := range m {
		out = append(out, Tally{Action: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Action < out[j].Action })
	return out
}

// digestOf is the determinism receipt: everything the report asserts, rendered
// in a fixed order at a fixed precision.
//
// It covers the counts, the distributions, the metric values and the candidate's
// identity — and not the wall-clock time, which is not an input. The Moved list
// is folded in by id and direction so a report that lists different rows has a
// different identity even when the totals happen to agree.
func digestOf(r Report) string {
	parts := []string{
		"evaluate.replay.v1",
		strconvInt(r.Rows), strconvInt(r.Changed), strconvInt(r.Tightened), strconvInt(r.Loosened),
		r.Policy, r.Calibration, canonical(r.Explore),
	}
	for _, t := range r.Was {
		parts = append(parts, "was:"+t.Action+"="+strconvInt(t.Count))
	}
	for _, t := range r.Would {
		parts = append(parts, "would:"+t.Action+"="+strconvInt(t.Count))
	}
	for _, m := range r.Moved {
		parts = append(parts, "moved:"+m.ID+":"+m.Was+">"+m.Would)
	}
	parts = append(parts,
		strconvInt(r.Metrics.Judged), strconvInt(r.Metrics.Productive), strconvInt(r.Metrics.Unproductive),
		opt(r.Metrics.ROC), opt(r.Metrics.PR), opt(r.Metrics.Precision), opt(r.Metrics.Recall),
		opt(r.Metrics.Brier), optInt(r.Metrics.CostNano), optInt(r.Metrics.BestNano), opt(r.Metrics.BestThreshold),
	)
	return fingerprint(parts...)
}

func opt(v *float64) string {
	if v == nil {
		return "-"
	}
	return canonical(*v)
}

func optInt(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconvInt64(*v)
}
