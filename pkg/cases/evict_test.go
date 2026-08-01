// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package cases

import (
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/base/tests"

	"github.com/luxfi/aml/pkg/types"
)

// The case plane used to keep a clock of its own: closed cases were dropped 90
// days after closure, once the store held more than a hundred thousand of them,
// and both the count and the sweep were taken across every tenant at once. These
// tests are what says it does not any more.
//
// Three things had to be true and were not:
//
//  1. the durable shelf does not evict — expiry is pkg/retention's, where the
//     clock is five years (Dir. (EU) 2015/849 Art. 40), the sweep is per tenant,
//     and the disposal is proven;
//  2. what eviction remains — the memory shelf's, which only this package's tests
//     construct — is scoped to one org, so one institution's volume cannot
//     destroy another's evidence;
//  3. opening a case takes no census, so the ingest path does not pay a full
//     scan under the store lock.

const (
	orgA = "hanzo/first-national"
	orgB = "hanzo/second-mutual"
)

// durableStore is a case store on real Base collections, and the directory it
// keeps them in.
func durableStore(t *testing.T) (*Store, *tests.TestApp) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	if err := Ensure(app); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return NewBase(app), app
}

// closeLongAgo resolves a case and rewinds its closure to `years` ago, which is
// what a case that every version of the old eviction rule would have dropped
// looks like.
func closeLongAgo(t *testing.T, s *Store, org, id string, years int) {
	t.Helper()
	if err := s.Resolve(org, id, types.ResolutionCleared, "a.mensah", "as-1"); err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	c, err := s.shelf.get(id)
	if err != nil || c == nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	long := time.Now().UTC().AddDate(-years, 0, 0)
	c.ClosedAt = &long
	c.UpdatedAt = long
	if err := s.shelf.put(c); err != nil {
		t.Fatalf("rewind %s: %v", id, err)
	}
}

// TestTheDurableStoreIsUnbounded is the invariant the rest of this file
// exercises, checked directly rather than sampled: [NewBase] produces a store
// with no case count to exceed and no window past closure, and there is no
// exported constructor that could give it either.
//
// It is worth stating as its own test because the alternative — writing more
// cases than some threshold and observing that none went — can only ever show
// that the threshold is not the number the test happened to pick. This shows
// there is no number.
func TestTheDurableStoreIsUnbounded(t *testing.T) {
	s, app := durableStore(t)
	t.Cleanup(app.Cleanup)

	if s.bound != (bound{}) {
		t.Fatalf("the durable case store carries a bound: %+v — the shelf an instance serves from must never evict", s.bound)
	}
}

// TestTheDurableStoreDropsNothingHoweverManyOrHowOld writes far more closed
// cases than any tenant would have in a day, every one of them closed ten years
// ago, runs the eviction entry point at them directly, opens more cases on top,
// restarts the instance over the same bytes — and finds all of them still there,
// with their timelines.
//
// Ten years is past every window that has ever been in this package and past the
// five-year statutory one as well, so nothing here survives by being too recent
// to drop. What it survives by is that the durable shelf has no bound, which
// [TestTheDurableStoreIsUnbounded] states exactly.
func TestTheDurableStoreDropsNothingHoweverManyOrHowOld(t *testing.T) {
	s, app := durableStore(t)

	const closed = 250
	ids := make([]string, 0, closed)
	for i := range closed {
		c := s.Create(orgA, types.SeverityLow, nil, nil)
		if c == nil {
			t.Fatalf("Create %d returned no case", i)
		}
		if err := s.AddEvent(orgA, c.ID, types.CaseEvent{
			Kind:     types.EventNote,
			AuthorID: "a.mensah",
			Body:     fmt.Sprintf("reviewed %d", i),
		}); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
		closeLongAgo(t, s, orgA, c.ID, 10)
		ids = append(ids, c.ID)
	}

	// The entry point that used to destroy them, aimed straight at them.
	if dropped := s.EvictExpired(orgA); dropped != 0 {
		t.Fatalf("EvictExpired dropped %d cases from the durable store", dropped)
	}
	if held := s.Len(orgA); held != closed {
		t.Fatalf("after EvictExpired the store holds %d cases, want %d", held, closed)
	}

	// Opening a case is where the sweep used to be triggered from.
	for range 5 {
		if c := s.Create(orgA, types.SeverityHigh, nil, nil); c == nil {
			t.Fatal("Create returned no case")
		}
	}
	if held := s.Len(orgA); held != closed+5 {
		t.Fatalf("opening cases dropped some: holds %d, want %d", held, closed+5)
	}

	// The restart.
	dir := copyTree(t, app.DataDir())
	app.Cleanup()
	second, err := tests.NewTestApp(dir)
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	t.Cleanup(second.Cleanup)
	after := NewBase(second)

	if held := after.Len(orgA); held != closed+5 {
		t.Fatalf("after the restart the store holds %d cases, want %d", held, closed+5)
	}
	for i, id := range ids {
		got := after.Get(id)
		if got == nil {
			t.Fatalf("case %d (%s) is gone after the restart", i, id)
		}
		if got.Status != types.CaseClosed || got.Assessment != "as-1" {
			t.Fatalf("case %d came back changed: %+v", i, got)
		}
		// The timeline is the evidence of the work; eviction took it with the case.
		timeline := after.Events(id)
		if len(timeline) != 2 {
			t.Fatalf("case %d has %d timeline entries, want 2 (the note and the closure)", i, len(timeline))
		}
		if timeline[0].AuthorID != "a.mensah" {
			t.Fatalf("case %d lost its note's author: %+v", i, timeline[0])
		}
	}
}

