package store

import (
	"testing"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tests"
	"github.com/hanzoai/dbx"

	_ "github.com/hanzoai/base/migrations"
)

func app(t *testing.T) core.App {
	t.Helper()
	built, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("test app: %v", err)
	}
	t.Cleanup(built.Cleanup)
	return built
}

// thing is a Kind to exercise the convention with. It deliberately does not declare
// Org: Ensure adds it, and a Kind that could omit it would be a collection whose
// records belong to nobody.
var thing = Kind{
	Name: "store_thing",
	Fields: []core.Field{
		&core.TextField{Name: "label", Required: true},
		&core.DateField{Name: "at", Required: true},
	},
	Indexes: []Index{
		{Name: "label", Fields: []string{Org, "label"}},
	},
}

func TestEnsureAddsTheTenantAndTheIndexes(t *testing.T) {
	built := app(t)
	if err := thing.Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	collection, err := built.FindCollectionByNameOrId(thing.Name)
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if collection.Fields.GetByName(Org) == nil {
		t.Error("the collection has no tenant field, so a record could belong to nobody")
	}
	if !indexed(collection.Indexes, thing.name(thing.Indexes[0])) {
		t.Errorf("index missing: %v", collection.Indexes)
	}
}

// TestEnsureIsIdempotentAndAdditive: Ensure runs on every start. A second run must
// change nothing, and a run of a declaration that has grown must add what is new
// without disturbing the records already there — a field the reader expects and the
// collection lacks reads as empty, which is indistinguishable from a record that
// never had it.
func TestEnsureIsIdempotentAndAdditive(t *testing.T) {
	built := app(t)
	if err := thing.Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	row, err := thing.New(built, "acme")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	row.Set("label", "before")
	row.Set("at", time.Now().UTC())
	if err := built.Save(row); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := thing.Ensure(built); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	grown := thing
	grown.Fields = append(append([]core.Field{}, thing.Fields...), &core.TextField{Name: "note"})
	grown.Indexes = append(append([]Index{}, thing.Indexes...), Index{Name: "at", Fields: []string{Org, "at"}})
	if err := grown.Ensure(built); err != nil {
		t.Fatalf("Ensure of a grown declaration: %v", err)
	}

	collection, err := built.FindCollectionByNameOrId(thing.Name)
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if collection.Fields.GetByName("note") == nil {
		t.Error("a field added to the declaration did not reach the collection")
	}
	if !indexed(collection.Indexes, grown.name(grown.Indexes[1])) {
		t.Errorf("an index added to the declaration did not reach the collection: %v", collection.Indexes)
	}

	kept, err := grown.Find(built, "acme", "label = {:label}", "", 0, dbx.Params{"label": "before"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("the record written before the collection grew is gone: %d records", len(kept))
	}
	if kept[0].GetString("note") != "" {
		t.Errorf("note = %q, want empty on a record written before the field existed", kept[0].GetString("note"))
	}
}

// TestQueryIsScopedToOneOrg: the tenant predicate is added by Find and not by the
// caller's filter, because a filter that forgets it reads another tenant's records
// and nothing about the result says so.
func TestQueryIsScopedToOneOrg(t *testing.T) {
	built := app(t)
	if err := thing.Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, org := range []string{"acme", "beta"} {
		row, err := thing.New(built, org)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		row.Set("label", "shared")
		row.Set("at", time.Now().UTC())
		if err := built.Save(row); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	mine, err := thing.Find(built, "acme", "label = {:label}", "", 0, dbx.Params{"label": "shared"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(mine) != 1 || mine[0].GetString(Org) != "acme" {
		t.Fatalf("a scoped query returned %d records: %v", len(mine), mine)
	}

	if _, err := thing.Find(built, "", "", "", 0, nil); err == nil {
		t.Error("an unscoped query was answered")
	}
	if _, err := thing.New(built, ""); err == nil {
		t.Error("a record with no organisation was built")
	}

	// Across is the deliberate exception, and the only one.
	every, err := thing.Across(built, "", "", 0, nil)
	if err != nil {
		t.Fatalf("Across: %v", err)
	}
	if len(every) != 2 {
		t.Errorf("Across saw %d records, want both orgs", len(every))
	}

	held, err := thing.Count(built)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if held != 2 {
		t.Errorf("Count = %d, want both orgs", held)
	}
}

// TestAMomentComparesAsAMoment: a date is stored in one layout and compared as
// text, so a filter parameter that is a Go time has to be converted before it is
// bound. Unconverted it does not error — it answers with the wrong rows, and the
// direction it is wrong in depends on the layout rather than on the data.
func TestAMomentComparesAsAMoment(t *testing.T) {
	built := app(t)
	if err := thing.Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	now := time.Now().UTC()
	for label, at := range map[string]time.Time{
		"old":    now.Add(-90 * time.Minute),
		"recent": now.Add(-30 * time.Minute),
		"later":  now.Add(30 * time.Minute),
	} {
		row, err := thing.New(built, "acme")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		row.Set("label", label)
		row.Set("at", at)
		if err := built.Save(row); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	since := now.Add(-time.Hour)
	within, err := thing.Find(built, "acme", "at >= {:since} && at <= {:now}", "at", 0, dbx.Params{"since": since, "now": now})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(within) != 1 || within[0].GetString("label") != "recent" {
		labels := make([]string, 0, len(within))
		for _, r := range within {
			labels = append(labels, r.GetString("label"))
		}
		t.Fatalf("the window matched %v, want [recent]", labels)
	}
}
