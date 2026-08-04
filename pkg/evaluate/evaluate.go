// Package evaluate measures a decision rule against what actually happened, and
// replays a candidate one over recorded history to see what it would have done.
//
// WHY NOT ACCURACY. Fraud is rare. A stream at one productive event in a
// thousand is 99.9% accurate if the model says "fine" to everything, and that
// model is worth nothing. Accuracy on an imbalanced stream is not a weak metric,
// it is an actively misleading one, so this package does not compute it. What it
// computes instead:
//
//	ROC-AUC     the probability that a productive event outranks an unproductive
//	            one. Insensitive to the base rate, which makes it comparable
//	            across tenants — and, for the same reason, optimistic-looking on
//	            a rare-event stream, because the enormous negative class makes
//	            false positives cheap in its arithmetic.
//	PR-AUC      average precision: the area under precision-recall, computed as
//	            the sum of precision at each recall step rather than by
//	            trapezoid, because interpolating between operating points on a PR
//	            curve credits a model with points it cannot actually reach. This
//	            is the number that moves when a rare-event model gets better, and
//	            it is the one to argue about.
//	Prevalence  reported beside both, always. An AUC without the base rate is
//	            uninterpretable, and a PR-AUC is bounded below by the prevalence,
//	            so 0.1 is excellent at 1-in-1000 and worthless at 1-in-5.
//	Cost        the only metric with a unit anybody outside the team cares
//	            about. A miss and a false alarm have different prices; the
//	            cost-minimising threshold is where the marginal one of each
//	            balances, and it is almost never where a round number sits.
//
// EVERY RATE IS A POINTER. An unmeasured proportion reported as 0.0 reads as a
// perfect model, and on a plane where most events are unjudged that is the
// default state. Absent means absent.
//
// THE COST ARITHMETIC IS EXACT. Prices are int64 nano-units multiplied by
// counts. No float touches money here: a cost curve compared across thresholds
// in floating point can order two thresholds by rounding noise, and the answer
// is which one to put in production.
package evaluate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/replay"
)

// Observation is one recorded decision: what the model said, what the system
// did, and what turned out to be true.
//
// Disposition is replay.Disposition and not a second vocabulary. The engine's
// exhaustive search already refuses to name a winner without judged events, and
// a parallel set of label words would leave it permanently unable to read the
// labels this plane collects.
type Observation struct {
	// ID identifies the decision, so a report can point at the rows behind a
	// number instead of only stating it.
	ID string
	// At is when it was decided. The order of a slice of these is the order they
	// happened, and every temporal split in this package relies on that.
	At time.Time
	// Score is the raw model score in [0,1] AS RECORDED AT THE TIME. It is not
	// recomputed: the rings have moved on, and re-scoring history through a model
	// that has since learned from that same history is how a backtest comes to
	// know the future.
	Score float64
	// Action is what the live system actually did.
	Action string
	// Disposition is what a human or a processor concluded, when anyone has.
	Disposition replay.Disposition
	// Weight down-weights a low-confidence label. Zero means one.
	Weight float64
}

func (o Observation) weight() float64 {
	if o.Weight <= 0 {
		return 1
	}
	return o.Weight
}

func (o Observation) judged() bool {
	return o.Disposition == replay.Productive || o.Disposition == replay.Unproductive
}

func (o Observation) productive() bool { return o.Disposition == replay.Productive }

// Confusion is the four counts at one operating point, weighted.
type Confusion struct {
	// TP is productive and flagged; FN is productive and let through — the
	// misses, which is what Cost.Miss prices.
	TP float64 `json:"tp"`
	FN float64 `json:"fn"`
	// FP is unproductive and flagged — the false alarms, which Cost.Alarm
	// prices; TN is unproductive and let through.
	FP float64 `json:"fp"`
	TN float64 `json:"tn"`
}

