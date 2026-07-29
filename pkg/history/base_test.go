package history

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tests"

	_ "github.com/hanzoai/base/migrations"
)

const org = "acme"

func kept(t *testing.T) *Base {
	t.Helper()
	built, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("test app: %v", err)
	}
	t.Cleanup(built.Cleanup)
	if err := Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return NewBase(core.App(built))
}

func window(t *testing.T, b *Base, kind, id string, lookback time.Duration) []Event {
	t.Helper()
	out, err := b.Window(context.Background(), Subject{OrgID: org, Kind: kind, ID: id}, lookback)
	if err != nil {
		t.Fatalf("Window(%s %q): %v", kind, id, err)
	}
	return out
}

// TestWindowIsBoundedByTheEventAndTheSubject: every behavioural measure is a
// function of this one query, so the query has to be right about three things at
// once — whose events, over what period, and in whose organisation. Each of them
// fails the same way if it is wrong: fewer events than there were, which reads as a
// customer who did less than they did.
func TestWindowIsBoundedByTheEventAndTheSubject(t *testing.T) {
	b := kept(t)
	now := time.Now().UTC()

	for _, e := range []Event{
		{ID: "tx-1", At: now.Add(-2 * time.Hour), USD: 100, Currency: "USD", Direction: In, User: "u1", Account: "a1", Device: "d1"},
		{ID: "tx-2", At: now.Add(-30 * time.Minute), USD: 200, Currency: "USD", Direction: Out, User: "u1", Account: "a1", Device: "d1"},
		{ID: "tx-3", At: now.Add(-90 * 24 * time.Hour), USD: 300, Currency: "USD", Direction: In, User: "u1", Account: "a2", Device: "d1"},
		{ID: "tx-4", At: now.Add(-time.Hour), USD: 400, Currency: "USD", Direction: In, User: "u2", Account: "a3", Device: "d1"},
	} {
		if err := b.Append(context.Background(), org, e); err != nil {
			t.Fatalf("Append(%s): %v", e.ID, err)
		}
	}

	// Most recent first, and the event outside the lookback is not in it.
	got := window(t, b, SubjectUser, "u1", 24*time.Hour)
	if len(got) != 2 {
		t.Fatalf("window held %d events, want 2: %+v", len(got), got)
	}
	if got[0].ID != "tx-2" || got[1].ID != "tx-1" {
		t.Errorf("window order = %s, %s, want tx-2 then tx-1", got[0].ID, got[1].ID)
	}
	if got[0].USD != 200 || got[0].Direction != Out || got[0].Account != "a1" || got[0].Device != "d1" {
		t.Errorf("event read back as %+v", got[0])
	}

	// A device is a subject too, which is what surfaces several nominally
	// unrelated customers acting as one.
	if held := window(t, b, SubjectDevice, "d1", 24*time.Hour); len(held) != 3 {
		t.Errorf("device window held %d events, want 3", len(held))
	}

	// The lookback is measured from the event's own timestamp, so widening it
	// reaches the older one.
	if held := window(t, b, SubjectUser, "u1", 365*24*time.Hour); len(held) != 3 {
		t.Errorf("a year's window held %d events, want 3", len(held))
	}

	// Another org's window over the same subject is empty.
	other, err := b.Window(context.Background(), Subject{OrgID: "beta", Kind: SubjectUser, ID: "u1"}, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("another org's window held %d of this org's events", len(other))
	}
}

// TestAppendRefusesAnEventWithNoOrg: an event that belongs to nobody would be read
// by nobody's window, so it is refused at the write rather than lost at the read.
func TestAppendRefusesAnEventWithNoOrg(t *testing.T) {
	b := kept(t)
	err := b.Append(context.Background(), "", Event{ID: "tx-1", At: time.Now().UTC(), USD: 100})
	if err == nil {
		t.Fatal("an event with no organisation was stored")
	}
}

// TestEnsureIsIdempotent: it runs on every start, and the second run must leave the
// collection the first one created alone.
func TestEnsureIsIdempotent(t *testing.T) {
	built, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("test app: %v", err)
	}
	t.Cleanup(built.Cleanup)

	if err := Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first, err := built.FindCollectionByNameOrId(Collection)
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if err := Ensure(built); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	second, err := built.FindCollectionByNameOrId(Collection)
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if len(second.Fields) != len(first.Fields) || len(second.Indexes) != len(first.Indexes) {
		t.Errorf("a second Ensure changed the collection: %d/%d fields, %d/%d indexes",
			len(second.Fields), len(first.Fields), len(second.Indexes), len(first.Indexes))
	}
}
