package anomaly

import (
	"math"
	"math/rand/v2"
)

// A half-space tree is a binary tree over the unit cube built BEFORE any data
// arrives: at each node a dimension is drawn at random and split at the midpoint
// of that node's range. Nothing about the split depends on the stream, which is
// the property the whole design rests on — there is no training pass, no sample
// to retain, and no retraining job, so the model is a set of counters over a
// fixed geometry.
//
// Each node holds two masses: ref, how many points fell in this region during
// the reference window, and cur, how many are falling in it now. A point is
// scored against ref and learned into cur. When a window closes, cur folds into
// ref and a new window starts.
//
// Scoring descends the point's path and returns the mass of the deepest region
// reached, scaled by 2^depth. The scaling is what makes the number comparable
// across depths: halving a region halves its expected mass, so mass*2^depth is
// flat at uniform density and falls toward zero as the region empties. A point
// in a region emptier than uniform is the anomaly.
//
// Tan, Ting & Liu, "Fast Anomaly Detection for Streaming Data", IJCAI 2011.

// tree is one half-space tree, flattened into arrays indexed by heap position:
// node i has children 2i+1 and 2i+2. The flat layout costs no pointers, no
// allocation per node, and keeps a path walk inside a handful of cache lines.
type tree struct {
	// dim and split describe the geometry and never change after planting.
	dim   []uint8
	split []float64
	// ref is the mass of the reference window, which is what points are scored
	// against. cur is the mass accumulating in the window now open.
	ref []float64
	cur []float64
}

// plant grows a tree of the given depth over dims dimensions, drawing the
// geometry from rng and nothing else.
//
// The work range per dimension follows the paper: a random offset s in (0,1)
// with half-width 2*max(s, 1-s). That places the unit interval strictly inside
// the range, so no point can fall outside the tree, and it puts the root split
// in that dimension exactly at s — which is what makes two trees planted from
// different draws disagree, and disagreement is what a forest averages over.
func plant(rng *rand.Rand, dims, depth int) *tree {
	n := 1<<(depth+1) - 1
	t := &tree{
		dim:   make([]uint8, n),
		split: make([]float64, n),
		ref:   make([]float64, n),
		cur:   make([]float64, n),
	}
	lo := make([]float64, dims)
	hi := make([]float64, dims)
	for q := range lo {
		s := rng.Float64()
		w := 2 * math.Max(s, 1-s)
		lo[q], hi[q] = s-w, s+w
	}
	t.grow(rng, 0, 0, depth, lo, hi)
	return t
}

// grow fills node i and everything below it.
func (t *tree) grow(rng *rand.Rand, i, at, depth int, lo, hi []float64) {
	if at == depth {
		return // external node: no split to record
	}
	q := rng.IntN(len(lo))
	p := (lo[q] + hi[q]) / 2
	t.dim[i] = uint8(q)
	t.split[i] = p

	keep := hi[q]
	hi[q] = p
	t.grow(rng, 2*i+1, at+1, depth, lo, hi)
	hi[q] = keep

	keep = lo[q]
	lo[q] = p
	t.grow(rng, 2*i+2, at+1, depth, lo, hi)
	lo[q] = keep
}

// child returns the heap index of the child x descends into.
func (t *tree) child(i int, x []float64) int {
	if x[t.dim[i]] < t.split[i] {
		return 2*i + 1
	}
	return 2*i + 2
}

// learn adds one point to the open window, incrementing every region on its
// path. Cost is depth+1 increments and no allocation, which is what "O(1)
// amortised update" means in practice.
func (t *tree) learn(x []float64, depth int) {
	i := 0
	for at := 0; ; at++ {
		t.cur[i]++
		if at == depth {
			return
		}
		i = t.child(i, x)
	}
}

// mass scores x against the reference window: the mass of the deepest region
// reached, scaled by 2^depth.
//
// The descent stops early once a region's reference mass falls to limit, and
// that early stop is doing real work rather than saving time. Below a handful of
// points the mass of a region says nothing about its density — it is noise — and
// multiplying noise by 2^depth would amplify exactly the thing that should be
// ignored. Stopping puts the answer at the last depth where the count still
// meant something.
func (t *tree) mass(x []float64, depth int, limit float64) float64 {
	i := 0
	for at := 0; ; at++ {
		if at == depth || t.ref[i] <= limit {
			return t.ref[i] * float64(uint64(1)<<uint(at))
		}
		i = t.child(i, x)
	}
}

// roll closes the open window: the mass it accumulated folds into the reference
// at rate blend, and a new window starts empty.
//
// blend = 1 is the published algorithm, where the new window simply replaces the
// reference. Below 1 it is an exponential blend, and the default is below 1 for
// one reason: with a straight replacement, anyone who can submit transactions
// can make their own behaviour normal by sending one window's worth of it. A
// blend of b stretches the reference over roughly 1/b windows without costing a
// byte more memory, so the same attack needs sustained volume against the whole
// tenant rather than a burst. Adaptation to genuine drift is slower by the same
// factor, which is the trade being made and the reason the number is a
// parameter.
func (t *tree) roll(blend float64) {
	for i := range t.ref {
		t.ref[i] += blend * (t.cur[i] - t.ref[i])
		t.cur[i] = 0
	}
}

// sound reports whether the masses are usable: finite, non-negative, and
// internally consistent.
//
// The consistency check is the useful one. Every point increments each node on
// its path, so a parent's mass is exactly the sum of its children's, and the
// blend is linear so folding preserves that. Any mass array that does not
// satisfy it was not produced by this algorithm. That makes the invariant a
// cheap guard on restored state — not authentication, which the caller owes, but
// enough that a mass array cannot be bent into a shape that hides a region
// without the bend being visible.
func (t *tree) sound(depth int) bool {
	internal := 1<<depth - 1
	for i := range t.ref {
		if !finite(t.ref[i]) || t.ref[i] < 0 || !finite(t.cur[i]) || t.cur[i] < 0 {
			return false
		}
		if i < internal {
			if !within(t.ref[i], t.ref[2*i+1]+t.ref[2*i+2]) {
				return false
			}
			if !within(t.cur[i], t.cur[2*i+1]+t.cur[2*i+2]) {
				return false
			}
		}
	}
	return true
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// within compares two masses that arithmetic should have made equal, allowing
// for the rounding a long chain of blends accumulates.
func within(a, b float64) bool {
	d := math.Abs(a - b)
	return d <= 1e-6*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}
