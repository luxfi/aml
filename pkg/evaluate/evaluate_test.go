package evaluate

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/replay"
)

const shape = "sha256:model-shape-under-test"

func obs(score float64, productive bool) Observation {
	d := replay.Unproductive
	if productive {
		d = replay.Productive
	}
	return Observation{Score: score, Disposition: d}
}

// A perfect ranker scores 1.0 and a coin scores 0.5. Both ends pinned, because
// an AUC implementation that is wrong is wrong in a way that still looks like a
// number between zero and one.
func TestROCAtBothEnds(t *testing.T) {
	perfect := []Observation{obs(0.1, false), obs(0.2, false), obs(0.8, true), obs(0.9, true)}
	if got := roc(perfect); got != 1 {
		t.Errorf("a perfect ranker scored %g", got)
	}
	inverted := []Observation{obs(0.1, true), obs(0.2, true), obs(0.8, false), obs(0.9, false)}
	if got := roc(inverted); got != 0 {
		t.Errorf("an exactly wrong ranker scored %g", got)
	}
	// Every score identical — which is exactly what a warming detector produces.
	// Midranks make this 0.5; counting ties as wins would make it 1.0 and report
	// a model that says nothing as flawless.
	flat := []Observation{obs(0.5, true), obs(0.5, false), obs(0.5, true), obs(0.5, false)}
	if got := roc(flat); got != 0.5 {
		t.Errorf("a model with no opinion scored %g, want 0.5 — ties are not wins", got)
	}
}

// Average precision must use the step definition. The trapezoid credits a
// classifier with operating points between two it actually has, which flatters
// exactly the rare-event case the metric exists for.
func TestAveragePrecisionIsTheStepArea(t *testing.T) {
	// Ranked: + - + -. Precision at the two recall steps is 1/1 and 2/3.
	// Step AP = 0.5*1 + 0.5*(2/3) = 0.8333...
	got := averagePrecision([]Observation{
		obs(0.9, true), obs(0.8, false), obs(0.7, true), obs(0.6, false),
	})
	want := 0.5*1 + 0.5*(2.0/3.0)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("AP = %.6f, want %.6f", got, want)
	}

	// A ranker with no signal converges on the prevalence.
	rng := rand.New(rand.NewSource(4))
	var noise []Observation
	for range 20000 {
		noise = append(noise, obs(rng.Float64(), rng.Float64() < 0.05))
	}
	if ap := averagePrecision(noise); math.Abs(ap-0.05) > 0.02 {
		t.Errorf("a signal-free ranker scored AP %.4f at a 5%% base rate, want ~0.05", ap)
	}
}

// Ties must not let the caller's ordering change the answer.
func TestMetricsAreOrderInvariant(t *testing.T) {
	base := []Observation{
		obs(0.5, true), obs(0.5, false), obs(0.5, false), obs(0.5, true),
		obs(0.9, true), obs(0.1, false), obs(0.9, false),
	}
	a, b := roc(base), averagePrecision(base)
	rng := rand.New(rand.NewSource(17))
	for range 50 {
		s := append([]Observation(nil), base...)
		rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
		if roc(s) != a || averagePrecision(s) != b {
			t.Fatalf("a reshuffle moved the metrics: roc %g->%g ap %g->%g", a, roc(s), b, averagePrecision(s))
		}
	}
}

// Absence is not zero. On a plane where most events are unjudged this is the
// default state, and a rate reported as 0.0 reads as a perfect model.
func TestUnjudgedReportsAbsenceNotZero(t *testing.T) {
	m := Measure([]Observation{{Score: 0.9}, {Score: 0.1}}, 0.5, policy.Cost{}, calibrate.Map{})
	if m.Rows != 2 || m.Judged != 0 {
		t.Fatalf("counted %d rows / %d judged", m.Rows, m.Judged)
	}
	if m.ROC != nil || m.PR != nil || m.Precision != nil || m.Recall != nil || m.Prevalence != nil {
		t.Errorf("reported rates over nothing: %+v", m)
	}
	if m.Refusal == "" {
		t.Error("an unmeasurable report did not say so")
	}

	// One class only: prevalence is real, separation is not.
	one := []Observation{obs(0.9, true), obs(0.8, true), obs(0.7, true)}
	m = Measure(one, 0.5, policy.Cost{}, calibrate.Map{})
	if m.Prevalence == nil || *m.Prevalence != 1 {
		t.Errorf("prevalence over one class is %v, want 1", m.Prevalence)
	}
	if m.ROC != nil || m.PR != nil {
		t.Error("separation was reported over a single class")
	}
}

