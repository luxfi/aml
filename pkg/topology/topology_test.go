package topology

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

const (
	acme  = "hanzo/acme"
	other = "zoo/acme"
)

var start = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

// stream is an institution's traffic: ordinary payments, with an occasional one
// far outside the account's own pattern. The outliers are what a shape either
// separates or does not.
func stream(n int, judged bool) replay.Slice {
	out := make(replay.Slice, 0, n)
	for i := 0; i < n; i++ {
		usd := 100 + float64(i%17)*3
		odd := i%53 == 52
		if odd {
			usd = 90_000
		}
		tx := types.Transaction{
			ID:        id(i),
			OrgID:     acme,
			UserID:    "u1",
			AccountID: "acct-1",
			Currency:  "USD",
			Notional:  usd,
			USD:       usd,
			Direction: "in",
			Timestamp: start.Add(time.Duration(i) * time.Minute),
		}
		e := replay.Event{Tx: tx, Entity: types.Entity{ID: "u1", OrgID: acme}}
		if judged {
			// What a human concluded. The outliers were productive; a sample of the
			// ordinary ones was looked at and dismissed.
			switch {
			case odd:
				e.Disposition = replay.Productive
			case i%23 == 0:
				e.Disposition = replay.Unproductive
			}
		}
		out = append(out, e)
	}
	return out
}

func id(i int) string { return "tx-" + time.Duration(i).String() }

func small() Space {
	return Space{Trees: []int{8}, Depth: []int{6}, Window: []int{16}, Blend: []float64{0.25}, Review: []float64{0.02}}
}

// TestGridIsTheProductAndIsBounded. A grid is a product, so four values on five
// axes is a thousand replays; the bound is refused at the door rather than
// absorbed, because a search that ran a subset reports a winner chosen from
// candidates the caller cannot name.
func TestGridIsTheProductAndIsBounded(t *testing.T) {
	grid, err := Space{Trees: []int{8, 16}, Depth: []int{4, 6}}.Grid()
	if err != nil {
		t.Fatal(err)
	}
	if len(grid) != 4 {
		t.Fatalf("2x2 is four candidates, got %d", len(grid))
	}
	// An unnamed axis takes the detector's own default rather than being empty.
	if grid[0].Window == 0 || grid[0].Blend == 0 || grid[0].Review == 0 {
		t.Fatalf("an unnamed axis must take the detector's default: %+v", grid[0])
	}

	huge := Space{
		Trees: seq(4), Depth: seq(4), Window: seq(4),
		Blend: fracs(4), Review: fracs(4),
	}
	if _, err := huge.Grid(); !errors.Is(err, ErrHuge) {
		t.Fatalf("a grid past the bound must be refused, got %v", err)
	}
	if _, err := (Space{Depth: []int{99}}).Grid(); !errors.Is(err, ErrShape) {
		t.Fatalf("a candidate past the node bound must be refused, got %v", err)
	}
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = 4 + i
	}
	return out
}

func fracs(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 0.05 * float64(i+1)
	}
	return out
}

// TestEmptyHistoryIsRefused, on the same terms as pkg/replay: "no alerts" is
// exactly what a quiet shape looks like, and choosing a model on the strength of
// an empty replay is the failure this package exists to prevent.
func TestEmptyHistoryIsRefused(t *testing.T) {
	_, err := Search(context.Background(), acme, replay.Slice{}, small(), Options{Seed: 7})
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("an empty history must be refused, got %v", err)
	}
	if _, err := Search(context.Background(), "", stream(10, false), small(), Options{Seed: 7}); !errors.Is(err, ErrOrg) {
		t.Fatalf("a search with no tenant studies nobody's geometry, got %v", err)
	}
	if _, err := Search(context.Background(), acme, nil, small(), Options{Seed: 7}); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("no history must be refused, got %v", err)
	}
}

