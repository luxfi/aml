package screen

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/sanctions"
)

var now = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func listed(name, source string) sanctions.Entry {
	return sanctions.Entry{
		List: source, RefID: "1", Group: "1", Kind: sanctions.Individual,
		Names: []sanctions.Name{{Full: name, Type: sanctions.Primary, Strong: true}},
	}
}

func TestUnloadedStoreRefusesRatherThanReportingClean(t *testing.T) {
	s := New(24*time.Hour, func() time.Time { return now })
	if err := s.Ready(); err == nil {
		t.Fatal("an unloaded store must not report ready")
	}
	if _, err := s.Search(sanctions.Query{Name: "Ivan Petrov"}, sanctions.Threshold); err == nil {
		t.Fatal("searching an unloaded store must fail, not return no matches")
	}
	// The engine path must fail too, so a rule reaches review rather than clearing.
	if _, err := s.Hit(context.Background(), "Ivan Petrov", engine.ClassSanctions); err == nil {
		t.Fatal("screening against an unloaded store must fail")
	}
}

func TestLoadedStoreScreens(t *testing.T) {
	s := New(24*time.Hour, func() time.Time { return now })
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(); err != nil {
		t.Fatalf("a freshly loaded store must be ready: %v", err)
	}
	hit, err := s.Hit(context.Background(), "Ivan Petrov", engine.ClassSanctions)
	if err != nil || !hit.Matched {
		t.Fatalf("a listed name must match: hit=%+v err=%v", hit, err)
	}
	hit, err = s.Hit(context.Background(), "Maria Gonzalez", engine.ClassSanctions)
	if err != nil {
		t.Fatalf("screening an unlisted name must succeed: %v", err)
	}
	if hit.Matched {
		t.Fatal("an unlisted name must not match")
	}
}

func TestRefusesToClearAListWithZeroEntries(t *testing.T) {
	// A publisher returning an empty file must not silently delete its
	// designations.
	s := New(24*time.Hour, func() time.Time { return now })
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(sanctions.OFAC, nil); err == nil {
		t.Fatal("loading zero entries must be refused")
	}
	if s.Len() != 1 {
		t.Fatalf("the previous entries must survive a refused load, got %d", s.Len())
	}
}

func TestOneSourceFailingLeavesTheOthersLoaded(t *testing.T) {
	s := New(24*time.Hour, func() time.Time { return now })
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(sanctions.UN, []sanctions.Entry{listed("Kim Jong Un", sanctions.UN)}); err != nil {
		t.Fatal(err)
	}
	// Replacing one source must not disturb the other.
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC), listed("New Name", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 3 {
		t.Fatalf("want 3 entries after replacing one source, got %d", s.Len())
	}
	hit, err := s.Hit(context.Background(), "Kim Jong Un", engine.ClassSanctions)
	if err != nil || !hit.Matched {
		t.Fatal("a source untouched by the reload must still screen")
	}
}

func TestStaleListsRefuseRatherThanReportClean(t *testing.T) {
	clock := now
	s := New(24*time.Hour, func() time.Time { return clock })
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(48 * time.Hour)
	if err := s.Ready(); err == nil {
		t.Fatal("a store whose newest load is two days old must not report ready at a one-day limit")
	}
	if _, err := s.Hit(context.Background(), "Maria Gonzalez", engine.ClassSanctions); err == nil {
		t.Fatal("screening against stale lists must fail rather than report no match")
	}
}

func TestPoliticalExposureIsRefusedNotAnswered(t *testing.T) {
	// There is no authoritative public list of politically exposed persons, and
	// none of the sanctions publishers is one. Answering from the sanctions lists
	// would report every customer as not politically exposed.
	s := New(24*time.Hour, func() time.Time { return now })
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	hit, err := s.Hit(context.Background(), "Ivan Petrov", engine.ClassPEP)
	if err == nil {
		t.Fatal("political exposure must be refused, not answered from sanctions data")
	}
	if hit.Matched {
		t.Fatal("a refused answer must not report a match")
	}
	if !strings.Contains(err.Error(), "politically") {
		t.Fatalf("the error must say what cannot be answered, got %v", err)
	}
}

func TestUnknownClassRefused(t *testing.T) {
	s := New(0, func() time.Time { return now })
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{listed("Ivan Petrov", sanctions.OFAC)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Hit(context.Background(), "Ivan Petrov", "watchlist"); err == nil {
		t.Fatal("an unknown screening class must be refused")
	}
}

func TestChainScreeningUsesTheSameStore(t *testing.T) {
	s := New(24*time.Hour, func() time.Time { return now })
	e := listed("Mixer Operator", sanctions.OFAC)
	e.Documents = []sanctions.Document{{Kind: sanctions.Address, Chain: "XBT", Number: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"}}
	if err := s.Load(sanctions.OFAC, []sanctions.Entry{e}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Chain("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa")
	if err != nil || len(m) != 1 {
		t.Fatalf("the designated address must match from the same store: %d matches, err %v", len(m), err)
	}
}

func TestSourcesReportsWhatIsLoaded(t *testing.T) {
	s := New(24*time.Hour, func() time.Time { return now })
	for _, src := range []string{sanctions.OFAC, sanctions.UN, sanctions.EU, sanctions.OFSI} {
		if err := s.Load(src, []sanctions.Entry{listed("Name "+src, src)}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Sources()
	if len(got) != 4 {
		t.Fatalf("want four loaded sources, got %d: %v", len(got), got)
	}
	for _, src := range []string{sanctions.OFAC, sanctions.UN, sanctions.EU, sanctions.OFSI} {
		if got[src].IsZero() {
			t.Errorf("source %q reports no load time", src)
		}
	}
}
