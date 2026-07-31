package retention

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/base/tests"
)

// TestRetainedRecordsSurviveARestart is the obligation stated as a test.
//
// A record has to be kept for five years after the relationship it sits inside
// ends (AMLR Art. 77(3)). The memory shelf keeps it until the process exits,
// which is not the same thing and is not an exception the law makes for a
// rollout — so the property worth asserting is not "the ledger can store a
// record" but "the bytes on disk still answer after the instance that wrote
// them is gone".
//
// The second app is a fresh instance over the first one's data directory: a new
// bootstrap, new handles, nothing carried over in memory. That is what a
// restarted pod reads.
func TestRetainedRecordsSurviveARestart(t *testing.T) {
	first, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("first instance: %v", err)
	}
	if err := Ensure(first); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	occurred := time.Now().UTC().Add(-time.Hour)
	id, err := NewBase(first).Retain(Record{
		Org:      "hanzo/acme",
		Class:    ClassRelationship,
		Trigger:  TriggerRelationshipEnd,
		Ref:      "rel-restart-1",
		Nature:   "correspondent banking",
		Parties:  []string{"pseudonym-a"},
		Occurred: occurred,
		Body:     []byte("sealed"),
	})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if id == "" {
		t.Fatal("retain returned no id")
	}

	// Take the bytes as they are on disk, then let the first instance go. The
	// copy is what survives the process, which is the whole question.
	dir := copyTree(t, first.DataDir())
	first.Cleanup()

	// The restart.
	second, err := tests.NewTestApp(dir)
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	t.Cleanup(second.Cleanup)

	answer, err := NewBase(second).Lookback(PurposeDisclosure, "hanzo/acme", "pseudonym-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("lookback after restart: %v", err)
	}
	if !answer.Maintained {
		t.Fatal("the relationship retained before the restart is not there after it — the record plane is not durable")
	}
	var found bool
	for _, r := range answer.Records {
		if r == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("record %q is not among %v after the restart", id, answer.Records)
	}
	if len(answer.Natures) == 0 || answer.Natures[0] != "correspondent banking" {
		t.Fatalf("the nature Art. 78 requires did not survive: %v", answer.Natures)
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