// Metrics is the report. Every proportion is absent rather than zero when
// nothing supports it.
type Metrics struct {
	// Rows is every observation looked at; Judged is how many carry a
	// disposition. The gap between them is the honest limit on everything below,
	// and it is reported first for that reason.
	Rows   int `json:"rows"`
	Judged int `json:"judged"`
	// Productive and Unproductive are the two judged classes, so the imbalance is
	// visible before anyone reads a rate computed over it.
	Productive   int `json:"productive"`
	Unproductive int `json:"unproductive"`
	// Prevalence is the productive share of the judged set.
	Prevalence *float64 `json:"prevalence,omitempty"`
	// ROC is the area under the receiver-operating curve, with midranks for
	// ties. Absent without both classes.
	ROC *float64 `json:"roc,omitempty"`
	// PR is average precision — the area under precision-recall by the step
	// definition. This is the imbalanced-data number.
	PR *float64 `json:"pr,omitempty"`
	// Threshold is the operating point everything below is measured at.
	Threshold *float64 `json:"threshold,omitempty"`
	// Precision, Recall and F1 at that point.
	Precision *float64 `json:"precision,omitempty"`
	Recall    *float64 `json:"recall,omitempty"`
	F1        *float64 `json:"f1,omitempty"`
	// Alarm is the share of the judged stream flagged at that point — the
	// realised review rate, which is the number an operations team is actually
	// resourced against.
	Alarm *float64 `json:"alarm,omitempty"`
	// Lift is precision divided by prevalence: how much better than chance the
	// flagged set is. One means the model is choosing at random.
	Lift *float64 `json:"lift,omitempty"`
	// Confusion is the four counts at the operating point.
	Confusion *Confusion `json:"confusion,omitempty"`
	// CostNano is what being wrong cost at that point, in nano-units, and
	// BestNano is the lowest it reaches anywhere, at BestThreshold. Absent when
	// the tenant has not stated a price, because a cost computed from unstated
	// prices is a number with no meaning and a decimal point on it.
	CostNano      *int64   `json:"cost_nano,omitempty"`
	BestNano      *int64   `json:"best_nano,omitempty"`
	BestThreshold *float64 `json:"best_threshold,omitempty"`
	// Brier is the calibration error of the supplied map on this data. A
	// well-ranked, badly-calibrated model makes every rung of the policy mean
	// something other than what it says, and ROC and PR cannot see that at all —
	// both are rank statistics, and a monotone calibration cannot change either.
	Brier *float64 `json:"brier,omitempty"`
	// Refusal names why a report is thin when it is. Silence and a clean result
	// are the same bytes otherwise.
	Refusal string `json:"refusal,omitempty"`
}

// Point is one operating point of a curve.
type Point struct {
	// Threshold is the score at or above which an observation is flagged.
	Threshold float64 `json:"threshold"`
	// TruePositive and FalsePositive are the ROC axes (recall and fall-out).
	TruePositive  float64 `json:"tpr"`
	FalsePositive float64 `json:"fpr"`
	// Precision is the PR axis against Recall, which equals TruePositive.
	Precision float64 `json:"precision"`
	// CostNano is what this point costs under the stated prices. Zero when no
	// price is stated; Metrics is where absence is expressed, and a curve is a
	// dense array a console plots.
	CostNano int64 `json:"cost_nano"`
}

