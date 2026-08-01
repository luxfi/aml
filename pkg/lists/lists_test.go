package lists

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/internal/instance"
)

const (
	acme  = "hanzo/acme"
	rival = "hanzo/rival"
	// other is the SAME org name under a different brand. Every isolation test
	// uses it as well as a different org, because the brand half of the key is the
	// half a bare-org regression would drop.
	other = "zoo/acme"
)

func shelf(t *testing.T) (*Shelf, core.App) {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return NewBase(app), app
}

func declare(t *testing.T, s *Shelf, org, name, kind, class string) {
	t.Helper()
	if _, err := s.Declare(context.Background(), org, &DeclareIn{
		Name: name, Kind: kind, Class: class, By: "a.mensah",
	}); err != nil {
		t.Fatalf("declare %s: %v", name, err)
	}
}

func add(t *testing.T, s *Shelf, org, name string, values ...Value) {
	t.Helper()
	if _, err := s.Add(context.Background(), org, &AddIn{Name: name, Values: values, By: "a.mensah"}); err != nil {
		t.Fatalf("add to %s: %v", name, err)
	}
}

func listed(t *testing.T, s *Shelf, org, name, value string) bool {
	t.Helper()
	hit, err := s.Listed(context.Background(), org, name, value)
	if err != nil {
		t.Fatalf("listed %s/%s: %v", name, value, err)
	}
	return hit
}

// TestListNobodyDeclaredIsAnError is the reason this package refuses rather than
// reporting a miss. A rule naming a list that does not exist would otherwise be
// installed, catalogued, reported as coverage, and incapable of firing.
func TestListNobodyDeclaredIsAnError(t *testing.T) {
	s, _ := shelf(t)
	_, err := s.Listed(context.Background(), acme, "ip-deny", "10.0.0.1")
	if !errors.Is(err, ErrNoList) {
		t.Fatalf("a rule naming an undeclared list must error, got %v", err)
	}
}

// TestEmptyValueIsAnError: a transaction carrying no address must not quietly
// pass an address deny list.
func TestEmptyValueIsAnError(t *testing.T) {
	s, _ := shelf(t)
	declare(t, s, acme, "ip-deny", Deny, IP)
	if _, err := s.Listed(context.Background(), acme, "ip-deny", ""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("an empty value must error, got %v", err)
	}
}

// TestValuesAreNormalisedByClass. One address written two ways is one address; a
// deny list holding only one spelling is a control with a documented bypass.
func TestValuesAreNormalisedByClass(t *testing.T) {
	s, _ := shelf(t)
	declare(t, s, acme, "ip-deny", Deny, IP)
	declare(t, s, acme, "mail-deny", Deny, Email)
	declare(t, s, acme, "bin-watch", Deny, BIN)

	add(t, s, acme, "ip-deny", Value{Value: "10.0.0.1", Reason: "known proxy"})
	add(t, s, acme, "mail-deny", Value{Value: "  Fraud@Example.COM ", Reason: "chargebacks"})
	add(t, s, acme, "bin-watch", Value{Value: "4111 11", Reason: "test range"})

	for _, c := range []struct{ list, value string }{
		{"ip-deny", "::ffff:10.0.0.1"},
		{"ip-deny", "10.0.0.1"},
		{"mail-deny", "fraud@example.com"},
		{"mail-deny", "FRAUD@EXAMPLE.COM"},
		{"bin-watch", "411111"},
		{"bin-watch", "4111-11"},
	} {
		if !listed(t, s, acme, c.list, c.value) {
			t.Errorf("%s should hold %q after normalisation", c.list, c.value)
		}
	}
	if listed(t, s, acme, "ip-deny", "10.0.0.2") {
		t.Error("an address that was never listed must not match")
	}
}

