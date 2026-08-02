package velocity

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/source"
)

// What a bound has to bound.
//
// Two properties, and both of them were false. A store that holds a hundred
// thousand keys holds a hundred thousand RINGS, and a ring is not a pointer to
// the caller's data — it is 444 buckets of counters, allocated whole on the
// first transaction. So a bound expressed in KEYS says nothing about the memory
// the process will actually use, and the number it implied here was over twice
// the pod's whole limit. And a bound over one map shared by every tenant is a
// bound each tenant spends out of the others' share: one institution's traffic
// evicted another's aggregates, silently, and a structuring window that reads
// zero is what a clean account also reads.
//
// Both tests below are measurements, not assertions about constants. The first
// weighs a real full store; the second watches one tenant's window while two
// others run traffic through it.

// TestTheCeilingIsInBytesAndItIsHonest fills one tenant to its bound and weighs
// the process.
//
// The published ceiling has to be an upper bound on what the store can actually
// take, or it is a number that reads as a guarantee and is not one. Measuring is
// the only way to know: the per-key cost is the sum of four rings' buckets and it
// changes whenever a window is added or its resolution moves, so a hand-written
// constant goes stale the first time somebody tunes a window.
func TestTheCeilingIsInBytesAndItIsHonest(t *testing.T) {
	const org = "hanzo/acme"
	s := New(Config{})

	ceiling := s.Ceiling()
	if ceiling <= 0 {
		t.Fatalf("the store publishes no byte ceiling")
	}

	// Fill this tenant past its own bound, so what is weighed is a store holding
	// as much as it will ever hold.
	at := time.Now().UTC()
	for i := range s.Room() * 2 {
		s.Record(Key{OrgID: org, Kind: "account", Value: fmt.Sprintf("acct-%d", i)}, at, 9_400, 10_000)
	}

	held := s.Bytes()
	if held > ceiling {
		t.Errorf("the store holds %d bytes against a published ceiling of %d", held, ceiling)
	}

	// And the published figure is not fiction: weigh the heap with the store
	// alive and compare. A ceiling that under-states the real cost by more than
	// the slack below is a ceiling an operator would size a pod from and be wrong.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	other := New(Config{})
	for i := range other.Room() {
		other.Record(Key{OrgID: org, Kind: "account", Value: fmt.Sprintf("acct-%d", i)}, at, 9_400, 10_000)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	real := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	runtime.KeepAlive(other)

	t.Logf("one tenant full: keys=%d published=%d bytes measured=%d bytes ceiling=%d bytes",
		other.Keys(), other.Bytes(), real, ceiling)
	if real > ceiling {
		t.Errorf("a full tenant measures %d bytes on the heap against a published ceiling of %d", real, ceiling)
	}
}

// TestOneTenantsTrafficDoesNotEvictAnothers is the whole of what per tenant
// means.
//
// A victim runs a window up, two other institutions then push their own traffic
// through the same process, and the victim's window is read again. It must be
// exactly what it was. The failure this catches is not a crash: the victim's
// structuring aggregate silently returns to zero, and no rule that reads it can
// tell that from an account that has done nothing.
func TestOneTenantsTrafficDoesNotEvictAnothers(t *testing.T) {
	// A store with room for very little, so the pressure is reachable in a test
	// without writing a million keys. The bound is what is under test; its size
	// is not.
	s := New(Config{PerOrg: 40 * keyCost(StandardWindows())})

	const victim = "hanzo/victim"
	at := time.Now().UTC()
	for i := range 20 {
		s.Record(Key{OrgID: victim, Kind: "account", Value: fmt.Sprintf("acct-%d", i)}, at, 9_400, 10_000)
	}
	before := s.Observe(Key{OrgID: victim, Kind: "account", Value: "acct-0"})
	if before[1].Count != 1 {
		t.Fatalf("the victim's own window did not record: %+v", before[1])
	}

	// Two other institutions, each sending far more than the whole store's worth.
	for _, other := range []string{"hanzo/loud", "hanzo/louder"} {
		for i := range 500 {
			s.Record(Key{OrgID: other, Kind: "account", Value: fmt.Sprintf("acct-%d", i)}, at, 100, 10_000)
		}
	}

	after := s.Observe(Key{OrgID: victim, Kind: "account", Value: "acct-0"})
	if after[1].Count != before[1].Count || after[1].Sum != before[1].Sum {
		t.Errorf("two other tenants' traffic changed this tenant's 24h window: before count=%d sum=%v, after count=%d sum=%v",
			before[1].Count, before[1].Sum, after[1].Count, after[1].Sum)
	}
	if got := s.Load(victim).Keys; got != 20 {
		t.Errorf("the victim holds %d keys after two other tenants ran traffic, want the 20 it wrote", got)
	}
}

// TestATenantOnlyEvictsItselfAndSaysSo: reaching a bound is allowed. Reaching it
// quietly is not.
//
// A tenant past its own byte bound drops its own least recently used key —
// which is a real loss of a real aggregate — so the loss is counted and graded,
// per tenant, and readable. A control that switches off without saying so is
// worse than no control.
func TestATenantOnlyEvictsItselfAndSaysSo(t *testing.T) {
	s := New(Config{PerOrg: 40 * keyCost(StandardWindows())})
	const org = "hanzo/acme"

	at := time.Now().UTC()
	for i := range 20 {
		s.Record(Key{OrgID: org, Kind: "account", Value: fmt.Sprintf("acct-%d", i)}, at, 100, 10_000)
	}
	if l := s.Load(org); l.Grade != GradeClear || l.Dropped != 0 {
		t.Errorf("a tenant well inside its bound reads %q with %d dropped", l.Grade, l.Dropped)
	}

	for i := 20; i < 400; i++ {
		s.Record(Key{OrgID: org, Kind: "account", Value: fmt.Sprintf("acct-%d", i)}, at, 100, 10_000)
	}
	l := s.Load(org)
	if l.Dropped == 0 {
		t.Errorf("a tenant past its bound dropped nothing: %+v", l)
	}
	if l.Grade != GradeFull {
		t.Errorf("a tenant that is dropping its own keys grades %q, want %q", l.Grade, GradeFull)
	}
	if l.Bytes > l.Ceiling {
		t.Errorf("a tenant holds %d bytes against its own ceiling of %d", l.Bytes, l.Ceiling)
	}
}

// TestATenantTurnedAwayIsCountedNotHidden: the process holds state for a bounded
// number of tenants, and the bound admits rather than evicts. The tenant that
// arrives past it gets no aggregates — which is a real gap in a real control —
// so it is refused loudly and counted, never made room for by taking another
// institution's.
func TestATenantTurnedAwayIsCountedNotHidden(t *testing.T) {
	s := New(Config{Orgs: 2})
	at := time.Now().UTC()

	for _, org := range []string{"hanzo/one", "hanzo/two"} {
		s.Record(Key{OrgID: org, Kind: "account", Value: "acct-1"}, at, 100, 10_000)
	}
	s.Record(Key{OrgID: "hanzo/three", Kind: "account", Value: "acct-1"}, at, 100, 10_000)

	p := s.Pressure()
	if p.Orgs != 2 || p.Refused != 1 {
		t.Errorf("pressure = %d tenants held, %d refused; want 2 held and 1 refused", p.Orgs, p.Refused)
	}
	// And the two that were admitted still have everything they wrote.
	for _, org := range []string{"hanzo/one", "hanzo/two"} {
		if got := s.Observe(Key{OrgID: org, Kind: "account", Value: "acct-1"})[1].Count; got != 1 {
			t.Errorf("%s lost its window to a third tenant's arrival: count=%d", org, got)
		}
	}
}

// TestNoTenantMapIsEvictedFrom reads this package's own source.
//
// The behavioural tests above prove that today's code does not evict across
// tenants. This one is about tomorrow's: the tenant map lives in [roster.Roster],
// which has no removal at all, so there is no expression in this package that
// could take one institution's state to make room for another's. If a
// package-level map keyed by tenant ever reappears here, the shape it made
// possible reappears with it.
func TestNoTenantMapIsEvictedFrom(t *testing.T) {
	source.NoTable(t, "velocity.go", "Store",
		"One map of every tenant's state under one cap is how one institution's traffic evicts another's; per-tenant state goes in roster.Roster, which cannot remove.")
}
