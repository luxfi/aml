// Package calibrate turns a score into a probability.
//
// THE PROBLEM IT SOLVES. A half-space tree's score is a density: how isolated a
// point is among the mass this tenant's recent traffic laid down. It ranks well
// and it means nothing on its own. 0.8 is not "80% likely to be fraud" — it is
// not a likelihood at all, and two tenants' 0.8s are not comparable because the
// mass under them is different. Every threshold set on such a score is a number
// someone liked, every cost calculation over it is arithmetic on the wrong
// units, and an adverse decision defended with it cannot answer "how likely was
// this, actually?".
//
// A calibration is the missing map. Fitted on a tenant's OWN judged decisions,
// it answers: among the decisions that scored near here, what share turned out
// to be productive? That number IS a probability, it is comparable across
// tenants and across model versions, and it is the only thing a cost-weighted
// policy can be stated over honestly.
//
// TWO METHODS, AND WHEN EACH IS RIGHT.
//
//	Isotonic  the non-parametric one. Assumes only that a higher score is never
//	          less likely to be productive — the one assumption the detector
//	          actually guarantees. Fits the exact least-squares monotone step
//	          function by pool-adjacent-violators, then interpolates linearly
//	          between block centroids so a policy threshold landing inside a
//	          plateau still means something. Needs data: with a few dozen
//	          positives it will fit the noise.
//	Platt     the parametric one: one logistic through the scores. Two
//	          parameters, so it survives a small sample, at the cost of assuming
//	          a shape. Fitted with Platt's own label smoothing, which is not
//	          optional on imbalanced data — without it the fit runs off to
//	          certainty on whichever class is scarce.
//
// WHAT IT REFUSES. A calibration fitted on one class is a constant, and a
// constant reported as a probability is a lie with a decimal point on it. Fit
// refuses below a floor of judged rows and below a floor of each class, and
// names which. An absent calibration is a fact a caller can act on; an invented
// one is not.
//
// THE SHAPE GATE — this is the training–serving skew control. A Map records the
// digest of the scorer it was fitted under (anomaly.Store.Digest(), which covers
// the feature inventory in order, the neutral values and the detector geometry).
// A Map cannot be read at all: Under(shape) is the only way to obtain a Reader,
// and it refuses for a scorer whose shape differs. So a calibration cannot
// outlive the model it describes: change a feature, and every map fitted before
// the change stops answering instead of silently mapping coordinates that no
// longer mean what they meant.
//
// The gate is at the BIND and not at the read, because a gate every read has to
// pass is a gate every read can be made to pass — the shape argument was
// available from the map itself, so passing m.Shape cleared the control and
// looked exactly like clearing it honestly. Reading is total once the bind has
// happened, so there is one place to get it right and no way downstream to get
// it wrong.
package calibrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Methods.
const (
	// Isotonic is the non-parametric monotone fit.
	Isotonic = "isotonic"
	// Platt is the two-parameter logistic fit.
	Platt = "platt"
)

// Floors. Below these a fit is refused rather than produced.
//
// Rows is a small number on purpose: the point is to refuse the degenerate case,
// not to impose a statistical opinion about sufficiency. The caller sees Rows
// and Positive on the Map and decides for itself whether to trust the fit; a
// package that silently declined to answer until it judged the sample large
// enough would be making that decision in the wrong place.
const (
	// MinRows is the fewest judged observations a fit will look at.
	MinRows = 20
	// MinClass is the fewest of EACH class. One of each is arithmetically
	// sufficient and statistically meaningless; five is the point at which the
	// isotonic blocks stop being individual observations.
	MinClass = 5
)

// Errors. Each names the thing that was missing, because "calibration failed" is
// not something an operator can act on.
var (
	ErrThin       = errors.New("calibrate: too few judged observations to fit a probability")
	ErrOneClass   = errors.New("calibrate: the judged observations are all one class, so any fit would be a constant")
	ErrMethod     = errors.New("calibrate: unknown method")
	ErrShape      = errors.New("calibrate: this map was fitted under a different model shape")
	ErrNoShape    = errors.New("calibrate: a map must record the model shape it was fitted under")
	ErrNotFitted  = errors.New("calibrate: this map holds no fit")
	ErrConverging = errors.New("calibrate: the logistic fit did not converge")
)