// TestRangesMatchAddresses. A deny list of single addresses is not a usable
// control against a hosting range.
func TestRangesMatchAddresses(t *testing.T) {
	s, _ := shelf(t)
	declare(t, s, acme, "ip-deny", Deny, IP)
	add(t, s, acme, "ip-deny", Value{Value: "10.0.0.7/8", Reason: "hosting range"})

	if !listed(t, s, acme, "ip-deny", "10.9.9.9") {
		t.Fatal("an address inside a listed range must match")
	}
	if listed(t, s, acme, "ip-deny", "11.0.0.1") {
		t.Fatal("an address outside every listed range must not match")
	}
	// A range is stored masked, so 10.0.0.7/8 and 10.0.0.0/8 are one row.
	page, err := s.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Value != "10.0.0.0/8" {
		t.Fatalf("a range must be stored masked, got %+v", page.Entries)
	}
}

// TestRemovalKeepsTheRecord. A list entry is the reason a payment was refused; it
// stops matching and it does not go.
func TestRemovalKeepsTheRecord(t *testing.T) {
	s, _ := shelf(t)
	declare(t, s, acme, "ip-deny", Deny, IP)
	add(t, s, acme, "ip-deny", Value{Value: "10.0.0.1", Reason: "known proxy"})

	if _, err := s.Remove(context.Background(), acme, &RemoveIn{
		Name: "ip-deny", Value: "10.0.0.1", Reason: "customer appealed", By: "r.okafor",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if listed(t, s, acme, "ip-deny", "10.0.0.1") {
		t.Fatal("a removed value must stop matching")
	}

	page, err := s.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("removal destroyed the record: %d entries, want 1", len(page.Entries))
	}
	e := page.Entries[0]
	if e.Removed.IsZero() || e.RemoveBy != "r.okafor" || e.RemoveWhy != "customer appealed" {
		t.Fatalf("the removal must name its decider and its reason: %+v", e)
	}
	if e.Reason != "known proxy" || e.By != "a.mensah" {
		t.Fatalf("the original decision must survive the removal: %+v", e)
	}

	live, err := s.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Entries) != 0 {
		t.Fatalf("a removed entry is not live: %+v", live.Entries)
	}
}

// TestValidityWindowStopsMatchingWithoutDeleting.
func TestValidityWindowStopsMatchingWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s, _ := shelf(t)
	s.Now = func() time.Time { return now }
	declare(t, s, acme, "ip-deny", Deny, IP)
	add(t, s, acme, "ip-deny", Value{Value: "10.0.0.1", Reason: "temporary", Until: now.Add(time.Hour)})

	if !listed(t, s, acme, "ip-deny", "10.0.0.1") {
		t.Fatal("an entry inside its window must match")
	}
	s.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if listed(t, s, acme, "ip-deny", "10.0.0.1") {
		t.Fatal("an entry past its window must not match")
	}
	page, err := s.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatal("a window closing must not destroy the row — only retention decides that")
	}
}

