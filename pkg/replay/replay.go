// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package replay runs a rule over real history and reports what it would have
// done, before anyone activates it.
//
// JMLSG 5.7.18 requires functionality to implement and test new typologies
// before live activation, and the FCA treats retiring a rule set as a governed
// act that must be justified against performance outcomes including alert
// intelligence-value and the false-positive proportion (FCG 3.2.5A). Those two
// requirements are one question — how many alerts would this raise, on what, and
// how does that compare with the rule it replaces — and this package answers it.
//
// # Dry run, structurally
//
// A sandbox that can mutate live state is not a sandbox. This package reaches
// the engine through [Evaluator], whose one method evaluates and returns hits,
// and history through [History], whose one method iterates. Neither can write:
// there is no store here, no case, no alert and no webhook, and the package
// imports nothing that has one.
//
// The evaluation itself is the engine's own, not a copy of it. A sandbox with its
// own evaluator can disagree with production about what a rule does, which makes
// its answer worthless; the only difference between a replay and the live path is
// that the rule set is the candidate and nothing is written.
//
// # Refusals
//
// An empty history is refused rather than reported as zero alerts, because "no
// alerts" is exactly how a quiet rule looks, and activating a rule on the
// strength of an empty replay is the failure this package exists to prevent.
// An evaluation error is refused for the same reason: a count taken from a rule
// that did not evaluate is not a count.
package replay

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// Errors.
var (
	ErrNoEvaluator = errors.New("replay: no evaluator")
	ErrNoHistory   = errors.New("replay: no history")
	ErrNoRule      = errors.New("replay: candidate has no expression")
	ErrEmpty       = errors.New("replay: history is empty, so a replay proves nothing")
	ErrEval        = errors.New("replay: rule did not evaluate over history")
)

// listed is how many transaction ids an outcome or a delta carries. The counts
// are exact whatever this is; the lists are evidence for a human, and a human
// reading a decision does not need the millionth id. A list shorter than its
// count was cut here.
const listed = 1000

// Evaluator evaluates a rule set against one event. *engine.Evaluator satisfies
// it, which is the whole point: the candidate is scored by the code that scores
// production.
type Evaluator interface {
	EvalAll(rules []types.Rule, ctx types.EvalContext) []types.RuleHit
}

// Disposition is what a human concluded about an event that alerted. It is what
// makes a false-positive proportion measured rather than guessed.
type Disposition string

const (
	// Unjudged is an event nobody has concluded anything about yet.
	Unjudged Disposition = ""
	// Productive is an alert that led somewhere: escalated, or reported.
	Productive Disposition = "productive"
	// Unproductive is an alert dismissed as not suspicious.
	Unproductive Disposition = "unproductive"
)

// Event is one historical transaction, the entity as it stood, and what the live
// system did with it.
type Event struct {
	Tx     types.Transaction `json:"tx"`
	Entity types.Entity      `json:"entity"`
	// Alerted is the rule ids that actually alerted when this was live. It is
	// how a replayed count is compared against an observed one.
	Alerted []string `json:"alerted,omitempty"`
	// Disposition is the conclusion a human reached about this event. It attaches
	// to the event and not to each alert, so where several rules alerted on one
	// event every one of them inherits it.
	Disposition Disposition `json:"disposition,omitempty"`
}

// History is a replayable window of real events.
type History interface {
	// Each visits every event. It stops and returns the callback's error.
	Each(func(Event) error) error
}

// Slice is a History held in memory.
type Slice []Event

