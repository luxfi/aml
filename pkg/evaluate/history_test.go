package evaluate

import (
	"math/rand"
	"testing"

	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/replay"
)

func rank(a string) int {
	switch a {
	case "allow":
		return 1
	case "challenge":
		return 2
	case "review":
		return 3
	case "block":
		return 4
	}
	return 0
}

func candidate(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Seal(policy.Policy{
		Stage: "payment", Floor: "allow",
		Rungs: []policy.Rung{
			{At: 0.20, Action: "challenge"},
			{At: 0.60, Action: "review"},
			{At: 0.90, Action: "block"},
		},
		Cost: policy.Cost{Miss: 40_000_000_000, Alarm: 2_000_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// history builds a recorded stream whose truth is a known function of the score,
// with the live system's own action recorded beside it.
func history(n int, seed int64) ([]Recorded, calibrate.Map, error) {
	rng := rand.New(rand.NewSource(seed))
	var h []Recorded
	var samples []calibrate.Sample
	for i := range n {
		s := rng.Float64()
		productive := rng.Float64() < s*s
		d := replay.Unproductive
		if productive {
			d = replay.Productive
		}
		action := "allow"
		if s > 0.8 {
			action = "review"
		}
		source := "review"
		if i%7 == 0 {
			source = Sample
		}
		h = append(h, Recorded{
			Observation: Observation{
				ID: "d_" + strconvInt(i), At: at(i), Score: s, Action: action, Disposition: d,
			},
			Source: source,
		})
		samples = append(samples, calibrate.Sample{Score: s, Productive: productive})
	}
	cal, err := calibrate.Fit(samples, calibrate.Isotonic, shape)
	return h, cal, err
}

// THE backtest determinism test. A replay is evidence only if re-running it
// reproduces it exactly — including under Go's randomised map iteration, which
// is why nothing that reaches the output may be a map.
func TestReplayIsDeterministic(t *testing.T) {
	h, cal, err := history(2000, 41)
	if err != nil {
		t.Fatal(err)
	}
	pol := candidate(t)

	first := Replay(h, cal, pol, rank)
	if first.Refusal != "" {
		t.Fatalf("refused: %s", first.Refusal)
	}
	if first.Digest == "" {
		t.Fatal("a report with no identity cannot be evidence")
	}
	for i := range 25 {
		again := Replay(h, cal, pol, rank)
		if again.Digest != first.Digest {
			t.Fatalf("run %d produced a different report: %s vs %s", i, again.Digest[:12], first.Digest[:12])
		}
		if len(again.Was) != len(first.Was) || len(again.Would) != len(first.Would) {
			t.Fatalf("run %d produced different distributions", i)
		}
		for j := range again.Was {
			if again.Was[j] != first.Was[j] || again.Would[j] != first.Would[j] {
				t.Fatalf("run %d moved a tally: %+v vs %+v", i, again.Was[j], first.Was[j])
			}
		}
	}
}

// The digest must actually discriminate, or determinism is trivially satisfied
// by a constant.
func TestReplayDigestMovesWithTheCandidate(t *testing.T) {
	h, cal, err := history(1500, 42)
	if err != nil {
		t.Fatal(err)
	}
	base := Replay(h, cal, candidate(t), rank)

	tighter := candidate(t)
	tighter.Rungs[0].At = 0.10
	tighter, err = policy.Seal(tighter)
	if err != nil {
		t.Fatal(err)
	}
	moved := Replay(h, cal, tighter, rank)
	if moved.Digest == base.Digest {
		t.Fatal("moving the first rung left the report identical")
	}
	if moved.Would[0].Count == base.Would[0].Count && moved.Changed == base.Changed {
		t.Fatal("a lower first rung changed nothing")
	}
	// A lower first rung can only act harder.
	if moved.Tightened < base.Tightened {
		t.Errorf("lowering the challenge rung reduced the tightened count: %d -> %d", base.Tightened, moved.Tightened)
	}
}

// A tally is ordered, not a map. This is the property the determinism rests on
// and it is worth asserting directly.
func TestTalliesAreOrdered(t *testing.T) {
	h, cal, err := history(500, 43)
	if err != nil {
		t.Fatal(err)
	}
	r := Replay(h, cal, candidate(t), rank)
	for _, set := range [][]Tally{r.Was, r.Would} {
		for i := 1; i < len(set); i++ {
			if set[i-1].Action >= set[i].Action {
				t.Fatalf("tallies are not ascending: %+v", set)
			}
		}
	}
}

// An empty history and a candidate that changes nothing render identically. The
// difference is the whole reason a sandbox exists, so it must be stated.
func TestReplayRefusesAnEmptyHistory(t *testing.T) {
	r := Replay(nil, calibrate.Map{}, candidate(t), rank)
	if r.Refusal == "" {
		t.Fatal("an empty replay reported as if it had run")
	}
	if r.Changed != 0 || r.Rows != 0 {
		t.Fatalf("counted something over nothing: %+v", r)
	}
}

// A candidate with no calibration cannot reach any rung, because the ladder is
// over probabilities. Every row taking the floor is the correct answer and the
// report must say why rather than presenting it as a policy that allows
// everything.
func TestReplayWithoutCalibrationSaysWhyNothingActs(t *testing.T) {
	h, _, err := history(400, 44)
	if err != nil {
		t.Fatal(err)
	}
	r := Replay(h, calibrate.Map{}, candidate(t), rank)
	if r.Refusal == "" {
		t.Fatal("every row took the floor and the report did not say why")
	}
	if len(r.Would) != 1 || r.Would[0].Action != "allow" {
		t.Fatalf("something acted with no probability to act on: %+v", r.Would)
	}
}

// A ladder that does not validate must be refused rather than replayed, or the
// report describes a policy that could never be put in force.
func TestReplayRefusesABrokenLadder(t *testing.T) {
	h, cal, err := history(400, 45)
	if err != nil {
		t.Fatal(err)
	}
	broken := candidate(t)
	broken.Rungs[1].At = 0.05 // descending
	r := Replay(h, cal, broken, rank)
	if r.Refusal == "" {
		t.Fatal("replayed a ladder that cannot be put in force")
	}
	if r.Changed != 0 {
		t.Fatalf("counted changes under a refused ladder: %d", r.Changed)
	}
}

// The exploration share is the honest limit on every replay: evidence that came
// only from what the incumbent chose to look at measures agreement with the
// incumbent.
func TestExploreIsMeasuredNotAssumed(t *testing.T) {
	h, cal, err := history(700, 46) // every seventh judgement comes from the sample arm
	if err != nil {
		t.Fatal(err)
	}
	r := Replay(h, cal, candidate(t), rank)
	if r.Explore <= 0.1 || r.Explore >= 0.2 {
		t.Errorf("explore share is %g, want about 1/7", r.Explore)
	}

	for i := range h {
		h[i].Source = "review"
	}
	r = Replay(h, cal, candidate(t), rank)
	if r.Explore != 0 {
		t.Errorf("explore share is %g with no sample arm at all", r.Explore)
	}
}

// The Moved list is evidence for a person and is cut; the count is exact and
// never is.
func TestMovedIsCutAndChangedIsNot(t *testing.T) {
	h, cal, err := history(4000, 47)
	if err != nil {
		t.Fatal(err)
	}
	all := candidate(t)
	all.Rungs = []policy.Rung{{At: 0, Action: "block"}}
	all, err = policy.Seal(all)
	if err != nil {
		t.Fatal(err)
	}
	r := Replay(h, cal, all, rank)
	if r.Changed <= Listed {
		t.Fatalf("only %d changed; the cut is untested", r.Changed)
	}
	if len(r.Moved) != Listed {
		t.Fatalf("listed %d rows, want the bound of %d", len(r.Moved), Listed)
	}
}