// Measure computes the report at one operating point.
//
// threshold is on the SCORE, not the probability, because the score is what was
// recorded and a calibration can be refitted afterwards. A caller holding a
// policy over probabilities converts once, at the boundary, rather than making
// every metric here depend on a map that may not exist.
//
// cal may be the zero Reader — no calibration, or one whose shape has moved.
// Brier is then absent and everything else is unaffected, since every other
// metric here is a rank statistic. It is a READER and not a Map because the
// question "does this map apply to these coordinates" is answered once, at the
// boundary that knows the live shape, and never re-asked here.
func Measure(obs []Observation, threshold float64, cost policy.Cost, cal calibrate.Reader) Metrics {
	m := Metrics{Rows: len(obs)}
	judged := make([]Observation, 0, len(obs))
	for _, o := range obs {
		if !o.judged() || math.IsNaN(o.Score) || math.IsInf(o.Score, 0) {
			continue
		}
		judged = append(judged, o)
		if o.productive() {
			m.Productive++
		} else {
			m.Unproductive++
		}
	}
	m.Judged = len(judged)
	if m.Judged == 0 {
		m.Refusal = "nothing in this window has been judged, so no rate can be measured"
		return m
	}
	prev := float64(m.Productive) / float64(m.Judged)
	m.Prevalence = ptr(round6(prev))

	if m.Productive == 0 || m.Unproductive == 0 {
		m.Refusal = "the judged observations are all one class, so separation cannot be measured"
	} else {
		m.ROC = ptr(round6(roc(judged)))
		m.PR = ptr(round6(averagePrecision(judged)))
	}

	c := confusionAt(judged, threshold)
	m.Threshold = ptr(round6(threshold))
	m.Confusion = &c
	if flagged := c.TP + c.FP; flagged > 0 {
		p := c.TP / flagged
		m.Precision = ptr(round6(p))
		if prev > 0 {
			m.Lift = ptr(round6(p / prev))
		}
	}
	if actual := c.TP + c.FN; actual > 0 {
		m.Recall = ptr(round6(c.TP / actual))
	}
	if total := c.TP + c.FP + c.TN + c.FN; total > 0 {
		m.Alarm = ptr(round6((c.TP + c.FP) / total))
	}
	if m.Precision != nil && m.Recall != nil && (*m.Precision+*m.Recall) > 0 {
		m.F1 = ptr(round6(2 * *m.Precision * *m.Recall / (*m.Precision + *m.Recall)))
	}
	if cost.Stated() {
		v, ok := nano(c, cost)
		best, at, okBest := minimum(judged, cost)
		if ok && okBest {
			m.CostNano = ptrInt(v)
			m.BestNano, m.BestThreshold = ptrInt(best), ptr(round6(at))
		} else if m.Refusal == "" {
			m.Refusal = "the stated prices cannot be multiplied by this many observations without overflowing, so no cost is reported"
		}
	}
	if b, ok := brier(judged, cal); ok {
		m.Brier = ptr(round6(b))
	}
	return m
}

// Curve is the whole sweep, one point per distinct score, descending. It is what
// a console renders as ROC, as precision-recall and as a cost curve — three
// charts from one pass, because they are three projections of the same sweep.
//
// One point per DISTINCT score, not per observation: two observations at the
// same score cannot be separated by any threshold, so a curve with a point
// between them describes an operating point that does not exist.
func Curve(obs []Observation, cost policy.Cost) []Point {
	judged := make([]Observation, 0, len(obs))
	var pos, neg float64
	for _, o := range obs {
		if !o.judged() || math.IsNaN(o.Score) || math.IsInf(o.Score, 0) {
			continue
		}
		judged = append(judged, o)
		if o.productive() {
			pos += o.weight()
		} else {
			neg += o.weight()
		}
	}
	if len(judged) == 0 || pos == 0 || neg == 0 {
		return nil
	}
	sort.SliceStable(judged, func(i, j int) bool { return judged[i].Score > judged[j].Score })
	priced := cost.Stated()

	out := make([]Point, 0, len(judged))
	var tp, fp float64
	for i := 0; i < len(judged); {
		j := i
		for j < len(judged) && judged[j].Score == judged[i].Score {
			if judged[j].productive() {
				tp += judged[j].weight()
			} else {
				fp += judged[j].weight()
			}
			j++
		}
		p := Point{
			Threshold:     round6(judged[i].Score),
			TruePositive:  round6(tp / pos),
			FalsePositive: round6(fp / neg),
			Precision:     round6(tp / (tp + fp)),
		}
		if priced {
			v, ok := nano(Confusion{TP: tp, FP: fp, FN: pos - tp, TN: neg - fp}, cost)
			if !ok {
				// One unrepresentable point makes the whole cost dimension
				// unreadable: a curve with a hole in it plots as a cliff. Drop it
				// for the sweep and let Metrics carry the refusal.
				priced = false
				for k := range out {
					out[k].CostNano = 0
				}
			}
			p.CostNano = v
		}
		out = append(out, p)
		i = j
	}
	return sample(out)
}