// THE cost-weighted test. A miss and a false alarm have different prices, and
// the cost-optimal threshold moves with the ratio — which is the whole reason
// the metric exists and the reason it must be exact.
func TestCostWeightedOptimumMovesWithThePrices(t *testing.T) {
	// 100 unproductive spread over [0,0.6), 10 productive over [0.4,1.0).
	var o []Observation
	for i := range 100 {
		o = append(o, obs(0.6*float64(i)/100, false))
	}
	for i := range 10 {
		o = append(o, obs(0.4+0.6*float64(i)/10, true))
	}

	// A miss priced far above an alarm should push the threshold DOWN — flag
	// more, catch more.
	cheap := Measure(o, 0.5, policy.Cost{Miss: 1_000_000_000_000, Alarm: 1_000_000}, calibrate.Map{})
	// An alarm priced above a miss should push it UP — flag almost nothing.
	dear := Measure(o, 0.5, policy.Cost{Miss: 1_000_000, Alarm: 1_000_000_000_000}, calibrate.Map{})

	if cheap.BestThreshold == nil || dear.BestThreshold == nil {
		t.Fatal("no cost-optimal threshold was found under stated prices")
	}
	if *cheap.BestThreshold >= *dear.BestThreshold {
		t.Errorf("cheap-alarm optimum %g is not below dear-alarm optimum %g",
			*cheap.BestThreshold, *dear.BestThreshold)
	}
	if cheap.BestNano == nil || *cheap.BestNano > *cheap.CostNano {
		t.Errorf("the reported optimum (%v) is worse than the operating point (%v)", cheap.BestNano, cheap.CostNano)
	}
	// Exactness: the cost at the operating point is counts times prices, in
	// integers, with nothing rounded away.
	c := cheap.Confusion
	want := int64(math.Round(c.FN))*1_000_000_000_000 + int64(math.Round(c.FP))*1_000_000
	if *cheap.CostNano != want {
		t.Errorf("cost is %d, want %d — the money arithmetic is not exact", *cheap.CostNano, want)
	}
}

// An unstated price must produce no cost at all, not a cost of zero.
func TestNoStatedPriceMeansNoCost(t *testing.T) {
	m := Measure([]Observation{obs(0.9, true), obs(0.1, false)}, 0.5, policy.Cost{}, calibrate.Map{})
	if m.CostNano != nil || m.BestNano != nil || m.BestThreshold != nil {
		t.Errorf("priced a mistake nobody put a price on: %+v", m)
	}
}

// A monotone calibration cannot reorder, so it cannot move a rank statistic.
// This is worth pinning because it is the property that makes ROC and PR
// comparable across calibration versions — and the property a reader most often
// assumes the other way round.
func TestCalibrationMovesBrierAndNotSeparation(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	var o []Observation
	var samples []calibrate.Sample
	for range 4000 {
		s := rng.Float64()
		p := s * s
		productive := rng.Float64() < p
		o = append(o, obs(s, productive))
		samples = append(samples, calibrate.Sample{Score: s, Productive: productive})
	}
	cal, err := calibrate.Fit(samples, calibrate.Isotonic, shape)
	if err != nil {
		t.Fatal(err)
	}
	raw := Measure(o, 0.5, policy.Cost{}, calibrate.Map{})
	fitted := Measure(o, 0.5, policy.Cost{}, cal)

	if *raw.ROC != *fitted.ROC || *raw.PR != *fitted.PR {
		t.Errorf("a monotone calibration moved a rank statistic: roc %g->%g pr %g->%g",
			*raw.ROC, *fitted.ROC, *raw.PR, *fitted.PR)
	}
	if raw.Brier != nil {
		t.Error("a Brier score was reported with no calibration to score")
	}
	if fitted.Brier == nil {
		t.Fatal("no Brier score was reported with a calibration in hand")
	}
}

