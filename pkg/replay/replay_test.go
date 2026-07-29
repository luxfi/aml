package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// evaluator stands in for the engine's evaluator with the same contract: a
// disabled rule is not evaluated, a broken rule yields a hit carrying its error,
// and a match yields a hit. The real *engine.Evaluator is exercised against this
// package at the wiring point, in pkg/api, where the two are compiled together.
type evaluator struct {
	fire func(types.Rule, types.EvalContext) (bool, error)
}

func (e evaluator) EvalAll(_ context.Context, rules []types.Rule, tx types.Transaction, ent types.Entity) []types.RuleHit {
	var hits []types.RuleHit
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		ok, err := e.fire(r, types.EvalContext{Tx: tx, Entity: ent})
		if err != nil {
			hits = append(hits, types.RuleHit{Rule: r, Match: true, EvalErr: err.Error()})
			continue
		}
		if ok {
			hits = append(hits, types.RuleHit{Rule: r, Match: true})
		}
	}
	return hits
}

// above fires a rule whose DSL names a threshold the notional passes.
func above() evaluator {
	return evaluator{fire: func(r types.Rule, ctx types.EvalContext) (bool, error) {
		var threshold float64
		if _, err := fmt.Sscanf(r.DSL, "notional > %f", &threshold); err != nil {
			return false, fmt.Errorf("cannot compile %q", r.DSL)
		}
		return ctx.Tx.Notional > threshold, nil
	}}
}

// selects fires each rule on a named set of transactions, which is how two
// partially overlapping rules are stated without inventing a DSL.
func selects(set map[string]map[string]bool) evaluator {
	return evaluator{fire: func(r types.Rule, ctx types.EvalContext) (bool, error) {
		if r.DSL == "" {
			return false, errors.New("empty expression")
		}
		return set[r.ID][ctx.Tx.ID], nil
	}}
}

func rule(id, dsl string) types.Rule {
	return types.Rule{ID: id, Name: id, DSL: dsl, Enabled: true}
}

func event(id string, notional float64, d Disposition, alerted ...string) Event {
	return Event{
		Tx: types.Transaction{
			ID:        id,
			Notional:  notional,
			Timestamp: time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC),
		},
		Alerted:     alerted,
		Disposition: d,
	}
}

// TestEmptyHistoryIsRefused is the sharpest fail-closed property here. A replay
// over nothing looks exactly like a quiet rule, and a rule activated on that
// evidence is the failure this package exists to prevent.
func TestEmptyHistoryIsRefused(t *testing.T) {
	for name, h := range map[string]History{
		"nil slice":   Slice(nil),
		"empty slice": Slice{},
	} {
		report, err := Run(context.Background(), above(), h, rule("cand", "notional > 100"), nil)
		if !errors.Is(err, ErrEmpty) {
			t.Errorf("%s: err = %v, want ErrEmpty", name, err)
		}
		if report.Events != 0 || report.Candidate.Alerts != 0 {
			t.Errorf("%s: refused run still reported %+v", name, report)
		}
	}
}

