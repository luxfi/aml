package screen

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/sanctions"
)

// TestScreenAgainstPublishedLists loads the real published files into the store and
// screens against them, which is the only way to show that the whole path works:
// parse, load, readiness, match.
func TestScreenAgainstPublishedLists(t *testing.T) {
	dir := os.Getenv("AML_LISTS")
	if dir == "" {
		t.Skip("set AML_LISTS to a directory of published list files to run")
	}
	s := New(0, nil)

	for _, c := range []struct {
		file  string
		src   string
		parse func([]byte) ([]sanctions.Entry, error)
	}{
		{"sdn.xml", sanctions.OFAC, sanctions.ParseOFAC},
		{"un.xml", sanctions.UN, sanctions.ParseUN},
		{"eu.xml", sanctions.EU, sanctions.ParseEU},
		{"ofsi.csv", sanctions.OFSI, sanctions.ParseOFSI},
	} {
		data, err := os.ReadFile(filepath.Join(dir, c.file))
		if err != nil {
			t.Skipf("%v", err)
		}
		entries, err := c.parse(data)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if err := s.Load(c.src, entries); err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
	}

	if err := s.Ready(); err != nil {
		t.Fatalf("the store must be ready with four lists loaded: %v", err)
	}
	t.Logf("loaded %d designated subjects across %d lists", s.Len(), len(s.Sources()))

	// A designated subject must be found. This name is on the UN consolidated list
	// and is the case the previous engine could not detect under any circumstances.
	hit, err := s.Hit(context.Background(), "Eric Badege", engine.ClassSanctions)
	if err != nil {
		t.Fatalf("screening failed: %v", err)
	}
	if !hit.Matched {
		t.Fatal("a designated subject must be found in the loaded lists")
	}
	t.Logf("designated subject matched: list=%s ref=%s score=%.3f name=%q",
		hit.List, hit.EntryID, hit.Score, hit.Name)

	// A name that is not designated must not match, across the whole loaded set.
	for _, clean := range []string{"Jonathan Micklethwaite Harbourne", "Priya Venkataraman Rao"} {
		hit, err := s.Hit(context.Background(), clean, engine.ClassSanctions)
		if err != nil {
			t.Fatalf("screening %q failed: %v", clean, err)
		}
		if hit.Matched {
			t.Errorf("%q matched %q on %s at %.3f — a false positive against the full list set",
				clean, hit.Name, hit.List, hit.Score)
		}
	}

	// A designated blockchain address must be found by exact match.
	var probe sanctions.Document
	s.mu.RLock()
	for _, e := range s.entries {
		if ch := e.Chains(); len(ch) > 0 {
			probe = ch[0]
			break
		}
	}
	s.mu.RUnlock()
	if probe.Number == "" {
		t.Fatal("the loaded lists must carry at least one designated blockchain address")
	}
	m, err := s.Chain(probe.Number)
	if err != nil || len(m) == 0 {
		t.Fatalf("designated address %s (%s) must match: %d matches, err %v", probe.Number, probe.Chain, len(m), err)
	}
	t.Logf("designated address matched: chain=%s subject=%q", probe.Chain, m[0].Entry.PrimaryName())

	// And a nearby address must not.
	altered := probe.Number[:len(probe.Number)-1] + "Z"
	if m, _ := s.Chain(altered); len(m) != 0 {
		t.Errorf("an altered address must not match: %s", altered)
	}
	_ = time.Now
}
