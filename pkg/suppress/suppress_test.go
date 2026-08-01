package suppress

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/instance"
)

const (
	acme  = "hanzo/acme"
	rival = "hanzo/rival"
	// other is the SAME org name under a different brand — the case a bare-org
	// regression collides into one tenant.
	other = "zoo/acme"
)

var noon = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func shelf(t *testing.T) *Shelf {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := NewBase(app)
	s.Now = func() time.Time { return noon }
	return s
}

func suppressed(t *testing.T, s *Shelf, org string, in *SuppressIn) *Suppression {
	t.Helper()
	if in.Reason == "" {
		in.Reason = "known merchant settlement pattern, reviewed 2026-02"
	}
	if in.By == "" {
		in.By = "a.mensah"
	}
	out, err := s.Suppress(context.Background(), org, in)
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	return out
}

// TestAKillSwitchIsNotASuppression. A row naming neither a rule nor a subject
// silences the whole monitoring programme and is indistinguishable in the store
// from an ordinary narrow one.
func TestAKillSwitchIsNotASuppression(t *testing.T) {
	s := shelf(t)
	_, err := s.Suppress(context.Background(), acme, &SuppressIn{Reason: "too noisy", By: "a.mensah"})
	if !errors.Is(err, ErrBroad) {
		t.Fatalf("a suppression covering everything must be refused, got %v", err)
	}
}

// TestEverySuppressionCarriesADecision.
func TestEverySuppressionCarriesADecision(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	if _, err := s.Suppress(ctx, acme, &SuppressIn{Rule: "r1", By: "a"}); !errors.Is(err, ErrReason) {
		t.Errorf("no reason must be refused, got %v", err)
	}
	if _, err := s.Suppress(ctx, acme, &SuppressIn{Rule: "r1", Reason: "r"}); !errors.Is(err, ErrDecider) {
		t.Errorf("no decider must be refused, got %v", err)
	}
	sup := suppressed(t, s, acme, &SuppressIn{Rule: "r1"})
	if _, err := s.Lift(ctx, acme, &LiftIn{ID: sup.ID, By: "a"}); !errors.Is(err, ErrReason) {
		t.Errorf("lifting without a reason must be refused, got %v", err)
	}
	if _, err := s.Lift(ctx, acme, &LiftIn{ID: sup.ID, Reason: "r"}); !errors.Is(err, ErrDecider) {
		t.Errorf("lifting without a decider must be refused, got %v", err)
	}
}

// TestASubjectNeedsItsAxis: a value with no kind covers "any subject whose value
// happens to be this string", which spans accounts and addresses at once.
func TestASubjectNeedsItsAxis(t *testing.T) {
	s := shelf(t)
	_, err := s.Suppress(context.Background(), acme, &SuppressIn{Value: "acct-1", Reason: "r", By: "a"})
	if !errors.Is(err, ErrSubject) {
		t.Fatalf("a subject value with no kind must be refused, got %v", err)
	}
}