// TestRestatementPutsAValueBack, and it names who put it back.
func TestRestatementPutsAValueBack(t *testing.T) {
	s, _ := shelf(t)
	declare(t, s, acme, "ip-deny", Deny, IP)
	add(t, s, acme, "ip-deny", Value{Value: "10.0.0.1", Reason: "known proxy"})
	if _, err := s.Remove(context.Background(), acme, &RemoveIn{
		Name: "ip-deny", Value: "10.0.0.1", Reason: "appealed", By: "r.okafor",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(context.Background(), acme, &AddIn{
		Name: "ip-deny", By: "s.tan", Values: []Value{{Value: "10.0.0.1", Reason: "reoffended"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !listed(t, s, acme, "ip-deny", "10.0.0.1") {
		t.Fatal("a restated value must match again")
	}
	page, _ := s.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny"})
	if len(page.Entries) != 1 {
		t.Fatalf("a restatement must not double the row: %d", len(page.Entries))
	}
	if page.Entries[0].By != "s.tan" || page.Entries[0].Reason != "reoffended" {
		t.Fatalf("the row must name the current decision: %+v", page.Entries[0])
	}
}

// TestEveryDecisionNamesADeciderAndAReason.
func TestEveryDecisionNamesADeciderAndAReason(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	if _, err := s.Declare(ctx, acme, &DeclareIn{Name: "x", Kind: Deny, Class: IP}); !errors.Is(err, ErrDecider) {
		t.Errorf("declaring without a decider must be refused, got %v", err)
	}
	declare(t, s, acme, "ip-deny", Deny, IP)
	if _, err := s.Add(ctx, acme, &AddIn{Name: "ip-deny", Values: []Value{{Value: "1.1.1.1"}}, By: "a"}); !errors.Is(err, ErrReason) {
		t.Errorf("adding without a reason must be refused, got %v", err)
	}
	if _, err := s.Add(ctx, acme, &AddIn{Name: "ip-deny", Values: []Value{{Value: "1.1.1.1", Reason: "r"}}}); !errors.Is(err, ErrDecider) {
		t.Errorf("adding without a decider must be refused, got %v", err)
	}
	if _, err := s.Remove(ctx, acme, &RemoveIn{Name: "ip-deny", Value: "1.1.1.1", By: "a"}); !errors.Is(err, ErrReason) {
		t.Errorf("removing without a reason must be refused, got %v", err)
	}
}

// TestPartialImportIsRefused: one unreadable value refuses the whole request
// rather than landing half of it, because a list whose contents nobody can state
// is not a control.
func TestPartialImportIsRefused(t *testing.T) {
	s, _ := shelf(t)
	declare(t, s, acme, "ip-deny", Deny, IP)
	_, err := s.Add(context.Background(), acme, &AddIn{
		Name: "ip-deny", By: "a.mensah",
		Values: []Value{{Value: "10.0.0.1", Reason: "ok"}, {Value: "not-an-address", Reason: "ok"}},
	})
	if !errors.Is(err, ErrValue) {
		t.Fatalf("an unreadable value must refuse the request, got %v", err)
	}
	page, _ := s.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny"})
	if len(page.Entries) != 0 {
		t.Fatalf("a refused import must have written nothing: %+v", page.Entries)
	}
}

// TestTenantIsolation is the property the whole product rests on, exercised over
// every operation this package has.
//
// The second tenant is the SAME org name under a different brand, which is the
// case a bare-org regression collides. Reads answer as though the other tenant's
// list does not exist, because a refusal that distinguished "not yours" from "not
// there" is a probe for which institutions exist.
func TestTenantIsolation(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	declare(t, s, acme, "ip-deny", Deny, IP)
	add(t, s, acme, "ip-deny", Value{Value: "10.0.0.1", Reason: "known proxy"})

	for _, stranger := range []string{other, rival} {
		if _, err := s.Listed(ctx, stranger, "ip-deny", "10.0.0.1"); !errors.Is(err, ErrNoList) {
			t.Errorf("%s can see %s's list: %v", stranger, acme, err)
		}
		if _, err := s.Entries(ctx, stranger, &EntriesIn{Name: "ip-deny"}); !errors.Is(err, ErrNoList) {
			t.Errorf("%s can read %s's entries: %v", stranger, acme, err)
		}
		if _, err := s.Remove(ctx, stranger, &RemoveIn{Name: "ip-deny", Value: "10.0.0.1", Reason: "r", By: "b"}); !errors.Is(err, ErrNoList) {
			t.Errorf("%s can remove from %s's list: %v", stranger, acme, err)
		}
		if _, err := s.Add(ctx, stranger, &AddIn{Name: "ip-deny", By: "b", Values: []Value{{Value: "9.9.9.9", Reason: "r"}}}); !errors.Is(err, ErrNoList) {
			t.Errorf("%s can write to %s's list: %v", stranger, acme, err)
		}
		cat, err := s.Catalog(ctx, stranger, &CatalogIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(cat.Lists) != 0 {
			t.Errorf("%s's catalog shows %d of another tenant's lists", stranger, len(cat.Lists))
		}
	}

	// And the two brands may each hold a list of the same name without either
	// seeing the other's values.
	declare(t, s, other, "ip-deny", Allow, IP)
	add(t, s, other, "ip-deny", Value{Value: "203.0.113.5", Reason: "our own egress"})
	if listed(t, s, acme, "ip-deny", "203.0.113.5") {
		t.Fatal("one brand's entry matched under another brand's tenant of the same org name")
	}
	if listed(t, s, other, "ip-deny", "10.0.0.1") {
		t.Fatal("one brand's entry matched under another brand's tenant of the same org name")
	}
}

// TestBareOrgIsRefused at the write boundary of every operation. The check is in
// the plane and not only where the key is minted, because the identity comes from
// a seam the deployment supplies.
func TestBareOrgIsRefused(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	for _, bare := range []string{"acme", "", "unknown/acme"} {
		if _, err := s.Declare(ctx, bare, &DeclareIn{Name: "x", Kind: Deny, Class: IP, By: "a"}); err == nil {
			t.Errorf("Declare accepted %q as a tenant", bare)
		}
		if _, err := s.Catalog(ctx, bare, &CatalogIn{}); err == nil {
			t.Errorf("Catalog accepted %q as a tenant", bare)
		}
		if _, err := s.Lookup(ctx, bare, &LookupIn{Name: "x", Value: "1.1.1.1"}); err == nil {
			t.Errorf("Lookup accepted %q as a tenant", bare)
		}
	}
}

// TestNothingDeletes reads the package's own source for a destructive call.
//
// Stated as a test rather than as a comment, because the property is what the
// retention plane's exclusivity rests on: if any operational knob can destroy a
// list entry, the five-year record is whatever survived the operator.
func TestNothingDeletes(t *testing.T) {
	for _, name := range []string{"lists.go", "shelf.go"} {
		src := read(t, name)
		if strings.Contains(src, ".Delete(") {
			t.Errorf("%s calls Delete: disposal is pkg/retention's decision and nobody else's", name)
		}
	}
}

// TestRestart is the durability half: what a restarted pod reads.
func TestRestart(t *testing.T) {
	first := instance.New(t)
	if err := Ensure(first); err != nil {
		t.Fatal(err)
	}
	before := NewBase(first)
	declare(t, before, acme, "ip-deny", Deny, IP)
	add(t, before, acme, "ip-deny", Value{Value: "10.0.0.1", Reason: "known proxy"})
	declare(t, before, other, "ip-deny", Allow, IP)
	add(t, before, other, "ip-deny", Value{Value: "203.0.113.5", Reason: "our own egress"})

	second := instance.Restart(t, first)
	if err := Ensure(second); err != nil {
		t.Fatal(err)
	}
	after := NewBase(second)

	if !listed(t, after, acme, "ip-deny", "10.0.0.1") {
		t.Fatal("the deny list did not survive the restart — the control was off after the rollout and nothing would have said so")
	}
	page, err := after.Entries(context.Background(), acme, &EntriesIn{Name: "ip-deny"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Reason != "known proxy" || page.Entries[0].By != "a.mensah" {
		t.Fatalf("the decision behind the entry did not survive: %+v", page.Entries)
	}
	if page.List.Added != 1 {
		t.Fatalf("the list's own counters did not survive: %+v", page.List)
	}

	// Tenancy still holds on the other side of the restart.
	if listed(t, after, other, "ip-deny", "10.0.0.1") {
		t.Fatal("the tenant boundary did not survive the restart")
	}
	if _, err := after.Listed(context.Background(), rival, "ip-deny", "10.0.0.1"); !errors.Is(err, ErrNoList) {
		t.Fatal("a third tenant can see a list after the restart")
	}
}
