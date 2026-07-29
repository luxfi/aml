package measure

import (
	"math"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/history"
)

// at builds an event n hours before a fixed reference instant.
func at(hoursAgo int, usd float64, dir string) history.Event {
	ref := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return history.Event{
		At:        ref.Add(-time.Duration(hoursAgo) * time.Hour),
		USD:       usd,
		Direction: dir,
	}
}

func TestStructuredDetectsSplitDeposits(t *testing.T) {
	// Five deposits of 2,500 total 12,500 with no single one reportable.
	// This is the case a point-in-time threshold check cannot see.
	evs := []history.Event{
		at(1, 2500, history.In), at(3, 2500, history.In), at(5, 2500, history.In),
		at(7, 2500, history.In), at(9, 2500, history.In),
	}
	if !Structured(evs, 10000, 2) {
		t.Fatal("five sub-threshold deposits totalling 12,500 must report as structured")
	}
}

func TestStructuredIgnoresReportedTransaction(t *testing.T) {
	// A single 12,000 transaction is reportable and was reported. Aggregating it
	// into a structuring finding would accuse the customer of evading a
	// requirement the transaction actually triggered.
	evs := []history.Event{at(1, 12000, history.In)}
	if Structured(evs, 10000, 2) {
		t.Fatal("one above-threshold transaction is a filing obligation, not structuring")
	}
}

func TestStructuredExcludesReportedAmountFromTheAggregate(t *testing.T) {
	// One reported 12,000 alongside two small deposits. The reported amount must
	// not be counted towards the evaded total: on its own the sub-threshold
	// activity is 2,000, nowhere near the threshold. Folding the reported 12,000
	// back in would manufacture a structuring finding out of a transaction that
	// was declared.
	evs := []history.Event{
		at(1, 12000, history.In), at(3, 1000, history.In), at(5, 1000, history.In),
	}
	if Structured(evs, 10000, 2) {
		t.Fatal("a declared transaction must not be counted towards an evaded aggregate")
	}
}

func TestStructuredRejectsUnconfiguredThreshold(t *testing.T) {
	// The realistic failure: an operator adds a structuring rule and leaves the
	// threshold and minimum count unset, so both arrive as zero. With no guard,
	// nothing is sub-threshold so the count is zero, the aggregate is zero, and
	// both "0 >= 0" tests pass — the rule reports every transaction ever seen,
	// including an empty window. An unconfigured control must detect nothing, not
	// everything.
	evs := []history.Event{at(1, 5000, history.In), at(2, 5000, history.In)}
	if Structured(evs, 0, 0) {
		t.Fatal("an unconfigured threshold must report nothing, not everything")
	}
	if Structured(nil, 0, 0) {
		t.Fatal("an unconfigured threshold over an empty window must report nothing")
	}
	if Structured(evs, -1, 2) {
		t.Fatal("a negative threshold must report nothing")
	}
}

func TestStructuredRequiresAggregateToReachThreshold(t *testing.T) {
	// Two small deposits that do not add up to the threshold are ordinary.
	evs := []history.Event{at(1, 500, history.In), at(2, 400, history.In)}
	if Structured(evs, 10000, 2) {
		t.Fatal("sub-threshold deposits below the threshold in aggregate must not report")
	}
}

func TestStructuredRespectsMinimumCount(t *testing.T) {
	evs := []history.Event{at(1, 9000, history.In), at(2, 9000, history.In)}
	if !Structured(evs, 10000, 2) {
		t.Fatal("two sub-threshold deposits over the threshold in aggregate must report at min=2")
	}
	if Structured(evs, 10000, 3) {
		t.Fatal("min=3 must suppress a two-transaction pattern")
	}
}

func TestStructuredCannotFireOnOneTransaction(t *testing.T) {
	// Even asking for min=1, one transaction below the threshold cannot reach the
	// threshold, so the aggregate test alone rules out single-transaction
	// structuring. This is why no separate floor on min is needed.
	evs := []history.Event{at(1, 9999, history.In)}
	if Structured(evs, 10000, 1) {
		t.Fatal("a single sub-threshold transaction can never aggregate to the threshold")
	}
}

func TestNearBand(t *testing.T) {
	if !Near(9500, 10000, 0.1) {
		t.Fatal("9,500 is inside the 10 percent band below 10,000")
	}
	if Near(8000, 10000, 0.1) {
		t.Fatal("8,000 is outside the 10 percent band below 10,000")
	}
	if Near(10000, 10000, 0.1) {
		t.Fatal("the threshold itself is reportable, not near")
	}
}

func TestNearRejectsBandWiderThanTheThreshold(t *testing.T) {
	// A band above 1 puts the lower bound below zero, which would report every
	// amount under the threshold as sitting just beneath it — the indicator would
	// fire on a 1-dollar payment.
	if Near(1, 10000, 2) {
		t.Fatal("a band wider than the threshold must be rejected, not treated as unbounded")
	}
	if Near(9500, 10000, 1.0001) {
		t.Fatal("a band above 1 must be rejected")
	}
}