// TestNarrowestCoverWins. A tenant that suppressed a whole rule and then took a
// specific decision about one account must see the specific one named, because
// that is the decision a reviewer will be asked about.
func TestNarrowestCoverWins(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	broad := suppressed(t, s, acme, &SuppressIn{Rule: "r1"})
	narrow := suppressed(t, s, acme, &SuppressIn{Rule: "r1", Kind: "account", Value: "acct-1"})

	cover, err := s.Cover(ctx, acme, &CoverIn{Rule: "r1", Kind: "account", Value: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !cover.Covered || cover.Suppression.ID != narrow.ID {
		t.Fatalf("the specific decision must be the one named, got %+v", cover.Suppression)
	}

	cover, err = s.Cover(ctx, acme, &CoverIn{Rule: "r1", Kind: "account", Value: "acct-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !cover.Covered || cover.Suppression.ID != broad.ID {
		t.Fatalf("another account falls to the rule-wide decision, got %+v", cover.Suppression)
	}

	cover, err = s.Cover(ctx, acme, &CoverIn{Rule: "r2", Kind: "account", Value: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	if cover.Covered {
		t.Fatalf("a suppression on r1 must not cover r2: %+v", cover.Suppression)
	}
}

// TestWindowsAndLifts. Both stop a suppression applying; neither destroys it.
func TestWindowsAndLifts(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	windowed := suppressed(t, s, acme, &SuppressIn{Rule: "r1", Until: noon.Add(time.Hour)})

	if c, _ := s.Cover(ctx, acme, &CoverIn{Rule: "r1"}); !c.Covered {
		t.Fatal("a suppression inside its window must cover")
	}
	s.Now = func() time.Time { return noon.Add(2 * time.Hour) }
	if c, _ := s.Cover(ctx, acme, &CoverIn{Rule: "r1"}); c.Covered {
		t.Fatal("a suppression past its window must not cover")
	}
	s.Now = func() time.Time { return noon }

	lifted, err := s.Lift(ctx, acme, &LiftIn{ID: windowed.ID, Reason: "volume reviewed", By: "r.okafor"})
	if err != nil {
		t.Fatal(err)
	}
	if lifted.LiftedBy != "r.okafor" || lifted.LiftWhy != "volume reviewed" || lifted.Lifted.IsZero() {
		t.Fatalf("a lift must name its decider and its reason: %+v", lifted)
	}
	if _, err := s.Lift(ctx, acme, &LiftIn{ID: windowed.ID, Reason: "again", By: "r.okafor"}); !errors.Is(err, ErrLifted) {
		t.Fatalf("lifting twice must be refused, got %v", err)
	}

	ledger, err := s.Ledger(ctx, acme, &LedgerIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Suppressions) != 1 {
		t.Fatalf("a lift destroyed the record: %+v", ledger.Suppressions)
	}
	if ledger.Suppressions[0].By != "a.mensah" {
		t.Fatalf("the original decider did not survive the lift: %+v", ledger.Suppressions[0])
	}
	inForce, err := s.Ledger(ctx, acme, &LedgerIn{InForce: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inForce.Suppressions) != 0 {
		t.Fatalf("a lifted suppression is not in force: %+v", inForce.Suppressions)
	}
}

// TestClosedWindowRefused: a suppression that could never apply is an operator
// error, and accepting it hides the mistake until somebody wonders why the alerts
// never stopped.
func TestClosedWindowRefused(t *testing.T) {
	s := shelf(t)
	_, err := s.Suppress(context.Background(), acme, &SuppressIn{
		Rule: "r1", Reason: "r", By: "a", Until: noon.Add(-time.Hour),
	})
	if !errors.Is(err, ErrWindow) {
		t.Fatalf("a window that has already closed must be refused, got %v", err)
	}
}

// TestTenantIsolation over every operation, including the same org name under a
// second brand.
func TestTenantIsolation(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	mine := suppressed(t, s, acme, &SuppressIn{Rule: "r1", Kind: "account", Value: "acct-1"})

	for _, stranger := range []string{other, rival} {
		c, err := s.Cover(ctx, stranger, &CoverIn{Rule: "r1", Kind: "account", Value: "acct-1"})
		if err != nil {
			t.Fatal(err)
		}
		if c.Covered {
			t.Errorf("%s is covered by %s's suppression", stranger, acme)
		}
		ledger, err := s.Ledger(ctx, stranger, &LedgerIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(ledger.Suppressions) != 0 {
			t.Errorf("%s can read %s's suppressions", stranger, acme)
		}
		if _, err := s.Lift(ctx, stranger, &LiftIn{ID: mine.ID, Reason: "r", By: "b"}); !errors.Is(err, ErrNotHere) {
			t.Errorf("%s can lift %s's suppression: %v", stranger, acme, err)
		}
	}
	if c, _ := s.Cover(ctx, acme, &CoverIn{Rule: "r1", Kind: "account", Value: "acct-1"}); !c.Covered {
		t.Fatal("another tenant lifted this suppression")
	}
}

// TestBareOrgIsRefused at the boundary of every operation.
func TestBareOrgIsRefused(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	for _, bare := range []string{"acme", "", "unknown/acme"} {
		if _, err := s.Suppress(ctx, bare, &SuppressIn{Rule: "r1", Reason: "r", By: "a"}); err == nil {
			t.Errorf("Suppress accepted %q as a tenant", bare)
		}
		if _, err := s.Cover(ctx, bare, &CoverIn{Rule: "r1"}); err == nil {
			t.Errorf("Cover accepted %q as a tenant", bare)
		}
		if _, err := s.Ledger(ctx, bare, &LedgerIn{}); err == nil {
			t.Errorf("Ledger accepted %q as a tenant", bare)
		}
	}
}

// TestNothingDeletes: silence is governed, so the record of it is not disposable
// by the same knob that created it.
func TestNothingDeletes(t *testing.T) {
	for _, name := range []string{"suppress.go", "shelf.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), ".Delete(") {
			t.Errorf("%s calls Delete: disposal is pkg/retention's decision and nobody else's", name)
		}
	}
}

// TestRestart: a suppression that vanished on a rollout turns a governed silence
// into a surprise, and one that outlived its record is worse.
func TestRestart(t *testing.T) {
	first := instance.New(t)
	if err := Ensure(first); err != nil {
		t.Fatal(err)
	}
	before := NewBase(first)
	before.Now = func() time.Time { return noon }
	mine := suppressed(t, before, acme, &SuppressIn{Rule: "r1", Kind: "account", Value: "acct-1"})
	suppressed(t, before, other, &SuppressIn{Rule: "r1", Kind: "account", Value: "acct-1"})

	second := instance.Restart(t, first)
	if err := Ensure(second); err != nil {
		t.Fatal(err)
	}
	after := NewBase(second)
	after.Now = func() time.Time { return noon }

	c, err := after.Cover(context.Background(), acme, &CoverIn{Rule: "r1", Kind: "account", Value: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Covered || c.Suppression.ID != mine.ID {
		t.Fatalf("the suppression did not survive the restart: %+v", c)
	}
	if c.Suppression.Reason == "" || c.Suppression.By == "" {
		t.Fatalf("the decision behind it did not survive: %+v", c.Suppression)
	}

	ledger, err := after.Ledger(context.Background(), acme, &LedgerIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Suppressions) != 1 {
		t.Fatalf("the tenant boundary did not survive the restart: %+v", ledger.Suppressions)
	}
	if c, _ := after.Cover(context.Background(), rival, &CoverIn{Rule: "r1", Kind: "account", Value: "acct-1"}); c.Covered {
		t.Fatal("a third tenant is covered after the restart")
	}
}
