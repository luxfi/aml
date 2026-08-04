package evaluate

import (
	"math/rand"
	"testing"

	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/replay"
)

// curveData builds a stream in time order whose truth is stationary, so the only
// thing that changes along the curve is how much of it the fit has seen.
func curveData(n int, seed int64) []Observation {
	rng := rand.New(rand.NewSource(seed))
	out := make([]Observation, 0, n)
	for i := range n {
		s := rng.Float64()
		d := replay.Unproductive
		if rng.Float64() < s*s {
			d = replay.Productive
		}
		out = append(out, Observation{ID: "d_" + strconvInt(i), At: at(i), Score: s, Disposition: d})
	}
	return out
}

// A learning curve with one arm cannot distinguish "more data would help" from
// "the fit has started memorising". Both arms, the split is temporal, and the
// ahead window is the SAME at every step — otherwise two points are measured on
// different data and the curve is a set of unrelated numbers.
func TestLearningHasBothArmsAndImprovesAhead(t *testing.T) {
	data := curveData(6000, 51)
	steps := Learning(data, calibrate.Isotonic, shape, 10, policy.Cost{})
	if len(steps) != 10 {
		t.Fatalf("%d steps, want 10", len(steps))
	}
	pool := len(data) - len(data)/11
	for i, s := range steps {
		if want := pool * (i + 1) / 10; s.Rows != want {
			t.Errorf("step %d saw %d rows, want %d — the split is not a growing temporal prefix", i, s.Rows, want)
		}
		if s.Held != len(data)-pool {
			t.Errorf("step %d is measured against %d held rows, want the fixed %d", i, s.Held, len(data)-pool)
		}
		if s.Ahead.Rows != s.Held {
			t.Errorf("step %d: the ahead report covers %d rows, the window is %d", i, s.Ahead.Rows, s.Held)
		}
	}

	// The whole point of the fixed window: out-of-sample calibration error is
	// comparable end to end, and it should fall as labels accrue.
	early, late := steps[0].Ahead.Brier, steps[len(steps)-1].Ahead.Brier
	if early == nil || late == nil {
		t.Fatal("no out-of-sample Brier was measured at both ends of the curve")
	}
	if *late > *early {
		t.Errorf("out-of-sample calibration error rose with more data: %.4f -> %.4f", *early, *late)
	}
}

// The split must never let a fit see the future: every held row lies strictly
// after every row any step trained on.
func TestLearningSplitIsTemporalAndDisjoint(t *testing.T) {
	data := curveData(1000, 52)
	steps := Learning(data, calibrate.Isotonic, shape, 5, policy.Cost{})
	if len(steps) == 0 {
		t.Fatal("no steps")
	}
	held := steps[0].Held
	cut := len(data) - held
	for i, s := range steps {
		if s.Held != held {
			t.Fatalf("step %d moved the held window: %d vs %d", i, s.Held, held)
		}
		if s.Rows > cut {
			t.Fatalf("step %d trained on %d rows, past the cut at %d — the fit saw the future", i, s.Rows, cut)
		}
		if !data[s.Rows-1].At.Before(data[cut].At) {
			t.Fatalf("step %d: the held window does not lie strictly after the training prefix", i)
		}
	}
}

// A step whose sample is too thin to fit must SAY so and still report the
// separation, which needs no calibration. A curve that went blank would hide the
// model improving underneath it.
func TestLearningStatesWhyAStepCouldNotFit(t *testing.T) {
	// 40 rows over 10 steps: the first steps have four rows each.
	steps := Learning(curveData(40, 53), calibrate.Isotonic, shape, 10, policy.Cost{})
	var refused int
	for _, s := range steps {
		if s.Refusal != "" {
			refused++
			if s.Train.Rows == 0 && s.Train.Judged == 0 {
				t.Errorf("a refused step reported nothing at all: %+v", s)
			}
		}
	}
	if refused == 0 {
		t.Fatal("no step refused on a sample this thin; the refusal path is untested")
	}
}

func TestLearningOverNothing(t *testing.T) {
	if got := Learning(nil, calibrate.Isotonic, shape, 10, policy.Cost{}); got != nil {
		t.Errorf("drew a curve over no history: %v", got)
	}
}