func TestDayAggregatesCalendarNotRolling(t *testing.T) {
	// 6,000 at 23:00 on the 9th and 6,000 at 01:00 on the 10th are two hours
	// apart but fall in different calendar days. A rolling 24-hour sum reports
	// 12,000 and would file a currency report; the calendar-day total for the
	// 10th is 6,000 and no report is due.
	evs := []history.Event{
		{At: time.Date(2026, 3, 9, 23, 0, 0, 0, time.UTC), USD: 6000},
		{At: time.Date(2026, 3, 10, 1, 0, 0, 0, time.UTC), USD: 6000},
	}
	if got := Sum(evs); got != 12000 {
		t.Fatalf("rolling sum = %v, want 12000", got)
	}
	when := time.Date(2026, 3, 10, 1, 0, 0, 0, time.UTC)
	if got := Day(evs, time.UTC, when); got != 6000 {
		t.Fatalf("calendar day total = %v, want 6000 — the two deposits are on different days", got)
	}
}

func TestDayTotalsOnlyTheNamedDay(t *testing.T) {
	evs := []history.Event{
		{At: time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC), USD: 4000},
		{At: time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC), USD: 7000},
		{At: time.Date(2026, 3, 9, 14, 0, 0, 0, time.UTC), USD: 5000},
	}
	when := time.Date(2026, 3, 9, 14, 0, 0, 0, time.UTC)
	if got := Day(evs, time.UTC, when); got != 12000 {
		t.Fatalf("total for 9 March = %v, want 12000", got)
	}
	when = time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	if got := Day(evs, time.UTC, when); got != 4000 {
		t.Fatalf("total for 8 March = %v, want 4000 — other days must not be counted", got)
	}
}

func TestDayHonoursLocation(t *testing.T) {
	// 22:00 and 02:00 UTC straddle midnight in UTC but are the same calendar day
	// in a UTC-5 zone. The business day belongs to the institution, not the
	// server, and getting this wrong moves a reporting obligation by a day.
	evs := []history.Event{
		{At: time.Date(2026, 3, 9, 22, 0, 0, 0, time.UTC), USD: 6000},
		{At: time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC), USD: 6000},
	}
	when := time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)
	if got := Day(evs, time.UTC, when); got != 6000 {
		t.Fatalf("UTC day total = %v, want 6000", got)
	}
	west := time.FixedZone("UTC-5", -5*3600)
	if got := Day(evs, west, when); got != 12000 {
		t.Fatalf("UTC-5 day total = %v, want 12000 — both fall on 9 March locally", got)
	}
}

func TestDayDefaultsToUTC(t *testing.T) {
	evs := []history.Event{{At: time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC), USD: 5000}}
	when := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	if got := Day(evs, nil, when); got != 5000 {
		t.Fatalf("nil location must fall back to UTC, got %v", got)
	}
}

func TestInOutDetectsPassThrough(t *testing.T) {
	evs := []history.Event{at(2, 50000, history.In), at(1, 49000, history.Out)}
	if !InOut(evs, 10000, 0.05) {
		t.Fatal("50,000 in and 49,000 out retains 2 percent and must report as pass-through")
	}
}

func TestInOutIgnoresAccumulation(t *testing.T) {
	// Money in with nothing going out is accumulation, not layering.
	evs := []history.Event{at(2, 50000, history.In), at(1, 40000, history.In)}
	if InOut(evs, 10000, 0.05) {
		t.Fatal("deposits with no withdrawal must not report as pass-through")
	}
}

func TestInOutIgnoresRetainedFunds(t *testing.T) {
	// Half the money stayed. That is a customer using an account.
	evs := []history.Event{at(2, 50000, history.In), at(1, 25000, history.Out)}
	if InOut(evs, 10000, 0.05) {
		t.Fatal("retaining half the inflow must not report as pass-through")
	}
}

func TestInOutIgnoresSmallAmounts(t *testing.T) {
	evs := []history.Event{at(2, 100, history.In), at(1, 99, history.Out)}
	if InOut(evs, 10000, 0.05) {
		t.Fatal("amounts below the minimum must not report")
	}
}

func TestDormancyMeasuresTheGapBroken(t *testing.T) {
	evs := []history.Event{at(0, 40000, history.In), at(24*200, 100, history.In)}
	got := Dormancy(evs)
	if want := 200 * 24 * time.Hour; got != want {
		t.Fatalf("dormancy = %v, want %v", got, want)
	}
}

