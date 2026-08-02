package calibrate

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

const shape = "sha256:model-shape-under-test"

// stream builds a sample whose true probability of being productive is a known
// function of the score, so the fit can be checked against the truth rather than
// against itself.
func stream(n int, seed int64, truth func(float64) float64) []Sample {
	rng := rand.New(rand.NewSource(seed))
	out := make([]Sample, 0, n)
	for range n {
		s := rng.Float64()
		out = append(out, Sample{Score: s, Productive: rng.Float64() < truth(s)})
	}
	return out
}

// THE calibration test. A calibration exists to make a score MEAN a probability,
// so the property to prove is that the fitted probability tracks the true one —
// not that the function returns a number.
func TestIsotonicRecoversTheTrueProbability(t *testing.T) {
	truth := func(s float64) float64 { return s * s } // convex: a raw score badly overstates risk at the low end
	m, err := Fit(stream(20000, 7, truth), Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	var worst float64
	for _, s := range []float64{0.05, 0.15, 0.3, 0.5, 0.7, 0.85, 0.95} {
		if d := math.Abs(r.P(s) - truth(s)); d > worst {
			worst = d
		}
	}
	if worst > 0.05 {
		t.Errorf("the fitted probability is off the truth by %.3f at worst; the map does not mean what it says", worst)
	}
	// The raw score is what a policy would have used without this package. It is
	// wrong by a wide margin at the low end, which is the whole reason the
	// calibration is not optional.
	if d := math.Abs(0.3 - truth(0.3)); d < 0.15 {
		t.Fatal("the test's own premise is broken: the raw score was already calibrated")
	}
}

// A calibration must never reorder. If it could, ROC and PR would move under it
// and every metric measured before a fit would be incomparable with one measured
// after.
func TestFitIsMonotone(t *testing.T) {
	for _, method := range []string{Isotonic, Platt} {
		m, err := Fit(stream(4000, 11, func(s float64) float64 { return 0.02 + 0.9*s*s*s }), method, shape)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		r, err := m.Under(shape)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		prev := -1.0
		for i := range 1001 {
			s := float64(i) / 1000
			p := r.P(s)
			if p < prev-1e-12 {
				t.Fatalf("%s: not monotone — p(%g)=%g after %g", method, s, p, prev)
			}
			if p < 0 || p > 1 {
				t.Fatalf("%s: p(%g)=%g is not a probability", method, s, p)
			}
			prev = p
		}
	}
}

// The fit is a record: an auditor re-running it must get the same map. Order of
// the input must not matter, because the caller's order is an accident of how
// rows came back from a store.
func TestFitIsDeterministicUnderReordering(t *testing.T) {
	base := stream(3000, 23, func(s float64) float64 { return s })
	a, err := Fit(base, Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := append([]Sample(nil), base...)
	rng := rand.New(rand.NewSource(99))
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	b, err := Fit(shuffled, Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("the same multiset in a different order produced two maps: %s vs %s", a.Digest[:12], b.Digest[:12])
	}
	if len(a.Knots) != len(b.Knots) {
		t.Fatalf("knot counts differ: %d vs %d", len(a.Knots), len(b.Knots))
	}
}

// THE training-serving skew gate. A calibration fitted under one feature space
// must refuse to answer under another. Silent misapplication is the failure mode
// this whole design exists to prevent, and it is invisible without this check —
// every number still looks like a probability.
func TestMapRefusesAnotherModelShape(t *testing.T) {
	m, err := Fit(stream(1000, 3, func(s float64) float64 { return s }), Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Under(shape); err != nil {
		t.Fatalf("refused its own shape: %v", err)
	}
	if _, err := m.Under("sha256:a-tenth-feature-was-added"); !errors.Is(err, ErrShape) {
		t.Fatalf("bound under a different shape (err=%v); a moved feature space would go unnoticed", err)
	}
	if _, err := m.Under(""); !errors.Is(err, ErrNoShape) {
		t.Fatalf("bound with no shape stated (err=%v)", err)
	}
	if _, err := (Map{}).Under(shape); !errors.Is(err, ErrNotFitted) {
		t.Fatalf("bound an unfitted map (err=%v)", err)
	}
	if _, err := Fit(stream(1000, 3, func(s float64) float64 { return s }), Isotonic, ""); !errors.Is(err, ErrNoShape) {
		t.Fatal("fitted a map that cannot refuse to be misapplied")
	}
}

// A fit on one class is a constant. Reporting a constant as a probability is the
// single most damaging thing this package could do, so it is refused by name.
func TestFitRefusesTheDegenerateSample(t *testing.T) {
	var oneClass []Sample
	for i := range 200 {
		oneClass = append(oneClass, Sample{Score: float64(i) / 200})
	}
	if _, err := Fit(oneClass, Isotonic, shape); !errors.Is(err, ErrOneClass) {
		t.Fatalf("fitted on one class: %v", err)
	}

	thin := []Sample{{Score: 0.1}, {Score: 0.9, Productive: true}}
	if _, err := Fit(thin, Isotonic, shape); !errors.Is(err, ErrThin) {
		t.Fatalf("fitted on two rows: %v", err)
	}

	// Enough rows, not enough of the scarce class.
	var scarce []Sample
	for i := range 100 {
		scarce = append(scarce, Sample{Score: float64(i) / 100, Productive: i > 97})
	}
	if _, err := Fit(scarce, Isotonic, shape); !errors.Is(err, ErrOneClass) {
		t.Fatalf("fitted on %d positives: %v", 2, err)
	}
}

// Platt's label smoothing is not decoration. Without it the maximum-likelihood
// fit on an imbalanced sample runs to a probability of exactly one, which is
// false and unusable in a cost calculation.
func TestPlattStaysInsideTheOpenInterval(t *testing.T) {
	// 2% base rate with near-perfect separation — the shape that makes an
	// unsmoothed fit diverge.
	var s []Sample
	for i := range 2000 {
		score := float64(i) / 2000
		s = append(s, Sample{Score: score, Productive: score > 0.98})
	}
	m, err := Fit(s, Platt, shape)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	hi, lo := r.P(1), r.P(0)
	if hi >= 1 || lo <= 0 {
		t.Fatalf("the fit reached certainty: p(0)=%g p(1)=%g", lo, hi)
	}
	if hi <= lo {
		t.Fatalf("the fit is not increasing: p(0)=%g p(1)=%g", lo, hi)
	}
}

// Reliability is what a console draws as the diagonal. An empty bin must report
// absence, not a point at zero — a point at zero on that chart claims perfect
// confidence in innocence.
func TestReliabilityReportsAbsenceNotZero(t *testing.T) {
	m, err := Fit(stream(3000, 5, func(s float64) float64 { return 0.5 * s }), Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	bins := Reliability(stream(3000, 6, func(s float64) float64 { return 0.5 * s }), r, 10)
	if len(bins) != 10 {
		t.Fatalf("%d bins, want 10", len(bins))
	}
	var empty, filled int
	for _, b := range bins {
		if b.Rows == 0 {
			empty++
			if b.Predicted != nil || b.Observed != nil {
				t.Errorf("empty bin [%g,%g) reports numbers", b.From, b.To)
			}
			continue
		}
		filled++
		if b.Predicted == nil || b.Observed == nil {
			t.Errorf("bin [%g,%g) has %d rows and no numbers", b.From, b.To, b.Rows)
		}
	}
	if filled == 0 {
		t.Fatal("no bin was filled; the test proves nothing")
	}
	// The truth caps at 0.5, so the upper bins must be empty — which is exactly
	// the case the absence rule exists for.
	if empty == 0 {
		t.Fatal("no bin was empty; the absence rule is untested")
	}
}

// A calibration should reduce the Brier score against the raw score, which is
// the whole claim being made when one is fitted.
func TestCalibrationImprovesBrier(t *testing.T) {
	truth := func(s float64) float64 { return s * s * s }
	train := stream(20000, 31, truth)
	test := stream(20000, 32, truth)

	m, err := Fit(train, Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	var fitted, raw float64
	for _, s := range test {
		y := 0.0
		if s.Productive {
			y = 1
		}
		p := r.P(s.Score)
		fitted += (p - y) * (p - y)
		raw += (s.Score - y) * (s.Score - y)
	}
	fitted /= float64(len(test))
	raw /= float64(len(test))
	if fitted >= raw {
		t.Fatalf("calibrated Brier %.4f is no better than the raw score's %.4f", fitted, raw)
	}
}

// THE TIE TEST, and the one that matters most in production. The scores this
// package is asked to calibrate are not a continuum: a decision score is
// 1 - prod(1-weight) over a FIXED set of rule weights, rounded to four places,
// so thousands of decisions land on a handful of atoms and the modal atom is
// exactly zero.
//
// Isotonic regression assigns ONE value per score, so every sample sharing a
// score is one block before the algorithm starts. Fed sample by sample instead,
// the run of unproductive-then-productive at a tied score is already
// non-decreasing, nothing merges, and each plateau ends up reported as whichever
// label sorted last — 1 at every plateau that holds a positive. The map then
// says CERTAINTY at the top of the range, which is where declines happen.
func TestIsotonicPoolsTiedScores(t *testing.T) {
	atoms := []struct {
		score        float64
		clean, fraud int
	}{
		{0.00, 900, 2},  // 0.002217
		{0.35, 190, 10}, // 0.05
		{0.50, 85, 15},  // 0.15
		{0.70, 45, 55},  // 0.55
		{0.90, 10, 40},  // 0.80
	}
	var s []Sample
	for _, a := range atoms {
		for range a.clean {
			s = append(s, Sample{Score: a.score})
		}
		for range a.fraud {
			s = append(s, Sample{Score: a.score, Productive: true})
		}
	}
	m, err := Fit(s, Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	// The observed rates are already non-decreasing, so pooling alone is the
	// whole fit and the map must reproduce them exactly. Nothing here is a
	// tolerance on statistical noise: this is arithmetic.
	for _, a := range atoms {
		want := round6(float64(a.fraud) / float64(a.clean+a.fraud))
		if got := r.P(a.score); math.Abs(got-want) > 1e-6 {
			t.Errorf("score %.2f -> %.6f, want %.6f (%d productive of %d)",
				a.score, got, want, a.fraud, a.clean+a.fraud)
		}
	}
	// One knot per distinct score, no more: a plateau is one place on the map.
	if len(m.Knots) != len(atoms) {
		t.Errorf("%d knots over %d distinct scores; ties were not pooled into blocks",
			len(m.Knots), len(atoms))
	}
}

// A tie whose samples arrive in the other order is the same evidence and must
// produce the same map. Pooling makes that true by construction; ordering the
// sort by class makes it look true without being true.
func TestTiedFitDoesNotDependOnLabelOrder(t *testing.T) {
	build := func(positiveFirst bool) []Sample {
		var s []Sample
		for _, sc := range []float64{0.1, 0.4, 0.8} {
			pos, neg := 20, 30
			if sc == 0.8 {
				pos, neg = 40, 10
			}
			if positiveFirst {
				for range pos {
					s = append(s, Sample{Score: sc, Productive: true})
				}
				for range neg {
					s = append(s, Sample{Score: sc})
				}
				continue
			}
			for range neg {
				s = append(s, Sample{Score: sc})
			}
			for range pos {
				s = append(s, Sample{Score: sc, Productive: true})
			}
		}
		return s
	}
	a, err := Fit(build(false), Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fit(build(true), Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("the same tied evidence in two orders produced two maps: %.12s vs %.12s", a.Digest, b.Digest)
	}
	ra, _ := a.Under(shape)
	if got := ra.P(0.1); math.Abs(got-0.4) > 1e-6 {
		t.Errorf("p(0.1) = %.6f, want 0.4 (20 productive of 50)", got)
	}
}

// A reader that cannot answer draws no chart. The reliability report IS the map
// applied to every row, so shipping one beside a refusal states in a picture
// exactly what the prose has just refused to state.
func TestReliabilityRefusesWhenTheMapDoes(t *testing.T) {
	m, err := Fit(stream(2000, 13, func(s float64) float64 { return s }), Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	rows := stream(2000, 14, func(s float64) float64 { return s })
	if _, err := m.Under("sha256:a-rule-was-written"); !errors.Is(err, ErrShape) {
		t.Fatalf("bound under a moved shape: %v", err)
	}
	if bins := Reliability(rows, Reader{}, 10); len(bins) != 0 {
		t.Errorf("a map that refuses to answer shipped %d reliability bins", len(bins))
	}
	good, err := m.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	if bins := Reliability(rows, good, 10); len(bins) != 10 {
		t.Fatalf("the bound reader drew %d bins, want 10", len(bins))
	}
}
