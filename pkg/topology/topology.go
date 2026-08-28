// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package topology searches the space of model shapes over a tenant's own
// history, and reports what each one would have done.
//
// A detector has a shape — how many trees, how deep, how long a reference window
// is, how fast the reference folds, and what share of the stream the appetite
// admits — and the shape that suits one institution's traffic does not suit
// another's. Picking it by hand is picking it by taste. This package picks it by
// replaying the institution's own events through the SAME detector production
// runs, one candidate at a time, and reporting the learning curve, the realised
// alert share against the stated one, and where each candidate went blind.
//
// # Dry run, structurally
//
// This package has no store, writes nothing, and imports nothing that has one.
// It reaches history through replay.History, whose one method iterates, and it
// scores through the anomaly package's own model rather than a copy of the
// algorithm — the same argument pkg/replay makes for rules, made again for the
// model. A trial builds its own detector and its own aggregate store, uses them,
// and drops them; a candidate cannot touch what a tenant is being scored by.
//
// # It refuses to name a winner it cannot justify
//
// Ranking topologies needs an outcome to rank against, and the only honest one is
// whether a candidate separates the events a human judged suspicious from the
// events a human dismissed. Where nothing has been judged, this package reports
// every trial and NO winner, with the reason. A ranking invented from unlabelled
// data — most alerts, fewest alerts, best-behaved threshold — would look like a
// recommendation and would be a preference, and an institution that changed its
// monitoring on the strength of one could not say why it did.
//
// # What a winning shape can and cannot be adopted into
//
// Learned state is only meaningful against the shape that produced it, and
// anomaly.Digest is what enforces that: a snapshot fitted under one shape cannot
// be restored into a store running another. So Fit's snapshot is adoptable only
// where the running detector already has that shape, and Trial.Digest is what a
// caller compares against the live model's. That is a governance property rather
// than a limitation — a tenant's model shape cannot be swapped underneath it by
// restoring a file.
package topology

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
)

// Errors.
var (
	ErrEmptySpace = errors.New("topology: the space is empty, so there is nothing to try")
	ErrHuge       = errors.New("topology: the space holds more candidates than one search may run")
	ErrNoHistory  = errors.New("topology: no history")
	ErrEmpty      = errors.New("topology: history is empty, so a search proves nothing")
	ErrShape      = errors.New("topology: candidate shape refused")
	ErrOrg        = errors.New("topology: no tenant, so the geometry would not be the tenant's")
)

// MaxTrials bounds one search.
//
// A grid is a product, so four values on each of five axes is already a thousand
// candidates and each one replays the whole history. The bound is refused at the
// door rather than absorbed, because a search that silently ran a subset of the
// grid reports a winner chosen from candidates the caller cannot name.
const MaxTrials = 256

// MaxEvents bounds how much history one trial replays, and MaxNodes bounds how
// large a candidate's detector may be — Trees × nodes, which is what the fitted
// state costs to carry.
const (
	MaxEvents = 50_000
	MaxNodes  = 262_144
)

// Refusals a search reports instead of a winner.
const (
	// RefusalUnjudged means no event carried a disposition, so no candidate can
	// be shown to separate anything.
	RefusalUnjudged = "no event has been judged, so no shape can be shown to separate suspicious activity from ordinary activity"
	// RefusalWarming means every candidate was still warming at the end of the
	// history: the history is too short for the shapes asked for.
	RefusalWarming = "every candidate was still warming when the history ran out, so none of them scored anything"
	// RefusalSaturated means every warm candidate was saturated — too much of the
	// stream scored in the top bucket for any threshold to honour the appetite.
	RefusalSaturated = "every warm candidate was saturated, so none of them could honour the appetite asked for"
)

// Topology is one candidate shape of the detector.
type Topology struct {
	Trees  int     `json:"trees"`
	Depth  int     `json:"depth"`
	Window int     `json:"window"`
	Blend  float64 `json:"blend"`
	Review float64 `json:"review"`
}

// MaxDepth is the deepest tree a candidate may ask for.
//
// It exists because the node count is exponential in the depth and a shift wide
// enough to overflow does not error — it WRAPS, and a wrapped node count is
// negative, which passes a "too large" bound and then allocates until the process
// dies. The ceiling is checked before the arithmetic, and Nodes saturates rather
// than wrapping, so neither of them can be the one that is wrong.
const MaxDepth = 24

// Nodes is how many mass counters one candidate carries per reference, which is
// what bounds both its memory and the size of a snapshot fitted from it.
//
// It saturates instead of overflowing: an unrepresentable count is reported as
// the largest one, which is above every bound and is therefore refused.
func (t Topology) Nodes() int {
	if t.Depth < 0 || t.Depth > MaxDepth || t.Trees <= 0 {
		return math.MaxInt32
	}
	per := 1<<(t.Depth+1) - 1
	if t.Trees > math.MaxInt32/per {
		return math.MaxInt32
	}
	return t.Trees * per
}