// TestQuietRuleOverRealHistoryIsNotAnError: zero alerts over a non-empty history
// is an answer, and the distinction from the empty case is the whole point.
func TestQuietRuleOverRealHistoryIsNotAnError(t *testing.T) {
	h := Slice{event("tx-1", 10, Unjudged), event("tx-2", 20, Unjudged)}

	report, err := Run(context.Background(), above(), h, rule("cand", "notional > 1000000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Events != 2 {
		t.Errorf("events = %d, want 2", report.Events)
	}
	if report.Candidate.Alerts != 0 {
		t.Errorf("alerts = %d, want 0", report.Candidate.Alerts)
	}
}

// TestRefusesWithoutItsParts.
func TestRefusesWithoutItsParts(t *testing.T) {
	h := Slice{event("tx-1", 10, Unjudged)}
	good := rule("cand", "notional > 5")

	if _, err := Run(context.Background(), nil, h, good, nil); !errors.Is(err, ErrNoEvaluator) {
		t.Errorf("no evaluator: err = %v", err)
	}
	if _, err := Run(context.Background(), above(), nil, good, nil); !errors.Is(err, ErrNoHistory) {
		t.Errorf("no history: err = %v", err)
	}
	for _, dsl := range []string{"", "   ", "\t\n"} {
		if _, err := Run(context.Background(), above(), h, rule("cand", dsl), nil); !errors.Is(err, ErrNoRule) {
			t.Errorf("candidate DSL %q: err = %v, want ErrNoRule", dsl, err)
		}
	}
	blank := rule("old", "")
	if _, err := Run(context.Background(), above(), h, good, &blank); !errors.Is(err, ErrNoRule) {
		t.Errorf("incumbent with no DSL: err = %v, want ErrNoRule", err)
	}
}

// TestCountsAlertsAndWhatTheyFellOn answers "how many, and on what".
func TestCountsAlertsAndWhatTheyFellOn(t *testing.T) {
	h := Slice{
		event("tx-1", 15000, Unjudged),
		event("tx-2", 500, Unjudged),
		event("tx-3", 11000, Unjudged),
	}

	report, err := Run(context.Background(), above(), h, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Candidate.Alerts != 2 {
		t.Fatalf("alerts = %d, want 2", report.Candidate.Alerts)
	}
	if got := report.Candidate.Txs; len(got) != 2 || got[0] != "tx-1" || got[1] != "tx-3" {
		t.Errorf("txs = %v, want [tx-1 tx-3]", got)
	}
	if report.Candidate.Rule != "ctr" {
		t.Errorf("rule = %q, want ctr", report.Candidate.Rule)
	}
}

// TestCandidateIsEvaluatedAsActivated: a rule that is not enabled yet is exactly
// the rule an operator wants to test, so the flag must not silently turn the
// answer into zero.
func TestCandidateIsEvaluatedAsActivated(t *testing.T) {
	h := Slice{event("tx-1", 15000, Unjudged)}

	candidate := rule("ctr", "notional > 10000")
	candidate.Enabled = false
	incumbent := rule("old", "notional > 9000")
	incumbent.Enabled = false

	report, err := Run(context.Background(), above(), h, candidate, &incumbent)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Candidate.Alerts != 1 {
		t.Errorf("candidate alerts = %d, want 1", report.Candidate.Alerts)
	}
	if report.Incumbent.Alerts != 1 {
		t.Errorf("incumbent alerts = %d, want 1", report.Incumbent.Alerts)
	}
	// The caller's rule is not modified by having been replayed.
	if candidate.Enabled || incumbent.Enabled {
		t.Error("Run enabled the caller's rule")
	}
}

// TestDeltaAgainstTheRuleItReplaces is the comparison a decommissioning has to
// justify: what is gained, what is given up, what is unchanged.
func TestDeltaAgainstTheRuleItReplaces(t *testing.T) {
	h := Slice{
		event("both", 20000, Unjudged),
		event("added", 9800, Unjudged),
		event("dropped", 9600, Unjudged),
		event("neither", 100, Unjudged),
	}

	// Two rules that overlap partially, which one threshold cannot express: the
	// candidate picks up something new and gives something up.
	which := selects(map[string]map[string]bool{
		"new": {"both": true, "added": true},
		"old": {"both": true, "dropped": true},
	})
	candidate := rule("new", "typology a")
	incumbent := rule("old", "typology b")

	report, err := Run(context.Background(), which, h, candidate, &incumbent)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Delta == nil {
		t.Fatal("no delta against an incumbent")
	}
	if got := report.Delta.Kept; len(got) != 1 || got[0] != "both" {
		t.Errorf("kept = %v, want [both]", got)
	}
	if got := report.Delta.Added; len(got) != 1 || got[0] != "added" {
		t.Errorf("added = %v, want [added]", got)
	}
	if got := report.Delta.Dropped; len(got) != 1 || got[0] != "dropped" {
		t.Errorf("dropped = %v, want [dropped]", got)
	}
	if s := report.Delta.Sizes; s.Kept != 1 || s.Added != 1 || s.Dropped != 1 {
		t.Errorf("counts = %+v, want 1/1/1", s)
	}
	if report.Candidate.Alerts != 2 || report.Incumbent.Alerts != 2 {
		t.Errorf("alerts candidate=%d incumbent=%d, want 2/2", report.Candidate.Alerts, report.Incumbent.Alerts)
	}
}

// TestNoDeltaWithoutAnIncumbent: nothing is being replaced, so there is nothing
// to compare and the report says so rather than inventing a baseline.
func TestNoDeltaWithoutAnIncumbent(t *testing.T) {
	h := Slice{event("tx-1", 15000, Unjudged)}

	report, err := Run(context.Background(), above(), h, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Incumbent != nil || report.Delta != nil {
		t.Errorf("invented a baseline: incumbent=%+v delta=%+v", report.Incumbent, report.Delta)
	}
}

// TestProportionsAreMeasuredOrAbsent: the two numbers the FCA asks for are
// computed over judged alerts, and reported as absent — never as zero — when
// nothing has been judged. A false-positive proportion of 0.0 reads as a perfect
// rule, which is the wrong thing to tell someone about to activate it.
func TestProportionsAreMeasuredOrAbsent(t *testing.T) {
	judged := Slice{
		event("a", 15000, Unproductive),
		event("b", 15000, Unproductive),
		event("c", 15000, Unproductive),
		event("d", 15000, Productive),
		event("e", 15000, Unjudged),
		event("f", 100, Productive), // does not alert, so does not count
	}

	report, err := Run(context.Background(), above(), judged, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	o := report.Candidate
	if o.Alerts != 5 {
		t.Fatalf("alerts = %d, want 5", o.Alerts)
	}
	if o.Judged != 4 || o.Unproductive != 3 || o.Productive != 1 {
		t.Fatalf("judged=%d unproductive=%d productive=%d, want 4/3/1", o.Judged, o.Unproductive, o.Productive)
	}
	if o.FalsePositive == nil || *o.FalsePositive != 0.75 {
		t.Errorf("false positive proportion = %v, want 0.75", o.FalsePositive)
	}
	if o.Intelligence == nil || *o.Intelligence != 0.25 {
		t.Errorf("intelligence value = %v, want 0.25", o.Intelligence)
	}

	unjudged := Slice{event("a", 15000, Unjudged), event("b", 15000, Unjudged)}
	report, err = Run(context.Background(), above(), unjudged, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Candidate.Alerts != 2 {
		t.Fatalf("alerts = %d, want 2", report.Candidate.Alerts)
	}
	if report.Candidate.FalsePositive != nil || report.Candidate.Intelligence != nil {
		t.Errorf("unmeasured proportions reported as %v and %v, want absent",
			report.Candidate.FalsePositive, report.Candidate.Intelligence)
	}

	// And absent survives the wire: no zero appears in the JSON.
	encoded, err := json.Marshal(report.Candidate)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"false_positive_proportion", "intelligence_value"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Errorf("%s present in %s", key, encoded)
		}
	}
}

// TestEvaluationErrorRefusesTheRun: a rule that did not evaluate has no count,
// and reporting the alerts it managed before failing would be a partial answer
// presented as a whole one.
func TestEvaluationErrorRefusesTheRun(t *testing.T) {
	h := Slice{
		event("tx-1", 15000, Unjudged),
		event("tx-2", 15000, Unjudged),
	}

	report, err := Run(context.Background(), above(), h, rule("broken", "this is not a threshold"), nil)
	if !errors.Is(err, ErrEval) {
		t.Fatalf("err = %v, want ErrEval", err)
	}
	if report.Events != 0 || report.Candidate.Alerts != 0 {
		t.Fatalf("failed run reported %+v", report)
	}

	// The same for the incumbent: half a comparison is not a comparison.
	broken := rule("brokenOld", "not a threshold either")
	if _, err := Run(context.Background(), above(), h, rule("ctr", "notional > 10000"), &broken); !errors.Is(err, ErrEval) {
		t.Errorf("broken incumbent: err = %v, want ErrEval", err)
	}
}

// TestObservedComesFromHistory: what the live system actually did, so a replayed
// count can be held against it.
func TestObservedComesFromHistory(t *testing.T) {
	h := Slice{
		event("tx-1", 15000, Unjudged, "old", "other"),
		event("tx-2", 15000, Unjudged, "old"),
		event("tx-3", 100, Unjudged, "other"),
	}

	incumbent := rule("old", "notional > 10000")
	report, err := Run(context.Background(), above(), h, rule("new", "notional > 12000"), &incumbent)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Incumbent.Observed != 2 {
		t.Errorf("incumbent observed = %d, want 2", report.Incumbent.Observed)
	}
	if report.Candidate.Observed != 0 {
		t.Errorf("candidate observed = %d, want 0 (it was never live)", report.Candidate.Observed)
	}
}

// TestWindowSpansTheHistory: the period replayed is reported, so nobody mistakes
// a handful of synthetic events for ninety days of traffic.
func TestWindowSpansTheHistory(t *testing.T) {
	first := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, time.June, 30, 0, 0, 0, 0, time.UTC)
	h := Slice{
		{Tx: types.Transaction{ID: "b", Notional: 15000, Timestamp: last}},
		{Tx: types.Transaction{ID: "a", Notional: 15000, Timestamp: first}},
	}

	report, err := Run(context.Background(), above(), h, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.From.Equal(first) || !report.To.Equal(last) {
		t.Errorf("window %s..%s, want %s..%s", report.From, report.To, first, last)
	}

	// A sample with no timestamps has no window, and says so.
	sample := Slice{{Tx: types.Transaction{ID: "synthetic", Notional: 15000}}}
	report, err = Run(context.Background(), above(), sample, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.From.IsZero() || !report.To.IsZero() {
		t.Errorf("synthetic sample claims a window %s..%s", report.From, report.To)
	}
}

// TestHistoryIsUnchanged: a sandbox that can mutate live state is not a sandbox,
// and the history is the closest thing to live state a replay can reach.
func TestHistoryIsUnchanged(t *testing.T) {
	h := Slice{
		event("tx-1", 15000, Unproductive, "old"),
		event("tx-2", 100, Productive, "other"),
	}
	before, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	incumbent := rule("old", "notional > 9000")
	if _, err := Run(context.Background(), above(), h, rule("new", "notional > 10000"), &incumbent); err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("history changed:\nbefore %s\nafter  %s", before, after)
	}
}

// TestListsAreBoundedAndCountsAreNot: the evidence a human reads is cut, the
// number they decide on is not.
func TestListsAreBoundedAndCountsAreNot(t *testing.T) {
	h := make(Slice, listed+500)
	for i := range h {
		h[i] = event(fmt.Sprintf("tx-%d", i), 15000, Unjudged)
	}

	report, err := Run(context.Background(), above(), h, rule("ctr", "notional > 10000"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Candidate.Alerts != len(h) {
		t.Errorf("alerts = %d, want %d", report.Candidate.Alerts, len(h))
	}
	if len(report.Candidate.Txs) != listed {
		t.Errorf("listed %d transactions, want %d", len(report.Candidate.Txs), listed)
	}
}

// TestHistoryErrorSurfaces: a history that cannot be read is not an empty
// history, and the difference must not be swallowed.
func TestHistoryErrorSurfaces(t *testing.T) {
	unreadable := errors.New("sealed record does not open")
	report, err := Run(context.Background(), above(), broken{unreadable}, rule("ctr", "notional > 10000"), nil)
	if !errors.Is(err, unreadable) {
		t.Fatalf("err = %v, want the history's own error", err)
	}
	if report.Events != 0 {
		t.Errorf("failed read reported %d events", report.Events)
	}
}

type broken struct{ err error }

func (b broken) Each(func(Event) error) error { return b.err }