// TestOneTenantsVolumeNeverDropsAnothers is the cross-tenant half. Institution A
// runs the volume; institution B has a handful of cases closed ten years ago,
// which is exactly what the old sweep looked for. B's evidence is not A's to
// destroy, and the tenant boundary has to hold across the restart too.
func TestOneTenantsVolumeNeverDropsAnothers(t *testing.T) {
	s, app := durableStore(t)

	// B's record, written first so it is the oldest thing in the store.
	bIDs := make([]string, 0, 3)
	for i := range 3 {
		c := s.Create(orgB, types.SeverityCritical, []string{"al-b"}, []string{"cust-b"})
		if c == nil {
			t.Fatalf("B Create %d returned no case", i)
		}
		if err := s.AddEvent(orgB, c.ID, types.CaseEvent{
			Kind: types.EventNote, AuthorID: "b.okafor", Body: "STR filed",
		}); err != nil {
			t.Fatalf("B note %d: %v", i, err)
		}
		closeLongAgo(t, s, orgB, c.ID, 10)
		bIDs = append(bIDs, c.ID)
	}

	// A's volume, all of it closed and ancient.
	for i := range 200 {
		c := s.Create(orgA, types.SeverityLow, nil, nil)
		if c == nil {
			t.Fatalf("A Create %d returned no case", i)
		}
		closeLongAgo(t, s, orgA, c.ID, 10)
	}
	// And A asking for its own eviction, which is the nearest thing to a lever
	// one tenant has on the store.
	s.EvictExpired(orgA)

	assertBIntact := func(when string, st *Store) {
		t.Helper()
		if held := st.Len(orgB); held != 3 {
			t.Fatalf("%s: B holds %d cases, want 3 — A's volume destroyed B's evidence", when, held)
		}
		for _, id := range bIDs {
			got := st.Get(id)
			if got == nil {
				t.Fatalf("%s: B's case %s is gone", when, id)
			}
			if got.OrgID != orgB {
				t.Fatalf("%s: B's case came back owned by %s", when, got.OrgID)
			}
			if ev := st.Events(id); len(ev) != 2 || ev[0].AuthorID != "b.okafor" {
				t.Fatalf("%s: B's timeline for %s did not survive: %+v", when, id, ev)
			}
		}
	}
	assertBIntact("after A's volume", s)

	dir := copyTree(t, app.DataDir())
	app.Cleanup()
	second, err := tests.NewTestApp(dir)
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	t.Cleanup(second.Cleanup)
	assertBIntact("after the restart", NewBase(second))
}

// TestABoundedStoreEvictsOnlyItsOwnTenant covers the eviction that remains. Only
// this package's tests can construct it, but it is still the code that runs when
// they do, and it must not be able to cross the boundary either — the memory
// shelf's map is shared by every org in it, which is what made an unscoped sweep
// so easy to write.
//
// It asserts BOTH halves: that A's own expired cases went (so the test would
// notice if eviction had simply stopped working) and that B's did not.
func TestABoundedStoreEvictsOnlyItsOwnTenant(t *testing.T) {
	s := newBounded(5, time.Millisecond)

	for range 3 {
		c := s.Create(orgB, types.SeverityHigh, nil, nil)
		if err := s.Resolve(orgB, c.ID, types.ResolutionCleared, "b.okafor", "as-b"); err != nil {
			t.Fatalf("B resolve: %v", err)
		}
	}
	for range 20 {
		c := s.Create(orgA, types.SeverityLow, nil, nil)
		if err := s.Resolve(orgA, c.ID, types.ResolutionCleared, "a.mensah", "as-a"); err != nil {
			t.Fatalf("A resolve: %v", err)
		}
	}

	// Everything closed is now past the window.
	time.Sleep(5 * time.Millisecond)

	// A crosses its bound, which is what triggers the sweep.
	s.Create(orgA, types.SeverityLow, nil, nil)

	if held := s.Len(orgA); held > 5+1 {
		t.Errorf("A's bound did not hold: %d cases, want at most 6 — eviction is not running at all", held)
	}
	if held := s.Len(orgB); held != 3 {
		t.Fatalf("B holds %d cases, want 3 — a bounded store evicted across the tenant boundary", held)
	}

	// And B's own eviction is B's to ask for.
	if dropped := s.EvictExpired(orgB); dropped != 3 {
		t.Errorf("B's own EvictExpired dropped %d, want 3", dropped)
	}
}