// sample bounds what a curve RETURNS without moving where its points sit.
//
// The sweep above is over every distinct score and stays that way: Metrics and
// the cost minimum are exact whatever this does. A curve is a CHART, and the
// argument that bounds Moved applies unchanged — a person reading one does not
// need the ten-thousandth point, and a response carrying fifty thousand of them
// is a megabyte nobody renders.
//
// It takes an evenly spaced subsequence and always keeps both ends, so the
// shape, the extremes and the monotonicity survive. Truncating instead would cut
// the curve off at one end and describe a model that stops.
func sample(points []Point) []Point {
	if len(points) <= Plotted {
		return points
	}
	out := make([]Point, 0, Plotted)
	last := len(points) - 1
	for i := range Plotted - 1 {
		out = append(out, points[i*last/(Plotted-1)])
	}
	return append(out, points[last])
}

// confusionAt counts the four cells at one threshold. Flagged is score >= t, so
// a threshold of zero flags everything and a threshold above one flags nothing —
// both ends reachable, which is what makes the sweep total.
func confusionAt(judged []Observation, t float64) Confusion {
	var c Confusion
	for _, o := range judged {
		w := o.weight()
		switch {
		case o.Score >= t && o.productive():
			c.TP += w
		case o.Score >= t:
			c.FP += w
		case o.productive():
			c.FN += w
		default:
			c.TN += w
		}
	}
	return c
}

// nano prices a confusion, and says whether the price is representable.
//
// Counts are weighted and therefore float; the MULTIPLICATION is done once, per
// cell, and rounded to the nearest nano before summing, so the result is an
// exact integer number of nano-units and two calls on the same counts cannot
// differ.
//
// It returns false rather than a wrapped number. policy.MaxPrice already keeps a
// sealed ladder's prices inside the range where this cannot happen, but a
// wrapped cost is the one arithmetic failure here that does not look like a
// failure — it looks like a recommendation — so the arithmetic refuses on its
// own account and does not rely on having been called correctly. A policy
// recorded before that bound existed reads back through this same path.
func nano(c Confusion, cost policy.Cost) (int64, bool) {
	miss, ok := mul(int64(math.Round(c.FN)), cost.Miss)
	if !ok {
		return 0, false
	}
	alarm, ok := mul(int64(math.Round(c.FP)), cost.Alarm)
	if !ok {
		return 0, false
	}
	sum := miss + alarm
	if sum < miss {
		return 0, false
	}
	return sum, true
}

// mul multiplies a count by a price, reporting whether the product fits. Both
// are non-negative here, so the check is the single division that inverts the
// multiplication exactly.
func mul(count, price int64) (int64, bool) {
	if count <= 0 || price <= 0 {
		return 0, true
	}
	if count > math.MaxInt64/price {
		return 0, false
	}
	return count * price, true
}

// Plotted bounds how many points a curve returns. A thousand is denser than any
// screen, and the metrics behind it are computed over every distinct score
// regardless — this bounds the rendering, never the arithmetic.
const Plotted = 1000

