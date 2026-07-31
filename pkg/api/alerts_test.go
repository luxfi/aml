package api

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/base/tests"

	"github.com/luxfi/aml/pkg/types"
)

// TestAlertsSurviveARestart closes the last of the record that was in memory.
//
// An alert is what a rule said about a transaction at the time it was judged. A
// case cites it, and the replay that measures a rule's false-positive rate reads
// it to know which rules actually fired — so an alert store that empties on a
// rollout leaves a case naming evidence that is no longer there.
func TestAlertsSurviveARestart(t *testing.T) {
	first, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("first instance: %v", err)
	}
	if err := EnsureAlerts(first); err != nil {
		t.Fatalf("EnsureAlerts: %v", err)
	}

	raised := time.Now().UTC().Add(-time.Minute)
	NewAlertStoreBase(first).Add("tx-1", []types.Alert{{
		ID: "al-1", OrgID: "hanzo/acme", TxID: "tx-1", RuleID: "struct-24h",
		RuleName: "Structuring, 24 hours", Severity: types.SeverityCritical,
		Score: 0.91, ActionTaken: types.ActionBlock, CreatedAt: raised,
	}, {
		ID: "al-2", OrgID: "zoo/acme", TxID: "tx-1", RuleID: "ctr-day",
		RuleName: "Daily reporting limit", Severity: types.SeverityHigh,
		Score: 0.62, ActionTaken: types.ActionReport, CreatedAt: raised,
	}})

	dir := copyAlertTree(t, first.DataDir())
	first.Cleanup()

	second, err := tests.NewTestApp(dir)
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	t.Cleanup(second.Cleanup)
	after := NewAlertStoreBase(second)

	got := after.ByTx("hanzo/acme", "tx-1")
	if len(got) != 1 {
		t.Fatalf("after the restart the transaction has %d alerts, want 1", len(got))
	}
	if got[0].ID != "al-1" || got[0].RuleName != "Structuring, 24 hours" || got[0].Score != 0.91 {
		t.Fatalf("the alert came back changed: %+v", got[0])
	}

	// The org is what scopes the read, and it still does on the other side.
	if other := after.ByTx("lux/acme", "tx-1"); len(other) != 0 {
		t.Fatalf("a tenant with no alerts on this transaction read %d", len(other))
	}
	if zoo := after.ByTx("zoo/acme", "tx-1"); len(zoo) != 1 || zoo[0].ID != "al-2" {
		t.Fatalf("the other tenant's own alert did not survive: %+v", zoo)
	}
}

func copyAlertTree(t *testing.T, src string) string {
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