// countingShelf reports what passed through it. A sweep is `each`; a destruction
// is `drop`.
type countingShelf struct {
	shelf
	sweeps  int
	dropped int
}

func (c *countingShelf) each(org string, visit func(*types.Case) error) error {
	c.sweeps++
	return c.shelf.each(org, visit)
}

func (c *countingShelf) drop(org string, ids []string) error {
	c.dropped += len(ids)
	return c.shelf.drop(org, ids)
}

// TestOpeningACaseTakesNoCensus is the other half of the defect: the count that
// decided whether to evict was a full scan of every case in the store, taken
// under the store's lock on every single Create, on the path the replay gate
// calls the request that must not wait. Once the store was over the threshold
// with nothing old enough to drop, every Create paid two of those scans forever.
//
// The wrapped shelf is the real durable one, so this is the ingest path an
// instance runs, counted.
func TestOpeningACaseTakesNoCensus(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	counted := &countingShelf{shelf: durable{app: app}}
	s := &Store{shelf: counted} // the bound NewBase gives: none

	for i := range 25 {
		if c := s.Create(orgA, types.SeverityLow, nil, nil); c == nil {
			t.Fatalf("Create %d returned no case", i)
		}
	}

	if counted.sweeps != 0 {
		t.Errorf("opening 25 cases swept the store %d times — the ingest path is taking a census", counted.sweeps)
	}
	if counted.dropped != 0 {
		t.Errorf("opening 25 cases destroyed %d of them", counted.dropped)
	}
}

// TestStaleEscalationStaysInsideOneTenant: the stale sweep writes — it changes a
// case's status and appends to its timeline — so an unscoped one was a
// cross-tenant WRITE on another institution's investigation, on whichever
// tenant's cycle happened to run.
func TestStaleEscalationStaysInsideOneTenant(t *testing.T) {
	s := NewStore()

	mine := s.Create(orgA, types.SeverityLow, nil, nil)
	theirs := s.Create(orgB, types.SeverityCritical, nil, nil)

	long := time.Now().UTC().AddDate(0, 0, -90)
	age(t, s, mine.ID, long, long)
	age(t, s, theirs.ID, long, long)

	if n := s.AutoEscalateStale(orgA, DefaultTriageConfig()); n != 1 {
		t.Fatalf("escalated %d of A's cases, want 1", n)
	}
	if got := s.Get(theirs.ID); got.Status != types.CaseOpen {
		t.Fatalf("A's triage cycle escalated B's case: %s", got.Status)
	}
	if got := s.Get(mine.ID); got.Status != types.CaseEscalated {
		t.Fatalf("A's own case was not escalated: %s", got.Status)
	}
	if ev := s.Events(theirs.ID); len(ev) != 0 {
		t.Fatalf("A's triage cycle wrote on B's timeline: %+v", ev)
	}
}

// TestLeastLoadedCountsOnlyTheTenantsOwnQueue: assignment picked the analyst with
// the fewest open cases anywhere, so another institution's queue both moved this
// one's assignments and was countable through them.
func TestLeastLoadedCountsOnlyTheTenantsOwnQueue(t *testing.T) {
	s := NewStore()

	// In B, "ana" is buried. A cannot see that and must not act on it.
	for range 10 {
		c := s.Create(orgB, types.SeverityLow, nil, nil)
		if err := s.Assign(c.ID, "ana"); err != nil {
			t.Fatalf("assign: %v", err)
		}
	}
	// In A, "ana" holds nothing and "bo" holds one.
	held := s.Create(orgA, types.SeverityLow, nil, nil)
	if err := s.Assign(held.ID, "bo"); err != nil {
		t.Fatalf("assign: %v", err)
	}

	next := s.Create(orgA, types.SeverityLow, nil, nil)
	if got := s.AutoAssign(next, []string{"ana", "bo"}, "least_loaded"); got != "ana" {
		t.Fatalf("assigned to %q, want \"ana\" — the load count is reading another tenant's queue", got)
	}
}

// TestCaseNumbersAreTheTenantsOwn: a case number is what a file is referred to
// by. Drawn from one sequence for the whole deployment, the gaps in one
// institution's numbering counted another's cases.
func TestCaseNumbersAreTheTenantsOwn(t *testing.T) {
	s, app := durableStore(t)
	t.Cleanup(app.Cleanup)

	if first := s.Create(orgA, types.SeverityLow, nil, nil); first.Number != 1 {
		t.Fatalf("A's first case is number %d, want 1", first.Number)
	}
	for range 9 {
		s.Create(orgA, types.SeverityLow, nil, nil)
	}
	if first := s.Create(orgB, types.SeverityLow, nil, nil); first.Number != 1 {
		t.Fatalf("B's first case is number %d, want 1 — A's volume is showing in B's file references", first.Number)
	}
	if next := s.Create(orgA, types.SeverityLow, nil, nil); next.Number != 11 {
		t.Fatalf("A's next case is number %d, want 11", next.Number)
	}
	if next := s.Create(orgB, types.SeverityLow, nil, nil); next.Number != 2 {
		t.Fatalf("B's next case is number %d, want 2", next.Number)
	}
}