// Each visits every event in order.
func (s Slice) Each(fn func(Event) error) error {
	for _, e := range s {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// Outcome is what one rule would have done over the replayed history.
type Outcome struct {
	Rule string `json:"rule"`
	// Alerts is how many events the rule would raise an alert on, and Txs is on
	// what. Txs is cut at a bound; Alerts never is.
	Alerts int      `json:"alerts"`
	Txs    []string `json:"txs,omitempty"`
	// Observed is how many events actually recorded an alert from this rule when
	// it was live. For a new rule it is zero, and a replayed count far from an
	// observed one on an existing rule means the rule or the history has moved.
	Observed int `json:"observed"`
	// Judged is how many of the replayed alerts fall on events a human has
	// concluded something about. The proportions below are over Judged, not over
	// Alerts.
	Judged       int `json:"judged"`
	Productive   int `json:"productive"`
	Unproductive int `json:"unproductive"`
	// FalsePositive and Intelligence are the two outcomes the FCA asks for when a
	// rule set is retired. They are absent, not zero, when nothing was judged: an
	// unmeasured proportion reported as 0.0 reads as a perfect rule.
	FalsePositive *float64 `json:"false_positive_proportion,omitempty"`
	Intelligence  *float64 `json:"intelligence_value,omitempty"`
}

// Delta is the candidate against the rule it replaces.
type Delta struct {
	// Added is what the candidate alerts on and the incumbent does not — the new
	// coverage, and the new volume.
	Added []string `json:"added,omitempty"`
	// Dropped is what the incumbent alerts on and the candidate does not — the
	// coverage being given up, which is what a decommissioning has to justify.
	Dropped []string `json:"dropped,omitempty"`
	// Kept is what both alert on.
	Kept  []string `json:"kept,omitempty"`
	Sizes struct {
		Added   int `json:"added"`
		Dropped int `json:"dropped"`
		Kept    int `json:"kept"`
	} `json:"counts"`
}

// Report is the answer: what the candidate would have done, what the incumbent
// did, and the difference.
type Report struct {
	// Events is how many events were replayed, and From/To the period they span.
	// A window is empty when the events carry no timestamps, which is how a
	// caller-supplied sample is distinguishable from real history.
	Events int       `json:"events"`
	From   time.Time `json:"from,omitzero"`
	To     time.Time `json:"to,omitzero"`

	Candidate Outcome  `json:"candidate"`
	Incumbent *Outcome `json:"incumbent,omitempty"`
	Delta     *Delta   `json:"delta,omitempty"`
}

// Run replays the candidate over the history, and the incumbent alongside it
// when one is given.
//
// Both rules are evaluated as activated whatever their enabled flag says: the
// question is what happens on activation, and the flag governs the live path
// rather than the question. Nothing is written anywhere.
func Run(e Evaluator, h History, candidate types.Rule, incumbent *types.Rule) (Report, error) {
	if e == nil {
		return Report{}, ErrNoEvaluator
	}
	if h == nil {
		return Report{}, ErrNoHistory
	}
	if strings.TrimSpace(candidate.DSL) == "" {
		return Report{}, ErrNoRule
	}

	cand := activated(candidate)
	report := Report{Candidate: Outcome{Rule: name(cand)}}

	var prev *types.Rule
	if incumbent != nil {
		if strings.TrimSpace(incumbent.DSL) == "" {
			return Report{}, fmt.Errorf("%w: incumbent %s", ErrNoRule, name(*incumbent))
		}
		p := activated(*incumbent)
		prev = &p
		report.Incumbent = &Outcome{Rule: name(p)}
		report.Delta = &Delta{}
	}

	err := h.Each(func(ev Event) error {
		report.Events++
		observe(&report, ev)

		hit, err := fires(e, cand, ev)
		if err != nil {
			return err
		}
		if hit {
			record(&report.Candidate, ev)
		}
		if prev == nil {
			return nil
		}

		was, err := fires(e, *prev, ev)
		if err != nil {
			return err
		}
		if was {
			record(report.Incumbent, ev)
		}
		switch {
		case hit && was:
			report.Delta.Kept = add(report.Delta.Kept, ev.Tx.ID)
			report.Delta.Sizes.Kept++
		case hit:
			report.Delta.Added = add(report.Delta.Added, ev.Tx.ID)
			report.Delta.Sizes.Added++
		case was:
			report.Delta.Dropped = add(report.Delta.Dropped, ev.Tx.ID)
			report.Delta.Sizes.Dropped++
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	if report.Events == 0 {
		return Report{}, ErrEmpty
	}

	proportions(&report.Candidate)
	if report.Incumbent != nil {
		proportions(report.Incumbent)
	}
	return report, nil
}

// fires reports whether a rule would alert on an event, through the engine's own
// evaluation. An evaluation error is the caller's answer, not a zero.
func fires(e Evaluator, r types.Rule, ev Event) (bool, error) {
	hits := e.EvalAll([]types.Rule{r}, types.EvalContext{Tx: ev.Tx, Entity: ev.Entity})
	for _, h := range hits {
		if h.EvalErr != "" {
			return false, fmt.Errorf("%w: %s on transaction %s: %s", ErrEval, name(r), ev.Tx.ID, h.EvalErr)
		}
	}
	return len(hits) > 0, nil
}

// activated is the rule as it would be if it were live. The jurisdiction and
// asset-class filters are untouched, because the live path applies them too.
func activated(r types.Rule) types.Rule {
	r.Enabled = true
	return r
}

func name(r types.Rule) string {
	if r.ID != "" {
		return r.ID
	}
	return r.Name
}

// observe counts what the live system actually did, so a replayed count can be
// held against it.
func observe(report *Report, ev Event) {
	if ts := ev.Tx.Timestamp; !ts.IsZero() {
		if report.From.IsZero() || ts.Before(report.From) {
			report.From = ts.UTC()
		}
		if ts.After(report.To) {
			report.To = ts.UTC()
		}
	}
	for _, id := range ev.Alerted {
		if id == report.Candidate.Rule {
			report.Candidate.Observed++
		}
		if report.Incumbent != nil && id == report.Incumbent.Rule {
			report.Incumbent.Observed++
		}
	}
}

func record(o *Outcome, ev Event) {
	o.Alerts++
	o.Txs = add(o.Txs, ev.Tx.ID)
	switch ev.Disposition {
	case Productive:
		o.Judged++
		o.Productive++
	case Unproductive:
		o.Judged++
		o.Unproductive++
	}
}

// proportions computes the two numbers the FCA asks for, and leaves them absent
// when there is nothing to compute them from.
func proportions(o *Outcome) {
	if o.Judged == 0 {
		return
	}
	fp := float64(o.Unproductive) / float64(o.Judged)
	value := float64(o.Productive) / float64(o.Judged)
	o.FalsePositive = &fp
	o.Intelligence = &value
}

func add(ids []string, id string) []string {
	if len(ids) >= listed {
		return ids
	}
	return append(ids, id)
}
