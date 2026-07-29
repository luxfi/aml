package sanctions

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPublishedLists parses the real published files.
//
// It runs only when AML_LISTS points at a directory holding sdn.xml, un.xml,
// eu.xml and ofsi.csv as downloaded from the publishers. A fixture proves the
// parser handles the shapes we thought to write down; only the published file
// proves it handles the shapes the publisher actually emits, and every defect
// this work fixed in the previous parser was of the second kind.
func TestPublishedLists(t *testing.T) {
	dir := os.Getenv("AML_LISTS")
	if dir == "" {
		t.Skip("set AML_LISTS to a directory of published list files to run")
	}

	cases := []struct {
		file  string
		parse func([]byte) ([]Entry, error)
		least int
	}{
		{"sdn.xml", ParseOFAC, 19000},
		{"un.xml", ParseUN, 900},
		{"eu.xml", ParseEU, 5000},
		{"ofsi.csv", ParseOFSI, 4000},
	}

	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, c.file))
			if err != nil {
				t.Skipf("%v", err)
			}
			entries, err := c.parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(entries) < c.least {
				t.Fatalf("parsed %d entries, expected at least %d", len(entries), c.least)
			}

			var noName, withBirth, withDoc, withProgram, weak, addrs int
			kinds := map[string]int{}
			for _, e := range entries {
				if e.PrimaryName() == "" {
					noName++
				}
				if len(e.Births) > 0 {
					withBirth++
				}
				if len(e.Documents) > 0 {
					withDoc++
				}
				if len(e.Programs) > 0 {
					withProgram++
				}
				for _, n := range e.Names {
					if !n.Strong {
						weak++
					}
				}
				addrs += len(e.Chains())
				kinds[e.Kind]++
			}
			t.Logf("%s: %d entries, kinds=%v, births=%d, documents=%d, programmes=%d, weak names=%d, chain addresses=%d",
				c.file, len(entries), kinds, withBirth, withDoc, withProgram, weak, addrs)

			// A subject with no name at all cannot be screened. One is a parser bug.
			if noName > 0 {
				t.Errorf("%d entries parsed with no name — those subjects are unscreenable", noName)
			}
			if len(kinds) == 0 {
				t.Error("no subject kinds recorded")
			}
		})
	}
}