// The curve is one point per REACHABLE operating point. A point between two
// observations at one score describes a threshold that separates nothing.
func TestCurveHasOnePointPerDistinctScore(t *testing.T) {
	o := []Observation{obs(0.9, true), obs(0.9, false), obs(0.5, true), obs(0.1, false)}
	c := Curve(o, policy.Cost{Miss: 1, Alarm: 1})
	if len(c) != 3 {
		t.Fatalf("%d points over 3 distinct scores", len(c))
	}
	for i := 1; i < len(c); i++ {
		if c[i].Threshold >= c[i-1].Threshold {
			t.Errorf("the curve is not descending: %v", c)
		}
		if c[i].TruePositive < c[i-1].TruePositive || c[i].FalsePositive < c[i-1].FalsePositive {
			t.Errorf("recall or fall-out went backwards as the threshold fell: %v", c)
		}
	}
	last := c[len(c)-1]
	if last.TruePositive != 1 || last.FalsePositive != 1 {
		t.Errorf("the lowest threshold does not flag everything: %+v", last)
	}
}

func TestCurveIsEmptyWithoutBothClasses(t *testing.T) {
	if got := Curve([]Observation{obs(0.9, true), obs(0.8, true)}, policy.Cost{}); got != nil {
		t.Errorf("drew a curve over one class: %v", got)
	}
	if got := Curve(nil, policy.Cost{}); got != nil {
		t.Errorf("drew a curve over nothing: %v", got)
	}
}

// A price that cannot be multiplied by the count without wrapping produces NO
// cost and says why. The wrapped number would be negative, would be the minimum
// of the sweep, and would therefore be returned as the threshold to adopt — an
// arithmetic failure wearing the shape of a recommendation.
func TestAnUnrepresentablePriceIsAbsentAndNotWrapped(t *testing.T) {
	var obsv []Observation
	for i := range 200 {
		obsv = append(obsv, obs(float64(i)/200, i%3 == 0))
	}
	huge := policy.Cost{Miss: math.MaxInt64 / 4, Alarm: math.MaxInt64 / 4}
	m := Measure(obsv, 0.5, huge, calibrate.Map{})
	if m.CostNano != nil {
		t.Errorf("a cost of %d was reported from a product that does not fit in an int64", *m.CostNano)
	}
	if m.BestNano != nil || m.BestThreshold != nil {
		t.Error("an operating point was recommended from arithmetic that overflowed")
	}
	if m.Refusal == "" {
		t.Error("the cost was dropped in silence, which reads as an unpriced policy")
	}
	// The rank statistics are unaffected: they never touch the prices.
	if m.ROC == nil || m.PR == nil {
		t.Error("separation went missing because a price did not fit")
	}
	// And nowhere on the curve is a wrapped number plotted.
	for i, p := range Curve(obsv, huge) {
		if p.CostNano != 0 {
			t.Fatalf("curve point %d carries %d, a price the arithmetic could not represent", i, p.CostNano)
		}
	}
}

// A curve is a chart and the response carrying it is read by a browser. The
// metrics behind it stay exact over every distinct score; what is RETURNED is
// bounded, keeps both ends, and stays descending — a truncated curve would
// describe a model that stops.
func TestTheCurveIsBoundedAndKeepsItsEnds(t *testing.T) {
	n := Plotted * 7
	var obsv []Observation
	for i := range n {
		obsv = append(obsv, obs(float64(i)/float64(n), i%5 == 0))
	}
	full := Curve(obsv, policy.Cost{})
	if len(full) > Plotted {
		t.Fatalf("the curve returned %d points, above the bound of %d", len(full), Plotted)
	}
	if len(full) < Plotted {
		t.Fatalf("the curve returned %d points from %d distinct scores; it should fill the bound", len(full), n)
	}
	last := full[len(full)-1]
	if last.TruePositive != 1 || last.FalsePositive != 1 {
		t.Errorf("the sampled curve lost its far end: %+v", last)
	}
	if full[0].Threshold <= full[len(full)-1].Threshold {
		t.Error("the sampled curve is not descending")
	}
	for i := 1; i < len(full); i++ {
		if full[i].TruePositive < full[i-1].TruePositive || full[i].FalsePositive < full[i-1].FalsePositive {
			t.Fatalf("sampling reordered the curve at %d", i)
		}
	}
}

func at(i int) time.Time { return time.Unix(int64(1_700_000_000+i*60), 0).UTC() }