// Valid reports why a candidate cannot be built, if it cannot. The bounds are
// the detector's own — anomaly.Config would silently substitute a default for a
// value outside them, and a trial reported under a shape it did not run is worse
// than a refused one.
func (t Topology) Valid() error {
	switch {
	case t.Trees <= 0 || t.Depth <= 0 || t.Window <= 0:
		return fmt.Errorf("%w: trees %d, depth %d, window %d must all be positive", ErrShape, t.Trees, t.Depth, t.Window)
	case t.Blend <= 0 || t.Blend > 1:
		return fmt.Errorf("%w: blend %v is outside (0,1]", ErrShape, t.Blend)
	case t.Review <= 0 || t.Review > 0.5:
		return fmt.Errorf("%w: review %v is outside (0,0.5]", ErrShape, t.Review)
	case t.Depth > MaxDepth:
		return fmt.Errorf("%w: depth %d, at most %d", ErrShape, t.Depth, MaxDepth)
	case t.Nodes() > MaxNodes:
		return fmt.Errorf("%w: %d nodes, at most %d", ErrShape, t.Nodes(), MaxNodes)
	}
	return nil
}

// Config is the candidate as the detector takes it. One conversion, here, so a
// trial cannot run a shape the report does not name.
func (t Topology) Config(seed uint64) anomaly.Config {
	return anomaly.Config{
		Trees: t.Trees, Depth: t.Depth, Window: t.Window, Blend: t.Blend,
		Appetite: anomaly.Appetite{Review: t.Review},
		// Not shadow: a trial exists to find out what the candidate WOULD alert
		// on, and a shadow model reports no alerts by construction.
		Shadow: false,
		Seed:   seed,
	}
}

// Space is the grid to search. An empty axis takes the detector's own default for
// that axis, so a caller may search one dimension without restating the others.
type Space struct {
	Trees  []int     `json:"trees,omitempty"`
	Depth  []int     `json:"depth,omitempty"`
	Window []int     `json:"window,omitempty"`
	Blend  []float64 `json:"blend,omitempty"`
	Review []float64 `json:"review,omitempty"`
}

// Grid enumerates the space, in a fixed order so two runs of the same space
// produce the same report in the same sequence.
func (s Space) Grid() ([]Topology, error) {
	trees := ints(s.Trees, 25)
	depth := ints(s.Depth, 8)
	window := ints(s.Window, 256)
	blend := floats(s.Blend, 0.25)
	review := floats(s.Review, 0.01)

	n := len(trees) * len(depth) * len(window) * len(blend) * len(review)
	if n == 0 {
		return nil, ErrEmptySpace
	}
	if n > MaxTrials {
		return nil, fmt.Errorf("%w: %d candidates, at most %d", ErrHuge, n, MaxTrials)
	}
	out := make([]Topology, 0, n)
	for _, tr := range trees {
		for _, d := range depth {
			for _, w := range window {
				for _, b := range blend {
					for _, r := range review {
						t := Topology{Trees: tr, Depth: d, Window: w, Blend: b, Review: r}
						if err := t.Valid(); err != nil {
							return nil, err
						}
						out = append(out, t)
					}
				}
			}
		}
	}
	return out, nil
}

func ints(v []int, fallback int) []int {
	if len(v) == 0 {
		return []int{fallback}
	}
	out := append([]int(nil), v...)
	sort.Ints(out)
	return out
}

func floats(v []float64, fallback float64) []float64 {
	if len(v) == 0 {
		return []float64{fallback}
	}
	out := append([]float64(nil), v...)
	sort.Float64s(out)
	return out
}

// Point is one place on the learning curve: what the model looked like after this
// many events.
//
// The curve is what makes "the history is too short" visible. A candidate whose
// realised share is still climbing at the last point has not settled, and its
// final number is a snapshot of a moving thing rather than a property of the
// shape.
type Point struct {
	Learned int64   `json:"learned"`
	Cut     float64 `json:"cut"`
	Scored  int64   `json:"scored"`
	Alerted int64   `json:"alerted"`
	// Realised is the share of scored events that alerted, up to this point.
	Realised float64 `json:"realised"`
	// Mean is the mean score of the events scored since the previous point, which
	// is what shows the reference settling.
	Mean float64 `json:"mean"`
}

// Contribution is one feature's part of what a candidate alerted on, summed over
// the run. It is the feature-side view of the topology: which dimensions carried
// the signal for this institution.
type Contribution struct {
	Feature string `json:"feature"`
	// Alerts is how many alerts this feature appeared in as a cause, and Share is
	// the mean of its share across those alerts.
	Alerts int64   `json:"alerts"`
	Share  float64 `json:"share"`
}