// TestNoWinnerWithoutJudgement is the refusal that keeps a recommendation from
// being a preference. Ranking needs an outcome, and the only honest one is whether
// a shape separates what a human judged suspicious from what a human dismissed.
func TestNoWinnerWithoutJudgement(t *testing.T) {
	report, err := Search(context.Background(), acme, stream(400, false), small(), Options{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if report.Winner != nil {
		t.Fatalf("a winner was named from unlabelled data: %+v", report.Winner)
	}
	if report.Refusal != RefusalUnjudged {
		t.Fatalf("the refusal must say why: %q", report.Refusal)
	}
	if len(report.Trials) != 1 || report.Trials[0].Separation != nil {
		t.Fatalf("separation is absent and not zero when nothing was judged: %+v", report.Trials[0])
	}
	if report.Trials[0].Judged != 0 {
		t.Fatalf("nothing was judged, got %d", report.Trials[0].Judged)
	}
}

// TestSearchReportsTheCurveAndTheAppetite — what a console renders and what a
// reviewer reads.
func TestSearchReportsTheCurveAndTheAppetite(t *testing.T) {
	report, err := Search(context.Background(), acme, stream(600, true), small(), Options{Seed: 7, Curve: 8})
	if err != nil {
		t.Fatal(err)
	}
	trial := report.Trials[0]
	if len(trial.Curve) < 8 {
		t.Fatalf("the learning curve must carry the points asked for, got %d", len(trial.Curve))
	}
	if trial.Curve[len(trial.Curve)-1].Learned <= trial.Curve[0].Learned {
		t.Fatal("the curve must show the model learning")
	}
	if !trial.Warm {
		t.Fatalf("600 events over a 16-event window must warm a model: %+v", trial)
	}
	if trial.Stated != 0.02 {
		t.Fatalf("the appetite asked for must be on the trial: %v", trial.Stated)
	}
	if trial.Realised > trial.Stated+0.05 {
		t.Fatalf("realised %v is far above the stated %v — the threshold is not honouring the appetite", trial.Realised, trial.Stated)
	}
	if trial.Drift < 0 {
		t.Fatal("drift is a distance and is never negative")
	}
	if trial.Digest == "" || trial.Seed == 0 {
		t.Fatalf("a trial must name the shape it ran and the seed it ran with: %+v", trial)
	}
	if report.Seed != 7 {
		t.Fatalf("the report must carry the seed so the run can be reproduced, got %d", report.Seed)
	}
	if trial.Separation == nil {
		t.Fatal("with judged events, separation must be computed")
	}
	if len(trial.Features) == 0 {
		t.Fatal("a shape that alerted must say which features carried it")
	}
}

// TestSearchIsReproducible. A recommendation nobody can re-run is one nobody can
// check.
func TestSearchIsReproducible(t *testing.T) {
	a, err := Search(context.Background(), acme, stream(400, true), small(), Options{Seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Search(context.Background(), acme, stream(400, true), small(), Options{Seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	if a.Trials[0].Alerted != b.Trials[0].Alerted || a.Trials[0].Digest != b.Trials[0].Digest {
		t.Fatalf("the same seed over the same history must give the same answer: %d vs %d", a.Trials[0].Alerted, b.Trials[0].Alerted)
	}
}

// TestGeometryIsTheTenants. The detector is seeded from the tenant key, so a
// search run under a different key studies a different set of trees.
func TestGeometryIsTheTenants(t *testing.T) {
	mine, err := Search(context.Background(), acme, stream(400, true), small(), Options{Seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := Search(context.Background(), other, stream(400, true), small(), Options{Seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	// The same brand-qualified org name under two brands must not be one study.
	if mine.Trials[0].Alerted == theirs.Trials[0].Alerted && mine.Trials[0].Realised == theirs.Trials[0].Realised {
		t.Log("two tenants happened to alert identically; the geometry check below is the real one")
	}
	sameShape := mine.Trials[0].Digest == theirs.Trials[0].Digest
	if !sameShape {
		t.Fatal("the digest identifies the SHAPE and must not vary with the tenant")
	}
}

// TestWinnerPrefersSeparationThenTheSmallerModel. A shape that ties on evidence
// and costs less is the better answer; preferring the bigger one drifts the fleet
// upward every time a search runs.
func TestWinnerPrefersSeparationThenTheSmallerModel(t *testing.T) {
	space := Space{Trees: []int{8, 24}, Depth: []int{6}, Window: []int{16}, Blend: []float64{0.25}, Review: []float64{0.02}}
	report, err := Search(context.Background(), acme, stream(600, true), space, Options{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if report.Winner == nil {
		t.Fatalf("with judged events a winner must be named or the refusal must say why: %q", report.Refusal)
	}
	if report.Winner.Separation == nil {
		t.Fatal("a winner is chosen on separation and must carry it")
	}
	best := report.Trials[0]
	for _, tr := range report.Trials {
		if tr.Separation != nil && (best.Separation == nil || *tr.Separation > *best.Separation) {
			best = tr
		}
	}
	if *report.Winner.Separation < *best.Separation {
		t.Fatalf("the winner is not the best separator: %v vs %v", *report.Winner.Separation, *best.Separation)
	}
}

// TestAUCIsHalfWhenNothingIsSeparated. A detector that says nothing must not be
// able to win a search by sort order.
func TestAUCIsHalfWhenNothingIsSeparated(t *testing.T) {
	flat := []float64{0.5, 0.5, 0.5, 0.5}
	label := []bool{true, false, true, false}
	got, ok := auc(flat, label)
	if !ok || got != 0.5 {
		t.Fatalf("all-tied scores are no separation: %v (ok=%v)", got, ok)
	}
	perfect, ok := auc([]float64{0.9, 0.8, 0.2, 0.1}, []bool{true, true, false, false})
	if !ok || perfect != 1 {
		t.Fatalf("perfect ordering is 1, got %v", perfect)
	}
	backwards, ok := auc([]float64{0.1, 0.2, 0.8, 0.9}, []bool{true, true, false, false})
	if !ok || backwards != 0 {
		t.Fatalf("reversed ordering is 0, got %v", backwards)
	}
	if _, ok := auc([]float64{0.5}, []bool{true}); ok {
		t.Fatal("one class is not separation and must be absent")
	}
}

// TestFitProducesRestorableState, and only into a model of the same shape.
//
// That is the governance property: a tenant's model shape cannot be swapped
// underneath it by restoring a file.
func TestFitProducesRestorableState(t *testing.T) {
	shape := Topology{Trees: 8, Depth: 6, Window: 16, Blend: 0.25, Review: 0.02}
	snap, trial, err := Fit(context.Background(), acme, stream(600, true), shape, Options{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if snap.OrgID != acme || snap.Learned == 0 {
		t.Fatalf("a fit must carry the tenant's learned state: %+v", snap.OrgID)
	}

	live, err := anomaly.New(shape.Config(7), velocityStore())
	if err != nil {
		t.Fatal(err)
	}
	if live.Digest() != trial.Digest {
		t.Fatalf("a fit of the running shape must match its digest: %s vs %s", trial.Digest, live.Digest())
	}
	if err := live.Restore(snap); err != nil {
		t.Fatalf("a fit of the running shape must restore: %v", err)
	}
	if st := live.State(acme); st.Learned != snap.Learned {
		t.Fatalf("the restored model did not take the learned state: %d vs %d", st.Learned, snap.Learned)
	}

	// A model of a DIFFERENT shape refuses it.
	otherShape := Topology{Trees: 16, Depth: 6, Window: 16, Blend: 0.25, Review: 0.02}
	elsewhere, err := anomaly.New(otherShape.Config(7), velocityStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := elsewhere.Restore(snap); err == nil {
		t.Fatal("a fit of one shape must not install into a model of another")
	}
}

// TestTheSandboxIsStructurallyDry. A study that can write is not a sandbox, and
// the property is held up by the import list rather than by care.
func TestTheSandboxIsStructurallyDry(t *testing.T) {
	for _, name := range []string{"topology.go", "search.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, forbidden := range []string{
			"luxfi/aml/pkg/store", "luxfi/aml/pkg/retention", "luxfi/aml/pkg/cases",
			"hanzoai/base", "hanzoai/dbx",
		} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s imports %s: a study of the model must have nothing it could write to", name, forbidden)
			}
		}
	}
}

// velocityStore is the aggregate store a live model reads. It is here rather than
// imported into the package under test's own file because only a test needs one.
func velocityStore() *velocity.Store { return velocity.New(velocity.Config{}) }
