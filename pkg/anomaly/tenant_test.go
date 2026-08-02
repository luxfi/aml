package anomaly

import (
	"fmt"
	"testing"

	"github.com/luxfi/aml/internal/source"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

// A tenant's model is a control, and no other tenant may switch it off.
//
// The model held here IS the behavioural half of this engine's monitoring. A
// tenant whose model went back to warming reports nothing, and reporting nothing
// is what a clean institution also does — so a model that resets on its own is a
// control that turned itself off and told nobody. The store used to hold every
// tenant's model in one map under one cap and drop the least recently used one to
// make room, which made that reset something ANOTHER institution's ordinary
// traffic could cause, for free, at any time.

// planted runs enough transactions through one tenant to plant and warm nothing
// in particular — it only has to exist and have learned something.
func planted(t *testing.T, s *Store, org string, n int) {
	t.Helper()
	for i := range n {
		s.Learn(types.Transaction{
			ID: fmt.Sprintf("%s-tx-%d", org, i), OrgID: org,
			UserID: "acct-1", AccountID: "acct-1", Notional: 1000, USD: 1000,
			Currency: "USD", Timestamp: start,
		}, types.Entity{})
	}
}

// TestOneTenantsVolumeCannotResetAnothersModel is red's repro, as a property.
func TestOneTenantsVolumeCannotResetAnothersModel(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{Orgs: 2, Seed: 1}, vel)
	if err != nil {
		t.Fatal(err)
	}

	const victim = "hanzo/victim"
	planted(t, s, victim, 40)
	before := s.State(victim)
	if before.Learned != 40 {
		t.Fatalf("the victim's own model did not learn: %+v", before.Learned)
	}

	for _, other := range []string{"hanzo/loud", "hanzo/louder"} {
		planted(t, s, other, 5)
	}

	after := s.State(victim)
	if after.Learned != before.Learned {
		t.Errorf("two other tenants' traffic took this tenant's model from learned=%d to learned=%d, with no error and nothing in the state to say so",
			before.Learned, after.Learned)
	}
	if !after.Planted.Equal(before.Planted) {
		t.Errorf("the victim's model was replanted at %s (was %s) by another tenant's traffic", after.Planted, before.Planted)
	}
}

// TestATenantWithNoRoomIsRefusedLoudly: the roster is full and a new institution
// arrives. It gets no model — which is a real gap in a real control — so the
// transaction is refused by name and counted, exactly as warming and unusable
// coordinates are. Silence is the one answer that is not allowed.
func TestATenantWithNoRoomIsRefusedLoudly(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{Orgs: 1, Seed: 1}, vel)
	if err != nil {
		t.Fatal(err)
	}
	planted(t, s, "hanzo/first", 5)

	const late = "hanzo/late"
	a := s.Learn(types.Transaction{
		ID: "tx-1", OrgID: late, UserID: "acct-1", AccountID: "acct-1",
		Notional: 1000, USD: 1000, Currency: "USD", Timestamp: start,
	}, types.Entity{})
	if a.Reason != ReasonCrowded {
		t.Errorf("a tenant this process has no room for was refused with %q, want %q", a.Reason, ReasonCrowded)
	}
	if a.Scored {
		t.Error("a tenant with no model was scored")
	}

	st := s.State(late)
	if !st.Crowded {
		t.Error("state does not say the process had no room for this tenant's model")
	}
	if p := s.Pressure(); p.Crowded == 0 {
		t.Errorf("the refusal is nowhere in the process's own reading: %+v", p)
	}
	// And the institution that WAS admitted is untouched.
	if s.State("hanzo/first").Learned != 5 {
		t.Error("the admitted tenant lost its model to an arrival")
	}
}

// TestTheAdoptedReloadIsAskedOncePerTenant: the reload put a store read and a
// whole learned-state unmarshal on the ingest path. It is right that it happens
// — an adopted control must come back after a rollout — but it must happen once
// per tenant and not once per transaction.
func TestTheAdoptedReloadIsAskedOncePerTenant(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{Seed: 1}, vel)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	s.SetAdopted(func(string) (Snapshot, bool) { asked++; return Snapshot{}, false })

	planted(t, s, "hanzo/acme", 25)
	if asked != 1 {
		t.Errorf("the durable adoption was read %d times for one tenant's 25 transactions, want 1", asked)
	}
}

// TestNoTenantMapIsEvictedFrom reads this package's own source. See the same
// test in pkg/velocity, and pkg/roster.
func TestNoTenantMapIsEvictedFrom(t *testing.T) {
	source.NoTable(t, "anomaly.go", "Store",
		"One map of every tenant's state under one cap is how one institution's traffic evicts another's; per-tenant state goes in roster.Roster, which cannot remove.")
}