// Trial is one candidate's whole result.
type Trial struct {
	Topology Topology `json:"topology"`
	Digest   string   `json:"digest"`
	Seed     uint64   `json:"seed"`

	Events  int   `json:"events"`
	Scored  int64 `json:"scored"`
	Alerted int64 `json:"alerted"`

	// Stated is the appetite asked for and Realised is what the candidate
	// actually did. Drift is the distance between them, which is the governance
	// number: an appetite nobody honours is not a control.
	Stated   float64 `json:"stated"`
	Realised float64 `json:"realised"`
	Drift    float64 `json:"drift"`

	Warm      bool `json:"warm"`
	Saturated bool `json:"saturated"`

	Refused map[string]int64 `json:"refused"`
	Blind   map[string]int64 `json:"blind"`

	Curve    []Point        `json:"curve"`
	Features []Contribution `json:"features"`

	// Separation is the area under the ROC curve of this candidate's scores
	// against the dispositions a human recorded: 1 is perfect separation, 0.5 is
	// none. It is ABSENT and not zero when nothing was judged, because a zero
	// reads as a candidate that got everything backwards.
	Separation *float64 `json:"separation,omitempty"`
	// Judged is how many of the replayed events carried a disposition, which is
	// what Separation was computed from.
	Judged int `json:"judged"`
}

// Report is a whole search.
type Report struct {
	Events int       `json:"events"`
	From   time.Time `json:"from,omitzero"`
	To     time.Time `json:"to,omitzero"`
	Judged int       `json:"judged"`
	// Cut is true when MaxEvents or the caller's own bound stopped the replay
	// before the history ended.
	Cut bool `json:"cut,omitempty"`
	// Seed the search ran with. Reported so the run can be reproduced exactly by
	// supplying it back, which is what makes a recommendation checkable.
	Seed uint64 `json:"seed"`

	Trials []Trial `json:"trials"`
	// Winner is the shape this search recommends, and Refusal is why there is
	// none. Exactly one of them is set.
	Winner  *Trial        `json:"winner,omitempty"`
	Refusal string        `json:"refusal,omitempty"`
	Elapsed time.Duration `json:"elapsed"`
}

// Options parameterise a search.
type Options struct {
	// Curve is how many points the learning curve carries. Zero takes the
	// default; the curve is sampled evenly over the replay.
	Curve int `json:"curve,omitempty"`
	// Events bounds the replay. Zero takes MaxEvents; a larger value is clamped
	// to it and the report says the replay was cut.
	Events int `json:"events,omitempty"`
	// Workers is how many candidates run at once. Zero takes the default.
	Workers int `json:"workers,omitempty"`
	// Seed fixes every candidate's geometry, so two searches over one space and
	// one history produce the same report. Zero draws one and reports it.
	Seed uint64 `json:"seed,omitempty"`
	// Limit is the reporting limit the aggregates are kept against, which is what
	// makes a payment sitting just under it visible as structuring. Zero takes
	// the same fallback the live path uses.
	Limit float64 `json:"limit,omitempty"`
}

// DefaultCurve is how many points a learning curve carries when the caller names
// no number, and DefaultWorkers how many candidates run at once.
const (
	DefaultCurve   = 32
	DefaultWorkers = 4
)

// MaxWorkers is the widest one study may run, however many the caller asks for.
//
// Workers is a caller-supplied number on the wire, and the grid holds up to
// MaxTrials candidates, so without a ceiling one request names 256 goroutines
// each replaying fifty thousand events. The ceiling is a bound on ONE study; the
// bound across every study at once is Budget, which is what keeps the rest of the
// machine for ingest.
const MaxWorkers = 4

// auc is the area under the ROC curve of scores against a binary label, by the
// rank-sum identity: AUC = (R₁ − n₁(n₁+1)/2) / (n₁n₀), where R₁ is the sum of the
// positives' ranks.
//
// Ties take the mean rank, which is what keeps a candidate that gives every event
// the same score at exactly 0.5 rather than at 1 or 0 depending on sort order — a
// detector that says nothing must not be able to win a search.
func auc(scores []float64, positive []bool) (float64, bool) {
	n := len(scores)
	if n != len(positive) || n == 0 {
		return 0, false
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] < scores[idx[b]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j+1 < n && scores[idx[j+1]] == scores[idx[i]] {
			j++
		}
		mean := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			ranks[idx[k]] = mean
		}
		i = j + 1
	}

	var n1, n0 int
	var r1 float64
	for i, p := range positive {
		if p {
			n1++
			r1 += ranks[i]
		} else {
			n0++
		}
	}
	if n1 == 0 || n0 == 0 {
		return 0, false
	}
	return (r1 - float64(n1)*float64(n1+1)/2) / (float64(n1) * float64(n0)), true
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
