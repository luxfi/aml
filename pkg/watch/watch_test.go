package watch

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/types"
)

const (
	acme  = "hanzo/acme"
	rival = "hanzo/rival"
	// other is the SAME org name under a different brand.
	other = "zoo/acme"
)

var noon = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func shelf(t *testing.T) (*Shelf, *suppress.Shelf) {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := suppress.Ensure(app); err != nil {
		t.Fatalf("ensure suppress: %v", err)
	}
	silence := suppress.NewBase(app)
	silence.Now = func() time.Time { return noon }
	s := NewBase(app)
	s.Cover = silence
	s.Now = func() time.Time { return noon }
	return s, silence
}

func fired(t *testing.T, s *Shelf, org, rule, account string, at time.Time) *Activation {
	t.Helper()
	a, err := s.Record(context.Background(), org, &RecordIn{
		Rule: rule, RuleName: "structuring", Severity: types.SeverityHigh,
		Action: types.ActionReview, Tx: "tx-" + at.Format(time.RFC3339Nano),
		Subject: Subject{Kind: "account", Value: account}, At: at,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	return a
}

// TestActivationRefusalsAreRefusals: an activation with no rule or no subject has
// nothing it could be about, and recording it against an empty subject would pool
// every anonymous detection in the tenant under one imaginary customer.
func TestActivationRefusalsAreRefusals(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	base := RecordIn{Rule: "r1", Action: types.ActionReview, Subject: Subject{Kind: "account", Value: "acct-1"}}

	no := base
	no.Rule = ""
	if _, err := s.Record(ctx, acme, &no); !errors.Is(err, ErrRule) {
		t.Errorf("an activation with no rule must be refused, got %v", err)
	}
	no = base
	no.Subject.Value = ""
	if _, err := s.Record(ctx, acme, &no); !errors.Is(err, ErrSubject) {
		t.Errorf("an activation with no subject must be refused, got %v", err)
	}
	no = base
	no.Subject.Kind = "wallet"
	if _, err := s.Record(ctx, acme, &no); !errors.Is(err, ErrKind) {
		t.Errorf("an unknown subject kind must be refused, got %v", err)
	}
	no = base
	no.Action = "quarantine"
	if _, err := s.Record(ctx, acme, &no); !errors.Is(err, ErrAction) {
		t.Errorf("an unknown action must be refused, got %v", err)
	}
}

// TestSuppressedIsRecordedNotDropped is the whole doctrine of this package.
//
// A detection that a suppression covers is written, marked, and named with the
// suppression that covered it. Dropped instead, the institution could not say how
// much it is not showing, and "no alerts" would mean either a quiet institution or
// a silent control with no way to tell which.
func TestSuppressedIsRecordedNotDropped(t *testing.T) {
	s, silence := shelf(t)
	ctx := context.Background()
	sup, err := silence.Suppress(ctx, acme, suppressFor("r1", "acct-1"))
	if err != nil {
		t.Fatal(err)
	}

	a := fired(t, s, acme, "r1", "acct-1", noon)
	if !a.Suppressed || a.Cause != CauseSuppressed || a.By != sup.ID {
		t.Fatalf("a covered activation must be marked and must name the suppression: %+v", a)
	}
	if a.Action != types.ActionReview || a.Response != types.ActionAllow {
		t.Fatalf("Action is what the rule asked for and Response what happened: %+v", a)
	}

	feed, err := s.Feed(ctx, acme, &FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Fatalf("a suppressed activation must still be in the record: %+v", feed.Activations)
	}
	live, err := s.Feed(ctx, acme, &FeedIn{Live: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Activations) != 0 {
		t.Fatalf("a suppressed activation did not reach a queue: %+v", live.Activations)
	}

	// The volume a suppression silences is a query, which is why nothing keeps a
	// counter of it.
	rates, err := s.Rates(ctx, acme, &RatesIn{Since: noon.Add(-time.Hour), Until: noon.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rates.Rules) != 1 || rates.Rules[0].Silenced != 1 || rates.Rules[0].Live != 0 {
		t.Fatalf("the rates must show the silence: %+v", rates.Rules)
	}
}

// suppressFor is a narrow suppression carrying the decision every one of them
// carries.
func suppressFor(rule, account string) *suppress.SuppressIn {
	return &suppress.SuppressIn{
		Rule: rule, Kind: "account", Value: account,
		Reason: "settlement pattern reviewed 2026-02", By: "a.mensah",
	}
}

// TestFoldMarksTheRepeat. Folding is a declared policy, not a default: with no
// rung, every activation is live.
func TestFoldMarksTheRepeat(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()

	first := fired(t, s, acme, "r1", "acct-1", noon)
	second := fired(t, s, acme, "r1", "acct-1", noon.Add(time.Minute))
	if first.Suppressed || second.Suppressed {
		t.Fatal("with no rung declared, nothing folds — silence must be a decision")
	}

	if _, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: Fold,
		Reason: "one alert per account per hour is enough to act on", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}

	third := fired(t, s, acme, "r1", "acct-1", noon.Add(2*time.Minute))
	if !third.Suppressed || third.Cause != CauseDuplicate {
		t.Fatalf("a repeat inside the window must be marked a duplicate: %+v", third)
	}
	if third.By != second.ID {
		t.Fatalf("a duplicate must name the alert a reviewer already has: by=%s want %s", third.By, second.ID)
	}
	if third.Response != types.ActionAllow {
		t.Fatalf("a folded activation reaches no queue: %+v", third)
	}

	// Past the window it is not a repeat.
	later := fired(t, s, acme, "r1", "acct-1", noon.Add(2*time.Hour))
	if later.Suppressed {
		t.Fatalf("an activation past the fold window is not a duplicate: %+v", later)
	}
	// And another account was never part of the streak.
	elsewhere := fired(t, s, acme, "r1", "acct-2", noon.Add(3*time.Minute))
	if elsewhere.Suppressed {
		t.Fatalf("a different subject is not a repeat: %+v", elsewhere)
	}
}

// TestElevationRaisesAndBeatsFolding.
func TestElevationRaisesAndBeatsFolding(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	if _, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: Fold,
		Reason: "one per hour", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}
	rung, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 3, Within: Span(time.Hour), To: types.ActionBlock,
		Reason: "three in an hour on one account is a decision the board has taken", By: "a.mensah",
	})
	if err != nil {
		t.Fatal(err)
	}

	fired(t, s, acme, "r1", "acct-1", noon)
	folded := fired(t, s, acme, "r1", "acct-1", noon.Add(time.Minute))
	if !folded.Suppressed || folded.Cause != CauseDuplicate {
		t.Fatalf("the second must fold: %+v", folded)
	}
	third := fired(t, s, acme, "r1", "acct-1", noon.Add(2*time.Minute))
	if third.Suppressed {
		t.Fatalf("an activation that reached an escalation rung is the escalation, not a repeat: %+v", third)
	}
	if third.Response != types.ActionBlock || third.Rung != rung.ID {
		t.Fatalf("the third must be raised and must name the rung: %+v", third)
	}
	if third.Streak != 3 {
		t.Fatalf("streak = %d, want 3", third.Streak)
	}
}