// Sample is one judged observation: what the model said, and what turned out to
// be true.
//
// Productive is the engine's own vocabulary (replay.Disposition), reduced to the
// one bit a fit reads. Unjudged observations are not Samples — a caller filters
// them out, because a fit over "unknown treated as negative" is a fit against
// the incumbent policy rather than against the world.
type Sample struct {
	// Score is the raw model score in [0,1].
	Score float64
	// Productive is what a human or a processor concluded: true when the
	// decision was right to be worried.
	Productive bool
	// Weight lets a caller down-weight a low-confidence label. Zero or negative
	// means one, so the zero value of Sample is an ordinary unweighted
	// observation.
	Weight float64
}

func (s Sample) weight() float64 {
	if s.Weight <= 0 {
		return 1
	}
	return s.Weight
}

// Knot is one point of the fitted map: at this score, this probability. Between
// two knots P interpolates linearly; outside the outermost pair it clamps.
type Knot struct {
	Score float64 `json:"score"`
	P     float64 `json:"p"`
}

// Map is a fitted calibration. It is a VALUE: immutable once returned, safe to
// share across goroutines, and cheap enough to hold one per tenant in memory and
// to write as a row.
type Map struct {
	// Method is isotonic or platt.
	Method string `json:"method"`
	// Knots is the isotonic fit, ascending by score. Empty for platt.
	Knots []Knot `json:"knots,omitempty"`
	// A and B are the platt fit: p = 1/(1+exp(A*score+B)). Zero for isotonic.
	A float64 `json:"a,omitempty"`
	B float64 `json:"b,omitempty"`
	// Shape is the digest of the scorer this was fitted under. P refuses under
	// any other shape, which is what stops a calibration from outliving the
	// coordinates it describes.
	Shape string `json:"shape"`
	// Rows, Positive and Negative are the sample it was fitted on, so a caller
	// can see how much evidence is behind the number before acting on it.
	Rows     int `json:"rows"`
	Positive int `json:"positive"`
	Negative int `json:"negative"`
	// Prevalence is the productive share of the fitting sample. A probability
	// read without it is uninterpretable: 0.4 is alarming at a base rate of
	// 1-in-1000 and unremarkable at 1-in-3.
	Prevalence float64 `json:"prevalence"`
	// Brier is the mean squared error of this map on its own fitting sample. It
	// is the in-sample number and is therefore optimistic; the honest one comes
	// from evaluating the map on data it was not fitted on, which is what the
	// learning curve does.
	Brier float64 `json:"brier"`
	// Digest identifies this fit exactly. A decision records it, so an auditor
	// can pin which map turned which score into which probability.
	Digest string `json:"digest"`
}

// Fitted reports whether this map can answer.
func (m Map) Fitted() bool {
	return m.Shape != "" && (len(m.Knots) > 0 || m.Method == Platt)
}

// Under binds this map to the scoring shape now in force, refusing when they
// differ. It is the ONLY way to read a probability out of a Map.
//
// A GATE A CALLER CAN SATISFY IS NOT A GATE. The earlier read was
// P(score, shape), and every internal call site passed m.Shape — the value being
// compared came from the value being checked, so the training–serving skew
// control was trivially true everywhere except the one place a live shape
// happened to be threaded in. Nothing about that was visible at the call site;
// each one looked like it was passing the gate.
//
// Binding separates the two concerns. ONE place decides whether this map applies
// to these coordinates, at the boundary where the current shape is a fact the
// server observed rather than a value a caller supplied. Everything downstream
// takes a Reader, for which the question is already answered and cannot be
// re-asked wrongly — a Reader has no shape to pass and no Map to read one off.
func (m Map) Under(shape string) (Reader, error) {
	if !m.Fitted() {
		return Reader{}, ErrNotFitted
	}
	if shape == "" {
		return Reader{}, ErrNoShape
	}
	if shape != m.Shape {
		return Reader{}, fmt.Errorf("%w: fitted under %s, asked under %s", ErrShape, short(m.Shape), short(shape))
	}
	if m.Method != Isotonic && m.Method != Platt {
		return Reader{}, fmt.Errorf("%w: %q", ErrMethod, m.Method)
	}
	return Reader{m: m, ok: true}, nil
}