func TestDormancyNeedsTwoEvents(t *testing.T) {
	if got := Dormancy([]history.Event{at(0, 100, history.In)}); got != 0 {
		t.Fatalf("dormancy with one event = %v, want 0 — a gap needs two ends", got)
	}
}

func TestDormancyDoesNotReorderCallerSlice(t *testing.T) {
	evs := []history.Event{at(0, 1, history.In), at(100, 2, history.In)}
	first := evs[0].USD
	Dormancy(evs)
	if evs[0].USD != first {
		t.Fatal("Dormancy must not reorder the caller's slice")
	}
}

func TestRoundFraction(t *testing.T) {
	evs := []history.Event{
		{USD: 10000}, {USD: 25000}, {USD: 5000}, {USD: 4237.19},
	}
	if got := Round(evs, 1000); got != 0.75 {
		t.Fatalf("round fraction = %v, want 0.75", got)
	}
	if got := Round(evs, 0); got != 0 {
		t.Fatalf("a non-positive unit must return 0, got %v", got)
	}
	if got := Round(nil, 1000); got != 0 {
		t.Fatalf("an empty window must return 0, got %v", got)
	}
}

func TestDeviationIsUnmovedByOneOutlier(t *testing.T) {
	// A customer whose ordinary transaction is about 100, with one past
	// 1,000,000. A mean-and-standard-deviation baseline is destroyed by that
	// outlier and scores the next 50,000 as unremarkable. The median absolute
	// deviation is not.
	evs := []history.Event{
		{USD: 100}, {USD: 105}, {USD: 95}, {USD: 102}, {USD: 98},
		{USD: 1_000_000},
	}
	z := Deviation(evs, 50000, 4)
	if z < 3.5 {
		t.Fatalf("robust deviation of 50,000 = %v, want >= 3.5 despite the outlier in the baseline", z)
	}

	// Show the classical statistic fails on the same data, which is why it is
	// not the one used.
	if classical := zMeanStdDev(evs, 50000); math.Abs(classical) >= 3.5 {
		t.Fatalf("mean/stddev z = %v; the test premise requires it to miss (< 3.5)", classical)
	}
}

func TestDeviationHasNoOpinionOnSmallWindows(t *testing.T) {
	// The baseline has genuine spread, so the score would be enormous if it were
	// computed at all. Three events is still too thin a baseline to accuse anyone
	// on, and the measure must decline rather than produce a confident number
	// from almost no data.
	evs := []history.Event{{USD: 100}, {USD: 105}, {USD: 95}}
	if z := Deviation(evs, 999999, 4); z != 0 {
		t.Fatalf("deviation over 3 events with min 4 = %v, want 0 — too little baseline to judge", z)
	}
	// Adding a fourth event of the same shape reaches the minimum and the measure
	// then does have an opinion, which proves the gate is the window size and not
	// something else about the data.
	evs = append(evs, history.Event{USD: 102})
	if z := Deviation(evs, 999999, 4); z == 0 {
		t.Fatal("at the minimum window the measure must produce a score")
	}
}

func TestDeviationHasNoOpinionWithoutSpread(t *testing.T) {
	// Every past amount identical: the median absolute deviation is zero and the
	// score would divide by zero. No opinion is the honest answer.
	evs := []history.Event{{USD: 100}, {USD: 100}, {USD: 100}, {USD: 100}, {USD: 100}}
	if z := Deviation(evs, 100000, 4); z != 0 {
		t.Fatalf("deviation with zero spread = %v, want 0", z)
	}
}

// zMeanStdDev is the classical z-score, present only so the test above can
// demonstrate the failure it avoids.
func zMeanStdDev(evs []history.Event, value float64) float64 {
	if len(evs) < 2 {
		return 0
	}
	var mean float64
	for _, e := range evs {
		mean += e.USD
	}
	mean /= float64(len(evs))
	var ss float64
	for _, e := range evs {
		ss += (e.USD - mean) * (e.USD - mean)
	}
	sd := math.Sqrt(ss / float64(len(evs)-1))
	if sd == 0 {
		return 0
	}
	return (value - mean) / sd
}

func TestDistinctCountsParties(t *testing.T) {
	evs := []history.Event{
		{Counterparty: "a"}, {Counterparty: "b"}, {Counterparty: "a"}, {Counterparty: ""},
	}
	if got := Distinct(evs, func(e history.Event) string { return e.Counterparty }); got != 2 {
		t.Fatalf("distinct counterparties = %d, want 2 (empty values are not a party)", got)
	}
}

func TestSumAndCountAndMax(t *testing.T) {
	evs := []history.Event{{USD: 10}, {USD: 30}, {USD: 20}}
	if got := Sum(evs); got != 60 {
		t.Fatalf("sum = %v, want 60", got)
	}
	if got := Count(evs); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
	if got := Max(evs); got != 30 {
		t.Fatalf("max = %v, want 30", got)
	}
}