// TestARungMayNotLowerAResponse. Lowering is a suppression by another name, and a
// suppression is asked for a reason and a decider on the decision itself.
func TestARungMayNotLowerAResponse(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	if _, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: types.ActionAllow,
		Reason: "r", By: "a",
	}); !errors.Is(err, ErrTo) {
		t.Fatalf("a rung lowering a response must be refused, got %v", err)
	}

	// And a rung naming a weaker action than the rule asked for leaves it alone.
	if _, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: types.ActionFlag,
		Reason: "r", By: "a",
	}); err != nil {
		t.Fatal(err)
	}
	fired(t, s, acme, "r1", "acct-1", noon)
	second := fired(t, s, acme, "r1", "acct-1", noon.Add(time.Minute))
	if second.Response != types.ActionReview {
		t.Fatalf("a rung must never weaken what the rule asked for: %+v", second)
	}
}

// TestRungRefusals.
func TestRungRefusals(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	good := DeclareIn{Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: Fold, Reason: "r", By: "a"}

	bad := good
	bad.Count = 1
	if _, err := s.Declare(ctx, acme, &bad); !errors.Is(err, ErrCount) {
		t.Errorf("a rung counting one is not about repetition, got %v", err)
	}
	bad = good
	bad.Within = 0
	if _, err := s.Declare(ctx, acme, &bad); !errors.Is(err, ErrWithin) {
		t.Errorf("a rung needs a window, got %v", err)
	}
	bad = good
	bad.Reason = ""
	if _, err := s.Declare(ctx, acme, &bad); !errors.Is(err, ErrReason) {
		t.Errorf("a rung needs a reason, got %v", err)
	}
	bad = good
	bad.By = ""
	if _, err := s.Declare(ctx, acme, &bad); !errors.Is(err, ErrDecider) {
		t.Errorf("a rung needs a decider, got %v", err)
	}
}