// Reader is a calibration that has already been checked against the coordinates
// it is about to be read in: a map bound to one scoring shape.
//
// It has no exported fields and no constructor other than Map.Under, so a
// function taking a Reader is a function whose caller has been through the gate,
// and that is a property the compiler checks rather than a reviewer.
//
// The ZERO Reader is the honest "no probability here": it is what a caller holds
// when this organisation has no calibration at all AND what it holds when the
// shape has moved, which are the two facts every consumer downstream has to
// treat the same way — silently, by not stating a probability.
type Reader struct {
	m  Map
	ok bool
}

// Fitted reports whether this reader can answer.
func (r Reader) Fitted() bool { return r.ok }

// Digest names the fit behind this reader, so a record can pin which map turned
// which score into which probability. Empty when it cannot answer.
func (r Reader) Digest() string {
	if !r.ok {
		return ""
	}
	return r.m.Digest
}

// P maps one score to a probability. It is TOTAL: the only thing that could have
// failed is the shape, and holding a Reader is proof it did not.
func (r Reader) P(score float64) float64 {
	if !r.ok {
		return 0
	}
	if r.m.Method == Platt {
		return clamp01(1 / (1 + math.Exp(r.m.A*score+r.m.B)))
	}
	return clamp01(interpolate(r.m.Knots, score))
}

// interpolate reads the piecewise-linear map at score, clamping outside the
// fitted range.
//
// Linear between knots rather than a step, deliberately. The exact
// pool-adjacent-violators solution is a step function, and a step means every
// score inside one block gets one probability — so a policy threshold that lands
// inside a block is arbitrary within it, and moving the threshold by a hair
// either changes nothing or changes everything. Interpolating keeps the fit
// monotone (both endpoints are, and a line between two non-decreasing values is)
// while making the map strictly informative about where in the block a score
// sat.
func interpolate(k []Knot, score float64) float64 {
	if len(k) == 0 {
		return 0
	}
	if score <= k[0].Score {
		return k[0].P
	}
	last := len(k) - 1
	if score >= k[last].Score {
		return k[last].P
	}
	i := sort.Search(len(k), func(i int) bool { return k[i].Score >= score })
	lo, hi := k[i-1], k[i]
	if hi.Score == lo.Score {
		return hi.P
	}
	t := (score - lo.Score) / (hi.Score - lo.Score)
	return lo.P + t*(hi.P-lo.P)
}

