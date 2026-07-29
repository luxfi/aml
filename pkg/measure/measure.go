// Package measure computes behavioural measures over a window of events.
//
// Every function here is pure: it takes the events and returns a number or a
// boolean. Nothing reaches a database, a clock, or a network. That is what makes
// the detection logic testable at the level a reviewer cares about — "given
// these five deposits, does structuring fire?" — instead of only through the
// storage layer.
package measure

import (
	"math"
	"sort"
	"time"

	"github.com/luxfi/aml/pkg/history"
)

// Count is the number of events in the window.
func Count(evs []history.Event) int { return len(evs) }

// Sum is the total USD value of the events in the window.
func Sum(evs []history.Event) float64 {
	var t float64
	for _, e := range evs {
		t += e.USD
	}
	return t
}

// Max is the largest single USD value in the window, or zero if empty.
func Max(evs []history.Event) float64 {
	var m float64
	for _, e := range evs {
		if e.USD > m {
			m = e.USD
		}
	}
	return m
}

// Distinct counts the distinct non-empty values of a key over the window.
// Passing a counterparty key answers "how many different parties", which
// separates one large relationship from a fan-out across many.
func Distinct(evs []history.Event, key func(history.Event) string) int {
	seen := make(map[string]struct{}, len(evs))
	for _, e := range evs {
		if v := key(e); v != "" {
			seen[v] = struct{}{}
		}
	}
	return len(seen)
}

// Sum of the events in one direction.
func SumDirection(evs []history.Event, dir string) float64 {
	var t float64
	for _, e := range evs {
		if e.Direction == dir {
			t += e.USD
		}
	}
	return t
}

// Structured reports whether the window holds a set of transactions each below
// the reporting threshold that together reach or exceed it.
//
// This is the detectable signature of the conduct prohibited by 31 USC 5324 and
// 31 CFR 1010.314: breaking an amount that would be reportable into several
// amounts that individually are not. The predicate deliberately requires at
// least two sub-threshold transactions — a single reportable transaction is a
// filing obligation, not structuring — and ignores transactions at or above the
// threshold, because those were reported and so were not evaded.
//
// min is the count of sub-threshold transactions required before the pattern is
// reported, so an institution can tune away the noise of ordinary repeat
// activity without changing the definition. Values below 2 have no effect: one
// transaction below the threshold cannot by itself reach the threshold, so the
// aggregate test already excludes it.
func Structured(evs []history.Event, threshold float64, min int) bool {
	if threshold <= 0 {
		return false
	}
	var n int
	var total float64
	for _, e := range evs {
		if e.USD > 0 && e.USD < threshold {
			n++
			total += e.USD
		}
	}
	return n >= min && total >= threshold
}

// Near reports whether a value sits in the band immediately below a threshold.
// Amounts just under a reporting threshold are a published red-flag indicator
// in their own right, independent of whether they aggregate over the threshold.
// band is a fraction of the threshold: 0.1 covers 9,000-10,000 at 10,000. A band
// wider than the whole threshold is rejected rather than clamped, because it
// would silently turn the indicator into "any amount below the threshold".
func Near(value, threshold, band float64) bool {
	if band <= 0 || band > 1 {
		return false
	}
	return value >= threshold*(1-band) && value < threshold
}

// Day totals the events falling on the same calendar day as when, in loc.
//
// A rolling lookback and a calendar day are different questions, and a currency
// reporting threshold asks the calendar one: 31 CFR 1010.311 aggregates multiple
// currency transactions totalling more than the threshold in one business day. A
// 24-hour rolling window spans two business days, so it both invents aggregates
// that no single day contains and misses same-day activity that has already
// fallen out of the window. loc is the institution's business day, which is not
// necessarily the server's.
func Day(evs []history.Event, loc *time.Location, when time.Time) float64 {
	if loc == nil {
		loc = time.UTC
	}
	day := when.In(loc).Format(time.DateOnly)
	var t float64
	for _, e := range evs {
		if e.At.In(loc).Format(time.DateOnly) == day {
			t += e.USD
		}
	}
	return t
}

// InOut reports whether value arrived and left again within the window, keeping
// no more than residue behind.
//
// Funds passing straight through an account is the layering signature described
// in FATF typology work: the account is a conduit, not a store of value. The
// test is that both directions are present, that the smaller of the two sides
// is at least min, and that the retained fraction is no greater than residue.
func InOut(evs []history.Event, min, residue float64) bool {
	in := SumDirection(evs, history.In)
	out := SumDirection(evs, history.Out)
	if in < min || out < min {
		return false
	}
	kept := math.Abs(in-out) / math.Max(in, out)
	return kept <= residue
}

// Dormancy is the time between the two most recent events in the window, which
// is the gap the newest event broke. It returns zero when the window holds
// fewer than two events, because a gap needs two ends.
//
// A long-idle account that suddenly transacts is a published indicator; the
// measure reports the gap and leaves the threshold to the rule.
func Dormancy(evs []history.Event) time.Duration {
	if len(evs) < 2 {
		return 0
	}
	at := make([]time.Time, 0, len(evs))
	for _, e := range evs {
		at = append(at, e.At)
	}
	sort.Slice(at, func(i, j int) bool { return at[i].After(at[j]) })
	return at[0].Sub(at[1])
}

// Round reports the fraction of the window's events whose value is an exact
// multiple of unit. Genuine commercial amounts carry odd cents and odd units;
// a book of perfectly round amounts is a published indicator of invented
// invoicing and of value moved for its own sake rather than for a trade.
func Round(evs []history.Event, unit float64) float64 {
	if unit <= 0 || len(evs) == 0 {
		return 0
	}
	var n int
	for _, e := range evs {
		if e.USD > 0 && math.Mod(e.USD, unit) == 0 {
			n++
		}
	}
	return float64(n) / float64(len(evs))
}

// Deviation is the robust modified z-score of value against the window as
// baseline: 0.6745 * (value - median) / medianAbsoluteDeviation.
//
// The median and the median absolute deviation are used rather than the mean and
// the standard deviation because a monitoring baseline is built from data that
// already contains the outliers being looked for. One large past transaction
// inflates a standard deviation enough to hide the next one, whereas the median
// absolute deviation is unmoved by up to half the sample. The 0.6745 constant
// scales the median absolute deviation onto the standard-deviation scale for a
// normal distribution, so a threshold of 3.5 carries its conventional meaning.
//
// Deviation returns zero when the window is too small to carry a baseline, or
// when the baseline has no spread at all. Both cases mean "no opinion", and a
// measure with no opinion must not manufacture one.
func Deviation(evs []history.Event, value float64, min int) float64 {
	if min < 4 {
		min = 4
	}
	if len(evs) < min {
		return 0
	}
	vals := make([]float64, 0, len(evs))
	for _, e := range evs {
		vals = append(vals, e.USD)
	}
	med := median(vals)

	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - med)
	}
	mad := median(dev)
	if mad == 0 {
		return 0
	}
	return 0.6745 * (value - med) / mad
}

// median returns the median of vals. It sorts a copy, because a measure must
// not reorder its caller's slice.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := make([]float64, len(vals))
	copy(s, vals)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}
