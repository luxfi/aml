package models

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

const (
	acme  = "hanzo/acme"
	rival = "hanzo/rival"
	// other is the SAME org name under a different brand.
	other = "zoo/acme"
)

var (
	noon  = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	shape = topology.Topology{Trees: 8, Depth: 6, Window: 16, Blend: 0.25, Review: 0.02}
)

// history is a tenant's own traffic, with the outliers a human judged productive.
type history struct{ n int }

func (h history) History(ctx context.Context, org string, held *topology.Grant) (replay.History, error) {
	out := make(replay.Slice, 0, h.n)
	for i := 0; i < h.n; i++ {
		usd := 100 + float64(i%17)*3
		odd := i%53 == 52
		if odd {
			usd = 90_000
		}
		e := replay.Event{
			Tx: types.Transaction{
				ID: "tx-" + time.Duration(i).String(), OrgID: org, UserID: "u1", AccountID: "acct-1",
				Currency: "USD", Notional: usd, USD: usd, Direction: "in",
				Timestamp: noon.Add(time.Duration(i) * time.Minute),
			},
			Entity: types.Entity{ID: "u1", OrgID: org},
		}
		switch {
		case odd:
			e.Disposition = replay.Productive
		case i%23 == 0:
			e.Disposition = replay.Unproductive
		}
		out = append(out, e)
	}
	return out, nil
}