// Fit produces a calibration from judged observations.
//
// shape is the scorer's own digest and is required: a map that does not know
// what it was fitted under cannot refuse to be misapplied, and refusing to be
// misapplied is most of what a calibration is for.
//
// The fit is DETERMINISTIC. Samples are sorted by score with ties broken by
// class, so two calls on the same multiset — in any order — return byte-identical
// knots and the same digest. That is what lets a fit be a record: an auditor
// re-running it gets the same map or knows the inputs changed.
func Fit(samples []Sample, method, shape string) (Map, error) {
	if shape == "" {
		return Map{}, ErrNoShape
	}
	switch method {
	case Isotonic, Platt:
	default:
		return Map{}, fmt.Errorf("%w: %q", ErrMethod, method)
	}

	rows, pos, neg := 0, 0, 0
	for _, s := range samples {
		if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) {
			continue
		}
		rows++
		if s.Productive {
			pos++
		} else {
			neg++
		}
	}
	if rows < MinRows {
		return Map{}, fmt.Errorf("%w: %d judged, %d needed", ErrThin, rows, MinRows)
	}
	if pos < MinClass || neg < MinClass {
		return Map{}, fmt.Errorf("%w: %d productive, %d unproductive, %d of each needed", ErrOneClass, pos, neg, MinClass)
	}

	clean := make([]Sample, 0, rows)
	for _, s := range samples {
		if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) {
			continue
		}
		clean = append(clean, s)
	}
	// Score ascending, so the fit does not depend on the caller's order. The
	// tie-break by class is cosmetic and stays only because a stable, stated order
	// is easier to read in a dump: pava pools every sample at one score into one
	// block before it starts, so the order WITHIN a tie cannot reach the answer.
	// Relying on that order instead of pooling is exactly the bug pooling fixes.
	sort.SliceStable(clean, func(i, j int) bool {
		if clean[i].Score != clean[j].Score {
			return clean[i].Score < clean[j].Score
		}
		return !clean[i].Productive && clean[j].Productive
	})

	m := Map{
		Method: method, Shape: shape,
		Rows: rows, Positive: pos, Negative: neg,
		Prevalence: float64(pos) / float64(rows),
	}
	var err error
	switch method {
	case Isotonic:
		m.Knots = pava(clean)
	case Platt:
		m.A, m.B, err = platt(clean, pos, neg)
		if err != nil {
			return Map{}, err
		}
	}
	// The in-sample Brier reads the map through the shape it was just fitted
	// under, which is the one place that identity is a fact rather than an
	// assumption — the shape was handed in and stamped on the same value two lines
	// above. Everywhere else the shape comes from the live scorer.
	r, err := m.Under(shape)
	if err != nil {
		return Map{}, err
	}
	m.Brier = brier(clean, r)
	m.Digest = digest(m)
	return m, nil
}

// pava is pool-adjacent-violators: the exact least-squares fit of a
// non-decreasing function to the labels, in one pass over the sorted samples.
//
// TIES ARE POOLED BEFORE THE ALGORITHM STARTS, and that is not a refinement —
// it is what makes the answer isotonic regression at all. A monotone function
// has ONE value at one score, so every sample sharing a score is one block by
// definition. Fed individual samples instead, a run of unproductive-then-
// productive at a single score is already non-decreasing, so the merge condition
// never fires, every sample stays its own block, and the score ends up mapped to
// whichever label the sort happened to put last: 1 wherever any positive sits at
// the top of a tie, 0 wherever none does.
//
// That is the ordinary case here, not an edge. A decision score is
// 1 - prod(1-weight) over a FIXED set of rule weights, rounded to four places, so
// the score alphabet is a handful of atoms and the modal atom is exactly zero.
// Un-pooled, the whole map collapses to a step function pinned at 0 and 1 — every
// plateau reported as certainty, at the top of the range where declines happen.
//
// Each block then holds a weighted mean; whenever a block's mean is not greater
// than its predecessor's the two are merged, which is the only operation the
// algorithm has. The result is the unique monotone least-squares solution — not
// an approximation and not an iterative one — which is why this is 60 lines and
// needs no numerical library.
//
// The knots are the block CENTROIDS (the weighted mean score inside the block)
// rather than the block edges. An edge is where two blocks meet and belongs to
// neither; the centroid is where the block's evidence actually sits, so
// interpolating between centroids puts the map's steepest movement where the
// data is densest.
func pava(sorted []Sample) []Knot {
	type block struct{ sum, weight, score float64 }
	blocks := make([]block, 0, len(sorted))
	push := func(b block) {
		blocks = append(blocks, b)
		for len(blocks) > 1 {
			last := len(blocks) - 1
			if blocks[last-1].sum/blocks[last-1].weight <= blocks[last].sum/blocks[last].weight {
				break
			}
			blocks[last-1].sum += blocks[last].sum
			blocks[last-1].weight += blocks[last].weight
			blocks[last-1].score += blocks[last].score
			blocks = blocks[:last]
		}
	}
	// One block per DISTINCT score. weight() is never zero, so every block the
	// loop pushes has mass and the means below are total.
	for i := 0; i < len(sorted); {
		var b block
		j := i
		for ; j < len(sorted) && sorted[j].Score == sorted[i].Score; j++ {
			w := sorted[j].weight()
			if sorted[j].Productive {
				b.sum += w
			}
			b.weight += w
			b.score += sorted[j].Score * w
		}
		push(b)
		i = j
	}

	// A block's centroid lies inside its own score range, and the ranges are
	// disjoint and ordered, so the centroids ascend strictly — until round6 puts
	// two of them on one score. Those two are POOLED rather than one of them
	// discarded: either endpoint alone states a probability the pooled evidence
	// does not support, and the pooled mean sits between the neighbours it
	// replaced, so the map stays monotone and interpolate stays total.
	knots := make([]Knot, 0, len(blocks))
	for i := 0; i < len(blocks); {
		at := round6(blocks[i].score / blocks[i].weight)
		var sum, weight float64
		j := i
		for ; j < len(blocks) && round6(blocks[j].score/blocks[j].weight) == at; j++ {
			sum += blocks[j].sum
			weight += blocks[j].weight
		}
		knots = append(knots, Knot{Score: at, P: round6(sum / weight)})
		i = j
	}
	return knots
}

