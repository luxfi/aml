package sanctions

import (
	"errors"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

var at = time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)

func monitor() *Monitor { return NewMonitor(DefaultLists()) }

// A source that has never loaded must be reported and unfit, not absent. This is
// the state two of the four sources were in for months.
func TestNeverLoadedSourceIsReportedAndUnfit(t *testing.T) {
	m := monitor()

	sources := m.Sources()
	if len(sources) != 4 {
		t.Fatalf("got %d sources, want 4 — a source that never loaded must still be listed", len(sources))
	}
	for _, s := range sources {
		if s.Fresh(at) {
			t.Errorf("%s reports fresh having never loaded", s.Name)
		}
		if !s.LoadedAt.IsZero() {
			t.Errorf("%s has a load time having never loaded", s.Name)
		}
	}

	ready, unfit := m.Ready(at)
	if ready {
		t.Fatal("an instance that has loaded nothing reported itself ready to screen")
	}
	if len(unfit) != 4 {
		t.Fatalf("unfit = %v, want all four", unfit)
	}
}

// Screening against three of four lists is not three-quarters of a control: a
// party designated only on the missing list passes, and the answer looks clean.
func TestOneFailedSourceMakesTheInstanceUnready(t *testing.T) {
	m := monitor()
	m.Record([]Result{
		{Source: types.ListOFACSDN, Entries: 19175, SHA256: "aa"},
		{Source: types.ListUN, Entries: 1011, SHA256: "bb"},
		{Source: types.ListEU, Entries: 6017, SHA256: "cc"},
		{Source: types.ListHMT, Err: errors.New("HTTP 404 from ConList.xml")},
	}, at)

	ready, unfit := m.Ready(at)
	if ready {
		t.Fatal("three of four lists loaded and the instance called itself ready")
	}
	if len(unfit) != 1 || unfit[0] != types.ListHMT {
		t.Fatalf("unfit = %v, want [%s]", unfit, types.ListHMT)
	}

	// The failure must be legible, not just counted.
	for _, s := range m.Sources() {
		if s.Name != types.ListHMT {
			continue
		}
		if s.Err == "" {
			t.Error("the failed source carries no reason")
		}
		if s.Entries != 0 {
			t.Errorf("a source that never loaded reports %d entries", s.Entries)
		}
	}
}

// A later failure must not erase what is stored: screening still runs against the
// last good load, so reporting zero entries would misdescribe the system.
func TestFailureKeepsThePreviousLoadButLetsItAge(t *testing.T) {
	m := monitor()
	m.Record([]Result{{Source: types.ListEU, Entries: 6017, SHA256: "cc"}}, at)
	m.Record([]Result{{Source: types.ListEU, Err: errors.New("403")}}, at.Add(24*time.Hour))

	var eu Source
	for _, s := range m.Sources() {
		if s.Name == types.ListEU {
			eu = s
		}
	}
	if eu.Entries != 6017 {
		t.Errorf("entries = %d, want the last good load's 6017", eu.Entries)
	}
	if !eu.LoadedAt.Equal(at) {
		t.Errorf("loaded_at = %v, want the last SUCCESSFUL load %v", eu.LoadedAt, at)
	}
	if eu.Err == "" {
		t.Error("the current failure is not reported")
	}
	// Inside maxListAge the stored data is still usable...
	if !eu.Fresh(at.Add(24 * time.Hour)) {
		t.Error("a day-old load with a failing refresh reported unfit; stored designations still screen")
	}
	// ...but it must not stay fresh forever on one old success.
	if eu.Fresh(at.Add(30 * 24 * time.Hour)) {
		t.Error("a month-old load still reports fresh")
	}
}

// A load that parses to zero entries is not a fit source, even though the fetch
// succeeded. This is the shape of a schema change.
func TestZeroEntryLoadIsNotFresh(t *testing.T) {
	s := Source{Name: types.ListUN, Entries: 0, LoadedAt: at}
	if s.Fresh(at) {
		t.Fatal("a source that loaded zero entries reported fit to screen against")
	}
}

func TestAgeIsMeasuredFromTheLastSuccessfulLoad(t *testing.T) {
	m := monitor()
	m.Record([]Result{{Source: types.ListUN, Entries: 1011}}, at)

	for _, s := range m.Sources() {
		if s.Name != types.ListUN {
			continue
		}
		if got := s.Age(at.Add(6 * time.Hour)); got != 6*time.Hour {
			t.Fatalf("age = %v, want 6h", got)
		}
	}
}