// minimum sweeps every reachable threshold and returns the lowest cost and where
// it is reached.
//
// Ties go to the HIGHER threshold — the one that flags less. Two thresholds that
// cost the same are not equivalent to the business: the one that stops fewer
// customers is the one to choose, and leaving the tie to iteration order would
// make the recommendation move between runs on identical data.
func minimum(judged []Observation, cost policy.Cost) (int64, float64, bool) {
	sorted := append([]Observation(nil), judged...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })

	var pos, neg float64
	for _, o := range sorted {
		if o.productive() {
			pos += o.weight()
		} else {
			neg += o.weight()
		}
	}
	// The empty flagged set is a real operating point — flag nothing, pay for
	// every miss — and it is the right answer when alarms cost more than misses.
	best, ok := nano(Confusion{FN: pos, TN: neg}, cost)
	if !ok {
		return 0, 0, false
	}
	at := math.Nextafter(1, 2)
	var tp, fp float64
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j].Score == sorted[i].Score {
			if sorted[j].productive() {
				tp += sorted[j].weight()
			} else {
				fp += sorted[j].weight()
			}
			j++
		}
		v, ok := nano(Confusion{TP: tp, FP: fp, FN: pos - tp, TN: neg - fp}, cost)
		if !ok {
			return 0, 0, false
		}
		if v < best {
			best, at = v, sorted[i].Score
		}
		i = j
	}
	return best, at, true
}

// roc is the area under the receiver-operating curve, by the Mann-Whitney
// identity: the probability that a random productive observation outranks a
// random unproductive one.
//
// Computed from midranks, so ties count as half a win rather than a whole one.
// Without that a model that gives every observation the same score — which is
// exactly what a warming detector does — scores 1.0 instead of 0.5.
func roc(judged []Observation) float64 {
	sorted := append([]Observation(nil), judged...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score < sorted[j].Score })

	var sumRanks, pos, neg float64
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j].Score == sorted[i].Score {
			j++
		}
		// Ranks are one-based; the midrank of the block [i,j) is the mean of the
		// ranks it spans.
		mid := (float64(i+1) + float64(j)) / 2
		for k := i; k < j; k++ {
			w := sorted[k].weight()
			if sorted[k].productive() {
				sumRanks += mid * w
				pos += w
			} else {
				neg += w
			}
		}
		i = j
	}
	if pos == 0 || neg == 0 {
		return 0
	}
	return (sumRanks - pos*(pos+1)/2) / (pos * neg)
}

// averagePrecision is the area under precision-recall by the STEP definition:
// the sum over operating points of (recall gained) x (precision there).
//
// Not the trapezoid. Interpolating between two PR operating points draws a line
// through points the classifier cannot actually reach, and it flatters exactly
// the rare-event case this metric exists for. Ties are handled as one block, so
// two observations at one score contribute one step and the answer does not
// depend on which of them the sort happened to put first.
func averagePrecision(judged []Observation) float64 {
	sorted := append([]Observation(nil), judged...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })

	var pos float64
	for _, o := range sorted {
		if o.productive() {
			pos += o.weight()
		}
	}
	if pos == 0 {
		return 0
	}
	var tp, seen, ap, prevRecall float64
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j].Score == sorted[i].Score {
			w := sorted[j].weight()
			seen += w
			if sorted[j].productive() {
				tp += w
			}
			j++
		}
		recall := tp / pos
		precision := tp / seen
		ap += (recall - prevRecall) * precision
		prevRecall = recall
		i = j
	}
	return ap
}

// brier is the mean squared error of the calibration on these observations. It
// is the out-of-sample number when the caller passes data the map was not fitted
// on, which is the only version of it worth reporting.
func brier(judged []Observation, cal calibrate.Reader) (float64, bool) {
	if !cal.Fitted() {
		return 0, false
	}
	var sum, w float64
	for _, o := range judged {
		p := cal.P(o.Score)
		y := 0.0
		if o.productive() {
			y = 1
		}
		sum += o.weight() * (p - y) * (p - y)
		w += o.weight()
	}
	if w == 0 {
		return 0, false
	}
	return sum / w, true
}

func ptr(v float64) *float64 { return &v }
func ptrInt(v int64) *int64  { return &v }

func round6(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1e6) / 1e6
}

// canonical renders a float for a digest: fixed precision, no exponent, so two
// runs that agree to six places agree byte for byte.
func canonical(v float64) string { return strconv.FormatFloat(round6(v), 'f', 6, 64) }

// fingerprint folds a sequence of strings into one identity.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