// platt fits p = 1/(1+exp(A*score+B)) by Newton's method on the regularised
// log-likelihood.
//
// The targets are SMOOTHED — t+ = (N+ + 1)/(N+ + 2) and t- = 1/(N- + 2) — which
// is Platt's own correction and is not decoration. On a fraud stream the classes
// are wildly unbalanced, and against hard 0/1 targets the maximum-likelihood fit
// drives the scarce class to a probability of exactly one, which is both false
// and unusable in a cost calculation. The smoothed targets are the posterior
// under a uniform prior over the observed counts, so the fit stays inside the
// open unit interval whatever the imbalance.
func platt(sorted []Sample, pos, neg int) (float64, float64, error) {
	hi := (float64(pos) + 1) / (float64(pos) + 2)
	lo := 1 / (float64(neg) + 2)

	a, b := 0.0, math.Log((float64(neg)+1)/(float64(pos)+1))
	const iterations = 128
	const tolerance = 1e-10
	for range iterations {
		var g0, g1, h00, h01, h11 float64
		for _, s := range sorted {
			t := lo
			if s.Productive {
				t = hi
			}
			w := s.weight()
			z := a*s.Score + b
			// p is written in the numerically stable branch for each sign of z;
			// exp of a large positive z overflows, and a probability that came
			// back as NaN would poison the whole Hessian.
			var p float64
			if z >= 0 {
				p = math.Exp(-z) / (1 + math.Exp(-z))
			} else {
				p = 1 / (1 + math.Exp(z))
			}
			// The negative log-likelihood differentiates to (t - p) with respect
			// to z = a*score + b, because p is written as 1/(1+exp(z)) and so
			// dp/dz is -p(1-p) rather than +p(1-p). Getting that sign wrong turns
			// Newton's method into gradient ascent, which converges — to
			// certainty, in the wrong direction, silently.
			d := w * (t - p)
			g0 += d * s.Score
			g1 += d
			v := w * p * (1 - p)
			h00 += v * s.Score * s.Score
			h01 += v * s.Score
			h11 += v
		}
		// A small ridge keeps the Hessian invertible when every score is equal,
		// which is exactly the degenerate input a warming model produces.
		h00 += 1e-12
		h11 += 1e-12
		det := h00*h11 - h01*h01
		if det == 0 || math.IsNaN(det) {
			return 0, 0, ErrConverging
		}
		da := (h11*g0 - h01*g1) / det
		db := (h00*g1 - h01*g0) / det
		a -= da
		b -= db
		if math.Abs(da) < tolerance && math.Abs(db) < tolerance {
			if math.IsNaN(a) || math.IsNaN(b) {
				return 0, 0, ErrConverging
			}
			return round6(a), round6(b), nil
		}
	}
	if math.IsNaN(a) || math.IsNaN(b) {
		return 0, 0, ErrConverging
	}
	return round6(a), round6(b), nil
}