// TestRetiringARungKeepsIt.
func TestRetiringARungKeepsIt(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	rung, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: Fold, Reason: "r", By: "a.mensah",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retire(ctx, acme, &RetireIn{ID: rung.ID, Reason: "volumes fell", By: "r.okafor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retire(ctx, acme, &RetireIn{ID: rung.ID, Reason: "again", By: "r.okafor"}); !errors.Is(err, ErrRetired) {
		t.Fatalf("retiring twice must be refused, got %v", err)
	}

	all, err := s.Ladder(ctx, acme, &LadderIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Rungs) != 1 || all.Rungs[0].RetiredBy != "r.okafor" {
		t.Fatalf("a retired rung stays, and names who retired it: %+v", all.Rungs)
	}
	inForce, err := s.Ladder(ctx, acme, &LadderIn{InForce: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inForce.Rungs) != 0 {
		t.Fatalf("a retired rung is not in force: %+v", inForce.Rungs)
	}
	fired(t, s, acme, "r1", "acct-1", noon)
	second := fired(t, s, acme, "r1", "acct-1", noon.Add(time.Minute))
	if second.Suppressed {
		t.Fatalf("a retired rung must not still fold: %+v", second)
	}
}

// TestFeedPagesForwardWithoutSkipping. A poller carries Through forward, so a page
// boundary must not lose the activation that sat on it.
func TestFeedPagesForwardWithoutSkipping(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		fired(t, s, acme, "r1", "acct-1", noon.Add(time.Duration(i)*time.Minute))
	}
	seen := map[string]bool{}
	since := time.Time{}
	for range 5 {
		page, err := s.Feed(ctx, acme, &FeedIn{Since: since, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Activations) == 0 {
			break
		}
		for _, a := range page.Activations {
			if seen[a.ID] {
				t.Fatalf("the feed handed out %s twice", a.ID)
			}
			seen[a.ID] = true
		}
		since = page.Through
	}
	if len(seen) != 5 {
		t.Fatalf("the feed skipped activations: saw %d of 5", len(seen))
	}
}

// TestLiveFeedIsLossyAndSaysSo. A blocking send would let one slow console pace
// the ingest path; a silent drop would let it report calm.
func TestLiveFeedIsLossyAndSaysSo(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	ch, close := s.Subscribe(acme)
	defer close()

	for i := 0; i < Buffer+5; i++ {
		fired(t, s, acme, "r1", "acct-1", noon.Add(time.Duration(i)*time.Second))
	}
	if got := s.Dropped(acme); got == 0 {
		t.Fatal("a subscriber that never read must have been dropped from, and the drops counted")
	}
	feed, err := s.Feed(ctx, acme, &FeedIn{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if feed.Dropped == 0 {
		t.Fatal("the drop count must travel on the poll, because the monitor reading it is who needs to know")
	}
	// The durable plane lost nothing.
	all, err := s.Feed(ctx, acme, &FeedIn{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Activations) != Buffer+5 {
		t.Fatalf("the record is the rows, not the channel: %d of %d", len(all.Activations), Buffer+5)
	}
	if len(ch) == 0 {
		t.Fatal("the subscriber should hold a full buffer")
	}
}

// TestFeedIsPerTenant: a subscription names its tenant and receives that tenant's
// activations and nothing else.
func TestFeedIsPerTenant(t *testing.T) {
	s, _ := shelf(t)
	ch, close := s.Subscribe(acme)
	defer close()

	fired(t, s, other, "r1", "acct-1", noon)
	fired(t, s, rival, "r1", "acct-1", noon)
	mine := fired(t, s, acme, "r1", "acct-1", noon)

	select {
	case got := <-ch:
		if got.ID != mine.ID || got.Org != acme {
			t.Fatalf("a subscriber received another tenant's activation: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the subscriber received nothing")
	}
	select {
	case got := <-ch:
		t.Fatalf("the subscriber received a second activation it should not have: %+v", got)
	default:
	}
}

// TestTenantIsolation over the reads and the policy writes.
func TestTenantIsolation(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	fired(t, s, acme, "r1", "acct-1", noon)
	rung, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: Fold, Reason: "r", By: "a",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, stranger := range []string{other, rival} {
		feed, err := s.Feed(ctx, stranger, &FeedIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(feed.Activations) != 0 {
			t.Errorf("%s can read %s's activations", stranger, acme)
		}
		rates, err := s.Rates(ctx, stranger, &RatesIn{Since: noon.Add(-time.Hour), Until: noon.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rates.Rules) != 0 {
			t.Errorf("%s can read %s's rates", stranger, acme)
		}
		ladder, err := s.Ladder(ctx, stranger, &LadderIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(ladder.Rungs) != 0 {
			t.Errorf("%s can read %s's rungs", stranger, acme)
		}
		if _, err := s.Retire(ctx, stranger, &RetireIn{ID: rung.ID, Reason: "r", By: "b"}); !errors.Is(err, ErrNotHere) {
			t.Errorf("%s can retire %s's rung: %v", stranger, acme, err)
		}
	}

	// A streak is per tenant: another brand's activations on the same subject do
	// not count towards this one's fold.
	fired(t, s, other, "r1", "acct-1", noon.Add(time.Minute))
	fired(t, s, other, "r1", "acct-1", noon.Add(2*time.Minute))
	mine := fired(t, s, acme, "r1", "acct-1", noon.Add(3*time.Minute))
	if mine.Streak != 2 {
		t.Fatalf("a streak counted another tenant's activations: streak=%d, want 2", mine.Streak)
	}
}

// TestBareOrgIsRefused at the boundary of every operation.
func TestBareOrgIsRefused(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	for _, bare := range []string{"acme", "", "unknown/acme"} {
		in := RecordIn{Rule: "r1", Action: types.ActionReview, Subject: Subject{Kind: "account", Value: "a"}}
		if _, err := s.Record(ctx, bare, &in); err == nil {
			t.Errorf("Record accepted %q as a tenant", bare)
		}
		if _, err := s.Feed(ctx, bare, &FeedIn{}); err == nil {
			t.Errorf("Feed accepted %q as a tenant", bare)
		}
		if _, err := s.Rates(ctx, bare, &RatesIn{}); err == nil {
			t.Errorf("Rates accepted %q as a tenant", bare)
		}
	}
}

// TestSpanCarriesItsUnit. A window written as a bare number is off by three orders
// of magnitude and nothing errors — the rung simply never matches.
func TestSpanCarriesItsUnit(t *testing.T) {
	var s Span
	if err := s.UnmarshalJSON([]byte("3600")); err == nil {
		t.Fatal("a window with no unit must be refused")
	}
	if err := s.UnmarshalJSON([]byte(`"1h30m"`)); err != nil {
		t.Fatalf("a window with its unit must be accepted: %v", err)
	}
	if s.Duration() != 90*time.Minute {
		t.Fatalf("span = %v, want 1h30m", s.Duration())
	}
	raw, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"1h30m0s"` {
		t.Fatalf("a span writes as a duration, got %s", raw)
	}
}

// TestNothingDeletes.
func TestNothingDeletes(t *testing.T) {
	for _, name := range []string{"watch.go", "shelf.go", "feed.go", "span.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), ".Delete(") {
			t.Errorf("%s calls Delete: disposal is pkg/retention's decision and nobody else's", name)
		}
	}
}

// TestRestart: the activation plane is the evidence that a control ran.
func TestRestart(t *testing.T) {
	first := instance.New(t)
	if err := Ensure(first); err != nil {
		t.Fatal(err)
	}
	if err := suppress.Ensure(first); err != nil {
		t.Fatal(err)
	}
	silence := suppress.NewBase(first)
	silence.Now = func() time.Time { return noon }
	before := NewBase(first)
	before.Cover = silence
	before.Now = func() time.Time { return noon }

	if _, err := silence.Suppress(context.Background(), acme, suppressFor("r2", "acct-9")); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Declare(context.Background(), acme, &DeclareIn{
		Rule: "r1", Kind: "account", Count: 2, Within: Span(time.Hour), To: types.ActionBlock,
		Reason: "board decision", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}
	live := fired(t, before, acme, "r1", "acct-1", noon)
	fired(t, before, other, "r1", "acct-1", noon)

	second := instance.Restart(t, first)
	if err := Ensure(second); err != nil {
		t.Fatal(err)
	}
	if err := suppress.Ensure(second); err != nil {
		t.Fatal(err)
	}
	after := NewBase(second)
	afterSilence := suppress.NewBase(second)
	afterSilence.Now = func() time.Time { return noon }
	after.Cover = afterSilence
	after.Now = func() time.Time { return noon }

	feed, err := after.Feed(context.Background(), acme, &FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 || feed.Activations[0].ID != live.ID {
		t.Fatalf("the activation did not survive the restart: %+v", feed.Activations)
	}
	if feed.Activations[0].Action != types.ActionReview || feed.Activations[0].RuleName != "structuring" {
		t.Fatalf("the activation came back changed: %+v", feed.Activations[0])
	}

	// The rung is still in force: the streak continues rather than restarting, so
	// the second activation after a rollout is the second and not the first.
	next := fired(t, after, acme, "r1", "acct-1", noon.Add(time.Minute))
	if next.Streak != 2 || next.Response != types.ActionBlock {
		t.Fatalf("the declared escalation did not survive the restart: %+v", next)
	}

	// And a suppression declared before the restart still covers after it.
	covered, err := after.Record(context.Background(), acme, &RecordIn{
		Rule: "r2", Action: types.ActionReview, Subject: Subject{Kind: "account", Value: "acct-9"}, At: noon,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !covered.Suppressed || covered.Cause != CauseSuppressed {
		t.Fatalf("the suppression did not survive the restart: %+v", covered)
	}

	// Tenancy still holds.
	if f, _ := after.Feed(context.Background(), rival, &FeedIn{}); len(f.Activations) != 0 {
		t.Fatal("a third tenant can read activations after the restart")
	}
}

// TestARetriedFiringIsOneActivation.
//
// Ingest writes a transaction, then its alerts, then their activations. Anything
// after the first write that fails answers 503, and a client that retries a 503
// offers the same firings again. With a fresh id each time the retry writes a
// SECOND row for the same firing, and the second row is counted in the streak —
// so a repetition policy fires on a repeat that never happened and the rates
// report a volume the institution did not have.
func TestARetriedFiringIsOneActivation(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	offer := func() *Activation {
		a, err := s.Record(ctx, acme, &RecordIn{
			Rule: "ctr", Action: types.ActionReport, Tx: "tx-1",
			Subject: Subject{Kind: "account", Value: "acct-1"}, At: noon,
		})
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		return a
	}

	first := offer()
	again := offer()
	if first.ID != again.ID {
		t.Fatalf("the same firing was recorded twice: %s and %s", first.ID, again.ID)
	}
	if again.Streak != first.Streak {
		t.Fatalf("the retry moved the streak from %d to %d", first.Streak, again.Streak)
	}

	feed, err := s.Feed(ctx, acme, &FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Fatalf("%d rows for one firing: %+v", len(feed.Activations), feed.Activations)
	}
	rates, err := s.Rates(ctx, acme, &RatesIn{Since: noon.Add(-time.Hour), Until: noon.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rates.Rules) != 1 || rates.Rules[0].Fired != 1 {
		t.Fatalf("the rate counted the retry: %+v", rates.Rules)
	}
}

// TestARetryDoesNotFoldAgainstItself. The sharpest form of the double count: a
// fold rung reads the streak, so a duplicated row makes the retry of the FIRST
// firing look like a repeat of itself and silences it.
func TestARetryDoesNotFoldAgainstItself(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	if _, err := s.Declare(ctx, acme, &DeclareIn{
		Rule: "ctr", Kind: "account", Count: 2, Within: Span(time.Hour), To: Fold,
		Reason: "one alert an hour per account is enough", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}

	in := RecordIn{
		Rule: "ctr", Action: types.ActionReport, Tx: "tx-1",
		Subject: Subject{Kind: "account", Value: "acct-1"}, At: noon,
	}
	first, err := s.Record(ctx, acme, &in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Suppressed {
		t.Fatalf("the first firing folded against nothing: %+v", first)
	}
	retry, err := s.Record(ctx, acme, &in)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Suppressed || retry.Cause == CauseDuplicate {
		t.Fatalf("a retry of one firing folded against itself, so the alert went quiet: %+v", retry)
	}

	// A genuinely different transaction on the same account still folds, so the
	// rung is not broken by the fix.
	second := in
	second.Tx, second.At = "tx-2", noon.Add(time.Minute)
	repeat, err := s.Record(ctx, acme, &second)
	if err != nil {
		t.Fatal(err)
	}
	if !repeat.Suppressed || repeat.Cause != CauseDuplicate {
		t.Fatalf("a real repeat must still fold: %+v", repeat)
	}
}

// TestAFiringIdentityIsPerTenant. The tenant is in the hash and it is first, so
// two institutions recording the same transaction id, rule and account do not
// collide — and neither can read or overwrite the other's row.
func TestAFiringIdentityIsPerTenant(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	in := RecordIn{
		Rule: "ctr", Action: types.ActionReport, Tx: "tx-1",
		Subject: Subject{Kind: "account", Value: "acct-1"}, At: noon,
	}
	mine, err := s.Record(ctx, acme, &in)
	if err != nil {
		t.Fatal(err)
	}
	// The same org name under another brand, which is the collision the tenant
	// key exists to prevent, arrived at through the activation id.
	theirs, err := s.Record(ctx, other, &in)
	if err != nil {
		t.Fatal(err)
	}
	if mine.ID == theirs.ID {
		t.Fatal("two tenants' firings share an id: one institution's retry would return the other's row")
	}
	for _, tc := range []struct{ org, id string }{{acme, theirs.ID}, {other, mine.ID}} {
		held, err := s.activation(tc.org, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if held != nil {
			t.Fatalf("%s read another tenant's activation by id: %+v", tc.org, held)
		}
	}
	// And the id carries no customer data, however the parts were written.
	if id := firing(acme, "tx-1", "ctr", Subject{Kind: "account", Value: "GB29NWBK60161331926819"}); strings.Contains(id, "GB29") {
		t.Fatalf("the activation id carries the subject in the clear: %s", id)
	}
}

// TestAnActivationWithNoTransactionKeepsItsOwnIdentity. An operator recording a
// detection by hand has no natural key, and two of them are two events.
func TestAnActivationWithNoTransactionKeepsItsOwnIdentity(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	in := RecordIn{Rule: "manual", Action: types.ActionReview,
		Subject: Subject{Kind: "account", Value: "acct-1"}, At: noon}
	first, err := s.Record(ctx, acme, &in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Record(ctx, acme, &in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two hand-recorded detections were collapsed into one")
	}
}

// TestARungIsBounded.
//
// A rung's Count becomes the LIMIT of a read on the ingest path and its Within
// becomes that read's window, so an unbounded declaration is an unbounded read
// per activation — asked for by one operator request, paid for by every payment.
func TestARungIsBounded(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	base := DeclareIn{
		Rule: "ctr", Kind: "account", Count: 2, Within: Span(time.Hour), To: types.ActionBlock,
		Reason: "two in an hour", By: "a.mensah",
	}

	deep := base
	deep.Count = MaxCount + 1
	if _, err := s.Declare(ctx, acme, &deep); !errors.Is(err, ErrCount) {
		t.Fatalf("a rung counting past the bound: %v, want ErrCount", err)
	}
	huge := base
	huge.Count = 10_000_000
	if _, err := s.Declare(ctx, acme, &huge); !errors.Is(err, ErrCount) {
		t.Fatalf("a rung counting ten million: %v, want ErrCount", err)
	}
	wide := base
	wide.Within = MaxWithin + Span(time.Second)
	if _, err := s.Declare(ctx, acme, &wide); !errors.Is(err, ErrWithin) {
		t.Fatalf("a rung spanning past the bound: %v, want ErrWithin", err)
	}
	// The bounds themselves are declarable, so the refusals above are about being
	// PAST them and not about the whole range being refused.
	at := base
	at.Count, at.Within = MaxCount, MaxWithin
	if _, err := s.Declare(ctx, acme, &at); err != nil {
		t.Fatalf("a rung at the bound must be declarable: %v", err)
	}
}

// TestTheStreakReadIsBoundedWhateverTheRowsSay.
//
// A bound introduced after a row was written does not reach that row, and the
// ingest path's guarantee cannot rest on what is in the store. This is the
// arithmetic that turns rungs into a LIMIT and a window, tested directly.
func TestTheStreakReadIsBoundedWhateverTheRowsSay(t *testing.T) {
	widest, deepest := bounds([]Rung{
		{Count: 3, Within: Span(time.Hour)},
		{Count: 10_000_000, Within: Span(4000 * 24 * time.Hour)}, // a row from before the bound
	})
	if deepest != MaxCount {
		t.Fatalf("the read would ask for %d rows, want at most %d", deepest, MaxCount)
	}
	if widest != MaxWithin.Duration() {
		t.Fatalf("the read would span %s, want at most %s", widest, MaxWithin)
	}
	// And ordinary rungs are untouched: the clamp is a ceiling, not a floor.
	widest, deepest = bounds([]Rung{{Count: 3, Within: Span(time.Hour)}, {Count: 5, Within: Span(2 * time.Hour)}})
	if deepest != 5 || widest != 2*time.Hour {
		t.Fatalf("widest=%s deepest=%d, want the rungs' own", widest, deepest)
	}
}

// TestAnIncompleteCoverIsMarkedAndNeverRefusesIngest.
//
// The suppression plane answers over a bounded page when a tenant has crowded one
// rule. That is an answer and the row says so: "not covered" then means "none was
// found among those read", which is weaker, and the difference is the sort of
// thing a monitoring plane must not absorb silently.
func TestAnIncompleteCoverIsMarkedAndNeverRefusesIngest(t *testing.T) {
	s, _ := shelf(t)
	s.Cover = partial{}
	a, err := s.Record(context.Background(), acme, &RecordIn{
		Rule: "ctr", Action: types.ActionReport, Tx: "tx-1",
		Subject: Subject{Kind: "account", Value: "acct-1"}, At: noon,
	})
	if err != nil {
		t.Fatalf("an incomplete cover check must not fail the ingest path: %v", err)
	}
	if a.Suppressed || a.Response != types.ActionReport {
		t.Fatalf("an unfound suppression must produce noise and not silence: %+v", a)
	}
	if !a.Unchecked {
		t.Fatal("the row does not record that its suppression check was incomplete")
	}

	// An ordinary complete answer carries no mark, so the mark means something.
	s.Cover = nil
	b, err := s.Record(context.Background(), acme, &RecordIn{
		Rule: "ctr", Action: types.ActionReport, Tx: "tx-2",
		Subject: Subject{Kind: "account", Value: "acct-1"}, At: noon,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Unchecked {
		t.Fatalf("an activation with no crowding was marked: %+v", b)
	}
}

// partial is a suppression plane whose answer is over a page of the candidates.
type partial struct{}

func (partial) Cover(context.Context, string, *suppress.CoverIn) (*suppress.Cover, error) {
	return &suppress.Cover{Partial: true}, nil
}