func live(t *testing.T, s topology.Topology, seed uint64) *anomaly.Store {
	t.Helper()
	model, err := anomaly.New(s.Config(seed), velocity.New(velocity.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func shelf(t *testing.T) *Shelf {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := NewBase(app)
	s.Now = func() time.Time { return noon }
	s.History = history{n: 600}
	s.Model = live(t, shape, 7)
	return s
}

// TestASearchIsKept. An answer that reached a screen and not the store is one
// nobody can produce when asked why the model looks the way it does.
func TestASearchIsKept(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	run, err := s.Search(ctx, acme, &SearchIn{
		Space:   topology.Space{Trees: []int{8}, Depth: []int{6}, Window: []int{16}, Blend: []float64{0.25}, Review: []float64{0.02}},
		Options: topology.Options{Seed: 7, Curve: 8},
		By:      "a.mensah",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Report.Trials) != 1 || run.Report.Seed != 7 {
		t.Fatalf("the run must carry the whole report: %+v", run.Report)
	}

	back, err := s.Run(ctx, acme, &RefIn{ID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Report.Trials) != 1 || back.By != "a.mensah" || back.Report.Seed != 7 {
		t.Fatalf("the run did not come back whole: %+v", back)
	}
	if len(back.Report.Trials[0].Curve) < 8 {
		t.Fatalf("the learning curve did not survive the store: %d points", len(back.Report.Trials[0].Curve))
	}

	list, err := s.Runs(ctx, acme, &RunsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Runs) != 1 || list.Runs[0].ID != run.ID || list.Runs[0].Trials != 1 {
		t.Fatalf("the list must summarise the run: %+v", list.Runs)
	}
	// A list is a summary and never the heavy read.
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\"curve\"") {
		t.Fatal("a list of runs must not carry a curve per candidate")
	}
}

// TestASearchNeedsAHistoryAndADecider.
func TestASearchNeedsAHistoryAndADecider(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	if _, err := s.Search(ctx, acme, &SearchIn{Space: topology.Space{}}); !errors.Is(err, ErrDecider) {
		t.Errorf("a search must name who asked for it, got %v", err)
	}
	s.History = nil
	if _, err := s.Search(ctx, acme, &SearchIn{Space: topology.Space{}, By: "a"}); !errors.Is(err, ErrNoHistory) {
		t.Errorf("a search over no history would report that every shape alerts on nothing, got %v", err)
	}
}

// TestFitIsAdoptableOnlyIntoItsOwnShape. That is the governance property: a
// tenant's model shape cannot be swapped underneath it by restoring a file.
func TestFitIsAdoptableOnlyIntoItsOwnShape(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()

	mine, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	if !mine.Adoptable {
		t.Fatalf("a fit of the running shape must be adoptable: %+v", mine)
	}

	bigger := shape
	bigger.Trees = 16
	elsewhere, err := s.Fit(ctx, acme, &FitIn{Topology: bigger, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere.Adoptable {
		t.Fatalf("a fit of a shape the deployment is not running must not be adoptable: %+v", elsewhere)
	}
	if _, err := s.Adopt(ctx, acme, &AdoptIn{ID: elsewhere.ID, Reason: "the search recommended it", By: "a.mensah"}); err == nil {
		t.Fatal("adopting a fit of another shape must be refused by the model itself")
	}

	adopted, err := s.Adopt(ctx, acme, &AdoptIn{ID: mine.ID, Reason: "the search recommended it", By: "r.okafor"})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if adopted.Adopted.IsZero() || adopted.AdoptedBy != "r.okafor" {
		t.Fatalf("an adoption must name who took it and when: %+v", adopted)
	}
	if st := s.Model.State(acme); st.Learned == 0 {
		t.Fatal("the live model did not take the fitted state")
	}
	if _, err := s.Adopt(ctx, acme, &AdoptIn{ID: mine.ID, Reason: "again", By: "r.okafor"}); !errors.Is(err, ErrAdopted) {
		t.Fatalf("adopting twice must be refused, got %v", err)
	}
}

// TestLearnedStateNeverLeavesTheStore.
//
// Mass counters describe where a tenant's activity is dense; handing them out
// over an API would publish the shape of an institution's customer behaviour to
// whoever holds a token.
func TestLearnedStateNeverLeavesTheStore(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	fit, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fit)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"ref", "cur", "hist", "state", "snapshot", "seed"} {
		if _, there := out[leaked]; there {
			t.Errorf("a fit handed back %q — the learned state must stay in the tenant's store", leaked)
		}
	}
	if fit.Digest == "" {
		t.Fatal("the digest is what a caller compares, and must be on the answer")
	}
}

// TestASnapshotCannotBeInstalledIntoAnotherTenant. The tenant comes from the row's
// own scope; a snapshot claiming otherwise is refused.
func TestASnapshotCannotBeInstalledIntoAnotherTenant(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	mine, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adopt(ctx, other, &AdoptIn{ID: mine.ID, Reason: "r", By: "b"}); !errors.Is(err, ErrNoFit) {
		t.Fatalf("another tenant naming this fit's id must see no such fit, got %v", err)
	}
	if st := s.Model.State(other); st.Learned != 0 {
		t.Fatal("another tenant's model took this tenant's state")
	}
}

// TestTenantIsolation over every operation.
func TestTenantIsolation(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	run, err := s.Search(ctx, acme, &SearchIn{
		Space:   topology.Space{Trees: []int{8}, Depth: []int{6}, Window: []int{16}},
		Options: topology.Options{Seed: 7, Curve: 4}, By: "a.mensah",
	})
	if err != nil {
		t.Fatal(err)
	}
	fit, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}

	for _, stranger := range []string{other, rival} {
		if _, err := s.Run(ctx, stranger, &RefIn{ID: run.ID}); !errors.Is(err, ErrNotHere) {
			t.Errorf("%s can read %s's run: %v", stranger, acme, err)
		}
		list, err := s.Runs(ctx, stranger, &RunsIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Runs) != 0 {
			t.Errorf("%s can list %s's runs", stranger, acme)
		}
		fits, err := s.Fits(ctx, stranger, &FitsIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(fits.Fits) != 0 {
			t.Errorf("%s can list %s's fits", stranger, acme)
		}
		if _, err := s.Adopt(ctx, stranger, &AdoptIn{ID: fit.ID, Reason: "r", By: "b"}); !errors.Is(err, ErrNoFit) {
			t.Errorf("%s can adopt %s's fit: %v", stranger, acme, err)
		}
	}
}

// TestBareOrgIsRefused.
func TestBareOrgIsRefused(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	for _, bare := range []string{"acme", "", "unknown/acme"} {
		if _, err := s.Search(ctx, bare, &SearchIn{By: "a"}); err == nil {
			t.Errorf("Search accepted %q as a tenant", bare)
		}
		if _, err := s.Fit(ctx, bare, &FitIn{Topology: shape, By: "a"}); err == nil {
			t.Errorf("Fit accepted %q as a tenant", bare)
		}
		if _, err := s.Runs(ctx, bare, &RunsIn{}); err == nil {
			t.Errorf("Runs accepted %q as a tenant", bare)
		}
	}
}

// TestNothingDeletes.
func TestNothingDeletes(t *testing.T) {
	for _, name := range []string{"models.go", "shelf.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), ".Delete(") {
			t.Errorf("%s calls Delete: disposal is pkg/retention's decision and nobody else's", name)
		}
	}
}

// TestRestart: the study that justified a model, and the state it produced, are
// both still there after the pod that made them is gone.
func TestRestart(t *testing.T) {
	first := instance.New(t)
	if err := Ensure(first); err != nil {
		t.Fatal(err)
	}
	before := NewBase(first)
	before.Now = func() time.Time { return noon }
	before.History = history{n: 600}
	before.Model = live(t, shape, 7)

	ctx := context.Background()
	run, err := before.Search(ctx, acme, &SearchIn{
		Space:   topology.Space{Trees: []int{8}, Depth: []int{6}, Window: []int{16}, Review: []float64{0.02}},
		Options: topology.Options{Seed: 7, Curve: 8}, By: "a.mensah",
	})
	if err != nil {
		t.Fatal(err)
	}
	fit, err := before.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Fit(ctx, other, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "z.other"}); err != nil {
		t.Fatal(err)
	}

	second := instance.Restart(t, first)
	if err := Ensure(second); err != nil {
		t.Fatal(err)
	}
	after := NewBase(second)
	after.Now = func() time.Time { return noon }
	after.History = history{n: 600}
	// A FRESH model: warming, nothing learned. This is what a rollout produces.
	after.Model = live(t, shape, 7)

	back, err := after.Run(ctx, acme, &RefIn{ID: run.ID})
	if err != nil {
		t.Fatalf("the study did not survive the restart: %v", err)
	}
	if back.By != "a.mensah" || len(back.Report.Trials) == 0 {
		t.Fatalf("the study came back changed: %+v", back)
	}

	if st := after.Model.State(acme); st.Learned != 0 {
		t.Fatal("the fresh model should be warming — the test is not testing a restart")
	}
	adopted, err := after.Adopt(ctx, acme, &AdoptIn{ID: fit.ID, Reason: "restoring after a rollout", By: "r.okafor"})
	if err != nil {
		t.Fatalf("the fitted state did not survive the restart: %v", err)
	}
	if adopted.Digest != fit.Digest {
		t.Fatalf("the fit came back under a different shape: %s vs %s", adopted.Digest, fit.Digest)
	}
	if st := after.Model.State(acme); st.Learned == 0 {
		t.Fatal("a restart plus an adoption must leave the model warm, or every rollout is a blind window")
	}

	// Tenancy still holds.
	fits, err := after.Fits(ctx, rival, &FitsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fits.Fits) != 0 {
		t.Fatal("a third tenant can list fits after the restart")
	}
	mine, err := after.Fits(ctx, acme, &FitsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Fits) != 1 {
		t.Fatalf("the tenant boundary did not survive the restart: %d fits", len(mine.Fits))
	}
}

// TestAnAdoptedControlDoesNotGoQuietOnItsOwn.
//
// The fit is durable and the adoption is durable, but what an adoption DOES is
// install learned state into a model that lives in memory — and memory does not
// survive a rollout (one replica, Recreate) or an eviction (the live store holds
// a bounded number of tenants' models). Neither is a decision anybody took, and
// both silently return the tenant to warming, which reports no alerts, which is
// what a quiet institution also reports.
//
// So the model asks the plane what this tenant last adopted when it plants a
// model for it, and the control comes back by itself.
func TestAnAdoptedControlDoesNotGoQuietOnItsOwn(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	fit, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adopt(ctx, acme, &AdoptIn{ID: fit.ID, Reason: "the search recommended it", By: "r.okafor"}); err != nil {
		t.Fatal(err)
	}

	// What a rollout produces: the same durable rows, a brand new blind model.
	fresh := live(t, shape, 7)
	if st := fresh.State(acme); st.Learned != 0 {
		t.Fatal("the fresh model is not blind, so this is not testing a rollout")
	}
	fresh.SetAdopted(s.Adopted)

	// The first transaction after the rollout plants the tenant's model, and
	// planting is where the adoption comes back.
	fresh.Learn(types.Transaction{
		ID: "tx-after", OrgID: acme, UserID: "u1", AccountID: "acct-1",
		Currency: "USD", Notional: 100, USD: 100, Direction: "in",
		Timestamp: noon.Add(time.Hour),
	}, types.Entity{ID: "u1", OrgID: acme})

	st := fresh.State(acme)
	if st.Learned <= 1 {
		t.Fatalf("the adopted state did not come back: learned = %d", st.Learned)
	}
	// And it SAYS it came back. A model that reset and recovered and a model that
	// never reset read the same from the outside otherwise, so the two things a
	// reviewer needs — when this model started, and whether it started from an
	// adoption — are on the state.
	if !st.Restored {
		t.Fatal("the model came back from an adoption and does not say so")
	}
	if st.Planted.IsZero() {
		t.Fatal("the model does not say when it started, so a reset is invisible")
	}

	// The negative control: a tenant with no adoption plants blind, and says that
	// too. Without this the assertion above would pass on a state that reported
	// Restored for everything.
	fresh.Learn(types.Transaction{
		ID: "tx-other", OrgID: rival, UserID: "u1", AccountID: "acct-1",
		Currency: "USD", Notional: 100, USD: 100, Direction: "in",
		Timestamp: noon.Add(time.Hour),
	}, types.Entity{ID: "u1", OrgID: rival})
	blind := fresh.State(rival)
	if blind.Restored {
		t.Fatal("a tenant that adopted nothing reports a restored model")
	}
	if blind.Planted.IsZero() {
		t.Fatal("a blind model does not say when it started either")
	}
}

// TestAnotherTenantsAdoptionIsNeverReloaded. The reload is a per-tenant read of a
// per-tenant row, and the snapshot's own tenant is checked against the row's
// scope on the way out — the same refusal Adopt makes, made again where nobody is
// watching.
func TestAnotherTenantsAdoptionIsNeverReloaded(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	fit, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adopt(ctx, acme, &AdoptIn{ID: fit.ID, Reason: "adopted", By: "r.okafor"}); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Adopted(acme); !ok {
		t.Fatal("the tenant's own adoption was not found")
	}
	// A third tenant, and the SAME org name under another brand — the collision
	// the tenant key exists to prevent.
	for _, stranger := range []string{rival, other} {
		if snap, ok := s.Adopted(stranger); ok {
			t.Fatalf("%s reloaded another tenant's adopted state: %+v", stranger, snap.OrgID)
		}
	}
	// And a bare org names no tenant at all.
	if _, ok := s.Adopted("acme"); ok {
		t.Fatal("a bare org reloaded state")
	}
}

// TestOnlyAnAdoptionIsReloaded. A fit that nobody adopted is a recommendation,
// and a recommendation must not install itself.
func TestOnlyAnAdoptionIsReloaded(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	if _, err := s.Fit(ctx, acme, &FitIn{Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Adopted(acme); ok {
		t.Fatal("an unadopted fit installed itself")
	}
}

// TestTheMostRecentAdoptionWins. Adoption is a sequence of decisions and the last
// one is the one in force; coming back as an earlier one would be a control
// quietly reverting.
func TestTheMostRecentAdoptionWins(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	var last string
	for i, at := range []time.Time{noon, noon.Add(time.Hour), noon.Add(2 * time.Hour)} {
		s.Now = func() time.Time { return at }
		fit, err := s.Fit(ctx, acme, &FitIn{
			Topology: shape,
			Options:  topology.Options{Seed: uint64(7 + i)},
			By:       "a.mensah",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Adopt(ctx, acme, &AdoptIn{ID: fit.ID, Reason: "rolling forward", By: "r.okafor"}); err != nil {
			t.Fatal(err)
		}
		last = fit.ID
	}
	snap, ok := s.Adopted(acme)
	if !ok {
		t.Fatal("nothing came back")
	}
	fits, err := s.Fits(ctx, acme, &FitsIn{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fits.Fits {
		if f.ID == last && f.Adopted.IsZero() {
			t.Fatal("the last adoption was not recorded")
		}
	}
	if snap.OrgID != acme {
		t.Fatalf("the reloaded state belongs to %q", snap.OrgID)
	}
	if snap.Seed == 0 {
		t.Fatal("the reloaded snapshot carries no seed, so it is not real state")
	}
}

// TestAFitRecordsWhatItCost. The model plane is the expensive one, so a tenant's
// spend on it has to be answerable from what was kept rather than from a counter
// a restart resets.
func TestAFitRecordsWhatItCost(t *testing.T) {
	s := shelf(t)
	fit, err := s.Fit(context.Background(), acme, &FitIn{
		Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fit.Elapsed <= 0 {
		t.Fatalf("a fit records no cost: %+v", fit.Elapsed)
	}
	fits, err := s.Fits(context.Background(), acme, &FitsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fits.Fits) != 1 || fits.Fits[0].Elapsed != fit.Elapsed {
		t.Fatalf("the cost did not survive the write: %+v", fits.Fits)
	}
	if fit.Trial.Events == 0 {
		t.Fatal("a fit records how much history it read")
	}
}
