package evaluate

// learning.go answers the question an operator asks before spending money on
// more labels: is this getting better, and would more data help?
//
// A learning curve is the metric as a function of how much history the fit had.
// The one built here has TWO arms, and the pair is the point:
//
//	train  the metric on the same rows the calibration was fitted on. Optimistic
//	       by construction.
//	ahead  the metric on the rows that came AFTER — data the fit had not seen,
//	       in the direction time actually runs.
//
// A curve where both arms are still falling means more labels would help. A
// curve where train keeps falling and ahead has stopped means the fit has
// started memorising the sample, and the next thing to change is the method, not
// the volume. A curve where ahead is flat and equal to train means the fit is as
// good as this feature space allows. Those are three different decisions and one
// arm cannot distinguish them.
//
// THE SPLIT IS TEMPORAL AND ONLY TEMPORAL. Never random. Two reasons, and the
// second is the one that actually bites:
//
//	A random split lets a fit see the future. Fraud arrives in campaigns; a
//	random holdout puts half of one campaign in training and half in test, and
//	the fit scores brilliantly on a pattern it has already been shown.
//
//	A random split lets a fit see the same ENTITY on both sides. One device, one
//	card, one account appearing in train and in test means the model is being
//	rewarded for memorising an identifier rather than for recognising behaviour,
//	and that is invisible in every metric. Ordering by time and cutting once is
//	the only split that cannot do either.
//
// SEPARATION IS RANK-INVARIANT AND CALIBRATION IS NOT — which is why both are
// reported and why they say different things. A calibration is monotone, so it
// cannot reorder anything: ROC and PR are IDENTICAL before and after fitting
// one. Brier is not. So the ROC/PR arm of the curve measures the SCORER getting
// better as the tenant's model learns, and the Brier arm measures the
// CALIBRATION getting better as labels accrue. Reading either as the other is
// the most common way a learning curve is misused.

import (
	"strconv"

	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/policy"
)

// Step is one point of a learning curve.
type Step struct {
	// Rows is how many observations the fit was given, and Judged how many of
	// those carried a disposition — the number that actually constrains the fit.
	Rows   int `json:"rows"`
	Judged int `json:"judged"`
	// Held is the size of the fixed later window every step is measured against.
	// It is the same at every step, which is what makes two steps comparable.
	Held int `json:"held"`
	// Productive is how many of the judged were positive. A step with two
	// positives produces a number; it does not produce evidence, and this column
	// is how a reader sees which is which.
	Productive int `json:"productive"`
	// Train is the report on the rows the fit saw.
	Train Metrics `json:"train"`
	// Ahead is the report on the fixed later window — the same rows at every
	// step, all of them after every row the fit saw.
	Ahead Metrics `json:"ahead"`
	// Refusal names why a step produced no fit — almost always that too few of
	// one class had accrued by that point. It is stated rather than skipped, so
	// the curve shows where the plane became able to answer at all.
	Refusal string `json:"refusal,omitempty"`
}

// Learning walks a growing temporal prefix of the history, fitting a calibration
// on each prefix and measuring it on the prefix and on ONE FIXED window of
// later data.
//
// THE AHEAD WINDOW IS THE SAME AT EVERY STEP, and that is the difference between
// a curve and a set of unrelated numbers. The obvious construction — train on
// the first n, test on everything after n — changes the test set at every step:
// the last point is measured on a handful of rows and the first on almost all of
// them, so the curve's own noise grows along the x-axis and a rise at the right
// end says nothing about the fit. Holding the window fixed makes two steps
// comparable, which is the only reason to draw them on one chart.
//
// The window is the final 1/(steps+1) of the history and the training pool is
// everything before it, so nothing in the pool is later than anything in the
// window and no step can see the future.
//
// steps is how many points the curve has. Ten is the default and is deliberate:
// enough to see a shape, few enough that the answer is a chart rather than a
// data set, and the same ten the topology search already uses so two curves in
// one console share an x-axis.
//
// history must be in time order. It is not sorted here, for the reason stated in
// Replay: silently repairing a caller's ordering would make a leaked split
// indistinguishable from a clean one.
func Learning(history []Observation, method, shape string, steps int, cost policy.Cost) []Step {
	if steps <= 0 {
		steps = 10
	}
	if len(history) < 2 {
		return nil
	}
	cut := len(history) - len(history)/(steps+1)
	if cut >= len(history) {
		cut = len(history) - 1
	}
	pool, ahead := history[:cut], history[cut:]

	out := make([]Step, 0, steps)
	for i := 1; i <= steps; i++ {
		n := len(pool) * i / steps
		if n == 0 {
			continue
		}
		train := pool[:n]
		s := Step{Rows: n, Held: len(ahead)}
		samples := make([]calibrate.Sample, 0, n)
		for _, o := range train {
			if !o.judged() {
				continue
			}
			s.Judged++
			if o.productive() {
				s.Productive++
			}
			samples = append(samples, calibrate.Sample{
				Score: o.Score, Productive: o.productive(), Weight: o.Weight,
			})
		}
		cal, err := calibrate.Fit(samples, method, shape)
		if err != nil {
			s.Refusal = err.Error()
			// The separation arm needs no calibration — it is a rank statistic —
			// so it is still measured and reported. A curve that went blank
			// wherever the calibration refused would hide the model improving
			// underneath it.
			s.Train = Measure(train, 0, cost, calibrate.Reader{})
			s.Ahead = Measure(ahead, 0, cost, calibrate.Reader{})
			out = append(out, s)
			continue
		}
		// The step's own fit is read under the shape it was just fitted under —
		// the one place that identity is a fact rather than an assumption. A bind
		// that refuses here is a fit that cannot describe its own sample, which is
		// a refusal and not a step.
		read, err := cal.Under(shape)
		if err != nil {
			s.Refusal = err.Error()
			s.Train = Measure(train, 0, cost, calibrate.Reader{})
			s.Ahead = Measure(ahead, 0, cost, calibrate.Reader{})
			out = append(out, s)
			continue
		}
		// The operating point is the score at the median fitted probability, so
		// every step is measured at a comparable place on its own map rather than
		// at a constant score that means something different at each step.
		at := scoreAt(read, 0.5)
		s.Train = Measure(train, at, cost, read)
		s.Ahead = Measure(ahead, at, cost, read)
		out = append(out, s)
	}
	return out
}

func strconvInt(v int) string     { return strconv.Itoa(v) }
func strconvInt64(v int64) string { return strconv.FormatInt(v, 10) }