// brier is the mean squared error of a map against the labels it was fitted on.
// Lower is better; 0.25 is what a constant at the base rate of a balanced set
// scores, so a number above that on a balanced set means the map is worse than
// saying nothing.
func brier(samples []Sample, r Reader) float64 {
	if len(samples) == 0 || !r.Fitted() {
		return 0
	}
	var sum, w float64
	for _, s := range samples {
		p := r.P(s.Score)
		y := 0.0
		if s.Productive {
			y = 1
		}
		sum += s.weight() * (p - y) * (p - y)
		w += s.weight()
	}
	if w == 0 {
		return 0
	}
	return round6(sum / w)
}

// Bin is one bucket of a reliability report: what the map predicted here, and
// what actually happened.
type Bin struct {
	// From and To bound the predicted probability, half-open.
	From float64 `json:"from"`
	To   float64 `json:"to"`
	// Rows is how many observations fell in the bin, and Predicted and Observed
	// are the mean predicted probability and the realised productive share. A
	// calibrated map has them equal; the gap between them is the whole report.
	Rows      int      `json:"rows"`
	Predicted *float64 `json:"predicted,omitempty"`
	Observed  *float64 `json:"observed,omitempty"`
}

// Reliability is the calibration report a console renders as a diagonal: bins of
// predicted probability against the share that turned out productive.
//
// Predicted and Observed are POINTERS. An empty bin has no mean and no realised
// share, and reporting either as 0.0 would draw a point on the chart at perfect
// confidence in innocence — which is the most misleading thing this whole
// package could do.
//
// A reader that cannot answer gets NO CHART, not an empty one. A calibration
// whose shape has moved refuses to state a probability; a reliability report is
// that same map applied to every row, so shipping one beside the refusal would
// draw the chart the prose just said does not exist.
func Reliability(samples []Sample, r Reader, bins int) []Bin {
	if !r.Fitted() {
		return nil
	}
	if bins <= 0 {
		bins = 10
	}
	out := make([]Bin, bins)
	sumP := make([]float64, bins)
	sumY := make([]float64, bins)
	w := make([]float64, bins)
	for i := range out {
		out[i] = Bin{From: float64(i) / float64(bins), To: float64(i+1) / float64(bins)}
	}
	for _, s := range samples {
		p := r.P(s.Score)
		i := int(p * float64(bins))
		if i >= bins {
			i = bins - 1
		}
		if i < 0 {
			i = 0
		}
		y := 0.0
		if s.Productive {
			y = 1
		}
		out[i].Rows++
		sumP[i] += s.weight() * p
		sumY[i] += s.weight() * y
		w[i] += s.weight()
	}
	for i := range out {
		if w[i] == 0 {
			continue
		}
		p := round6(sumP[i] / w[i])
		y := round6(sumY[i] / w[i])
		out[i].Predicted, out[i].Observed = &p, &y
	}
	return out
}

// digest identifies a fit by its parameters, so two maps that answer identically
// have one identity and a map that has moved has another. It covers the shape it
// was fitted under, because the same knots under a different model are a
// different map.
func digest(m Map) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%s|%d|%d|", m.Method, m.Shape, m.Positive, m.Negative)
	fmt.Fprintf(h, "%s|%s|", strconv.FormatFloat(m.A, 'g', 17, 64), strconv.FormatFloat(m.B, 'g', 17, 64))
	for _, k := range m.Knots {
		fmt.Fprintf(h, "%s:%s|", strconv.FormatFloat(k.Score, 'g', 17, 64), strconv.FormatFloat(k.P, 'g', 17, 64))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// short renders a digest for a message. A whole SHA-256 in an error is noise a
// reader skips; twelve hex characters is enough to tell two apart.
func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func clamp01(v float64) float64 {
	switch {
	case math.IsNaN(v):
		return 0
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// round6 trims to six places. A probability is reported to a person and stored
// as a record; seventeen digits of float noise makes two identical fits look
// different and invites a caller to compare the fifteenth decimal.
func round6(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1e6) / 1e6
}
