package cases

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/base/tests"

	"github.com/luxfi/aml/pkg/types"
)

// TestCasesSurviveARestart is the case plane's half of the record-keeping
// obligation, stated as a test.
//
// A case is the record that an alert was considered and what was decided, kept
// whichever way the decision went (AMLR Art. 77(1)(b); JMLSG 6.32). The timeline
// is the evidence of the work: who looked, what they found, what they concluded.
// Held in a map, all of it goes when the pod does — and an investigation that
// disappears on a rollout is not one anybody can be asked to stand behind.
//
// The second instance is a fresh app over the first one's bytes: new bootstrap,
// new handles, nothing carried in memory. That is what a restarted pod reads.
func TestCasesSurviveARestart(t *testing.T) {
	first, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("first instance: %v", err)
	}
	if err := Ensure(first); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	before := NewBase(first)
	opened := before.Create("hanzo/acme", types.SeverityCritical, []string{"al-1", "al-2"}, []string{"cust-1042"})
	if opened == nil {
		t.Fatal("Create returned no case")
	}
	if opened.Number != 1 {
		t.Fatalf("first case number = %d, want 1", opened.Number)
	}
	if err := before.AddEvent("hanzo/acme", opened.ID, types.CaseEvent{
		Kind:     types.EventNote,
		AuthorID: "a.mensah",
		Body:     "source of funds requested",
	}); err != nil {
		t.Fatalf("add note: %v", err)
	}

	dir := copyTree(t, first.DataDir())
	first.Cleanup()

	// The restart.
	second, err := tests.NewTestApp(dir)
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	t.Cleanup(second.Cleanup)
	after := NewBase(second)

	got := after.Get(opened.ID)
	if got == nil {
		t.Fatal("the case opened before the restart is not there after it — the case plane is not durable")
	}
	if got.OrgID != "hanzo/acme" || got.Severity != types.SeverityCritical || got.Status != types.CaseOpen {
		t.Fatalf("the case came back changed: %+v", got)
	}
	if len(got.AlertIDs) != 2 || got.AlertIDs[0] != "al-1" {
		t.Fatalf("the alerts the case was opened from did not survive: %v", got.AlertIDs)
	}

	timeline := after.Events(opened.ID)
	if len(timeline) != 1 || timeline[0].Body != "source of funds requested" {
		t.Fatalf("the timeline did not survive: %+v", timeline)
	}
	if timeline[0].AuthorID != "a.mensah" {
		t.Fatalf("the note lost its author, which is what makes it evidence: %+v", timeline[0])
	}

	// Tenancy still holds on the other side of the restart.
	if err := after.AddEvent("zoo/acme", opened.ID, types.CaseEvent{Kind: types.EventNote, Body: "x"}); err == nil {
		t.Fatal("another tenant wrote on this case after the restart")
	}
	if got := after.List("zoo/acme", ""); len(got) != 0 {
		t.Fatalf("another tenant listed this case after the restart: %v", got)
	}
	if got := after.List("hanzo/acme", ""); len(got) != 1 {
		t.Fatalf("the owner's list came back with %d cases, want 1", len(got))
	}

	// The numbering continues rather than starting again, or two cases share a
	// number and a case number is what a file is referred to by.
	next := after.Create("hanzo/acme", types.SeverityLow, nil, nil)
	if next == nil {
		t.Fatal("Create after the restart returned no case")
	}
	if next.Number != 2 {
		t.Fatalf("case number after the restart = %d, want 2", next.Number)
	}

	// And the decision that closes it is retained too.
	if err := after.Resolve("hanzo/acme", opened.ID, types.ResolutionFalsePositive, "a.mensah", "as-1"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	closed := after.Get(opened.ID)
	if closed.Status != types.CaseClosed || closed.Assessment != "as-1" {
		t.Fatalf("the resolution did not stick: %+v", closed)
	}
}

// copyTree copies a data directory as it stands, so a second instance can be
// opened over the same bytes after the first one is gone.
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("copy data dir: %v", err)
	}
	return dst
}
