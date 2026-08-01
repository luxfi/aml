// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package watch

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/store"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/types"
)

const (
	fieldAt         = "at"
	fieldRule       = "rule"
	fieldRuleName   = "rule_name"
	fieldTypology   = "typology"
	fieldSeverity   = "severity"
	fieldScore      = "score"
	fieldTx         = "tx"
	fieldKind       = "kind"
	fieldValue      = "value"
	fieldAction     = "action"
	fieldResponse   = "response"
	fieldSuppressed = "suppressed"
	fieldCause      = "cause"
	fieldBy         = "by"
	fieldRung       = "rung"
	fieldStreak     = "streak"

	fieldCount   = "count"
	fieldWithin  = "within"
	fieldTo      = "to"
	fieldReason  = "reason"
	fieldRetired = "retired"
	fieldOffBy   = "retired_by"
	fieldOffWhy  = "retire_why"
)

var activationKind = store.Kind{
	Name: "aml_activations",
	Fields: []core.Field{
		&core.DateField{Name: fieldAt, Required: true},
		&core.TextField{Name: fieldRule, Required: true},
		&core.TextField{Name: fieldRuleName},
		&core.TextField{Name: fieldTypology},
		&core.TextField{Name: fieldSeverity},
		&core.NumberField{Name: fieldScore},
		&core.TextField{Name: fieldTx},
		&core.TextField{Name: fieldKind, Required: true},
		&core.TextField{Name: fieldValue, Required: true},
		&core.TextField{Name: fieldAction, Required: true},
		&core.TextField{Name: fieldResponse, Required: true},
		&core.BoolField{Name: fieldSuppressed},
		&core.TextField{Name: fieldCause},
		&core.TextField{Name: fieldBy},
		&core.TextField{Name: fieldRung},
		&core.NumberField{Name: fieldStreak},
	},
	Indexes: []store.Index{
		// The feed and the rates: this tenant's activations in time order.
		{Name: "at", Fields: []string{store.Org, fieldAt}},
		// The streak and the fold: this rule on this subject, most recent first.
		{Name: "streak", Fields: []string{store.Org, fieldRule, fieldKind, fieldValue, fieldAt}},
	},
}

var rungKind = store.Kind{
	Name: "aml_rungs",
	Fields: []core.Field{
		&core.TextField{Name: fieldRule, Required: true},
		&core.TextField{Name: fieldKind, Required: true},
		&core.NumberField{Name: fieldCount},
		&core.NumberField{Name: fieldWithin},
		&core.TextField{Name: fieldTo, Required: true},
		&core.TextField{Name: fieldReason, Required: true},
		&core.TextField{Name: fieldBy, Required: true},
		&core.DateField{Name: fieldAt, Required: true},
		&core.DateField{Name: fieldRetired},
		&core.TextField{Name: fieldOffBy},
		&core.TextField{Name: fieldOffWhy},
	},
	Indexes: []store.Index{
		{Name: "rule", Fields: []string{store.Org, fieldRule, fieldKind}},
	},
}

// Ensure creates what activations and rungs are kept in. Idempotent, so it runs
// on every start.
func Ensure(app core.App) error {
	if err := activationKind.Ensure(app); err != nil {
		return err
	}
	return rungKind.Ensure(app)
}

// Shelf is the durable activation plane, plus the live feed over it.
type Shelf struct {
	app core.App
	// Cover is consulted before an activation is marked live. Nil means no
	// suppression plane is wired and nothing is ever covered — which is the safe
	// default, because the failure it produces is noise rather than silence.
	Cover Suppressor
	// Now supplies the instant. Tests set it.
	Now func() time.Time

	mu      sync.RWMutex
	feeds   map[string][]*feed
	dropped map[string]*atomic.Int64
}

// Suppressor is the one method this plane needs from pkg/suppress. It is an
// interface so the activation plane can be tested without a suppression plane,
// and so the dependency runs one way only.
type Suppressor interface {
	Cover(ctx context.Context, org string, in *suppress.CoverIn) (*suppress.Cover, error)
}

// NewBase returns the durable shelf. Ensure has to have run first.
func NewBase(app core.App) *Shelf {
	return &Shelf{app: app, feeds: map[string][]*feed{}, dropped: map[string]*atomic.Int64{}}
}

func (s *Shelf) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Record writes one activation and returns it as it was recorded.
//
// The order is the whole design: the row is written first, and only a written row
// is offered to the live feed. Wired the other way a monitor could show a
// detection that the store never took, which is worse than showing none.
func (s *Shelf) Record(ctx context.Context, org string, in *RecordIn) (*Activation, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	rule := strings.TrimSpace(in.Rule)
	subj := Subject{Kind: strings.TrimSpace(in.Subject.Kind), Value: strings.TrimSpace(in.Subject.Value)}
	switch {
	case rule == "":
		return nil, ErrRule
	case subj.Value == "":
		return nil, ErrSubject
	case !slices.Contains(history.Kinds, subj.Kind):
		return nil, fmt.Errorf("%w: %q, want one of %s", ErrKind, subj.Kind, strings.Join(history.Kinds, ", "))
	case !slices.Contains(actions, in.Action):
		return nil, fmt.Errorf("%w: %q", ErrAction, in.Action)
	}

	at := in.At.UTC()
	if at.IsZero() {
		at = s.now()
	}
	a := Activation{
		ID: uuid.NewString(), Org: org, At: at,
		Rule: rule, RuleName: in.RuleName, Typology: in.Typology, Severity: in.Severity,
		Score: in.Score, Tx: strings.TrimSpace(in.Tx), Subject: subj,
		Action: in.Action, Response: in.Action,
	}

	// A declared suppression is the first question, because it is a decision
	// somebody took and it outranks anything computed from repetition.
	if s.Cover != nil {
		cover, err := s.Cover.Cover(ctx, org, &suppress.CoverIn{Rule: rule, Kind: subj.Kind, Value: subj.Value, At: at})
		if err != nil {
			return nil, err
		}
		if cover.Covered {
			a.Suppressed, a.Cause, a.By = true, CauseSuppressed, cover.Suppression.ID
			a.Response = types.ActionAllow
		}
	}

	if !a.Suppressed {
		if err := s.apply(org, &a); err != nil {
			return nil, err
		}
	}

	row, err := activationKind.New(s.app, org)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Id = a.ID
	writeActivation(row, a)
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: recording %s on %s: %w", ErrStore, rule, subj.Value, err)
	}

	s.publish(a)
	return &a, nil
}

// apply reads the tenant's rungs for this rule and subject kind, counts the
// streak once if any of them could bear on it, and moves the response.
//
// The streak query is skipped entirely when no rung is declared. That matters:
// the ordinary deployment has none, and a monitoring plane that costs an extra
// indexed read per detection for a policy nobody declared is a cost paid for
// nothing.
func (s *Shelf) apply(org string, a *Activation) error {
	rungs, err := s.rungs(org, a.Rule, a.Subject.Kind, a.At)
	if err != nil {
		return err
	}
	if len(rungs) == 0 {
		return nil
	}

	widest := time.Duration(0)
	for _, r := range rungs {
		if r.Within.Duration() > widest {
			widest = r.Within.Duration()
		}
	}
	prior, err := s.priorInWindow(org, a, widest)
	if err != nil {
		return err
	}
	a.Streak = len(prior) + 1

	// Elevation first, and the furthest rung reached wins. An activation that
	// reached an escalation is the escalation, not a repeat of one.
	best := Rung{}
	for _, r := range rungs {
		if r.To == Fold || r.Count > a.Streak {
			continue
		}
		if within(prior, a.At, r.Within.Duration())+1 < r.Count {
			continue
		}
		if best.ID == "" || r.Count > best.Count {
			best = r
		}
	}
	if best.ID != "" && types.ActionRank(best.To) > types.ActionRank(a.Response) {
		a.Response, a.Rung = best.To, best.ID
		return nil
	}

	// Folding, only where nothing raised it.
	for _, r := range rungs {
		if r.To != Fold || r.Count > a.Streak {
			continue
		}
		if within(prior, a.At, r.Within.Duration())+1 < r.Count {
			continue
		}
		a.Suppressed, a.Cause, a.Rung = true, CauseDuplicate, r.ID
		a.Response = types.ActionAllow
		// By names the activation this one repeats, which is the most recent live
		// one inside the window: that is the alert a reviewer already has.
		for _, p := range prior {
			if !p.Suppressed && a.At.Sub(p.At) <= r.Within.Duration() {
				a.By = p.ID
				break
			}
		}
		return nil
	}
	return nil
}

// within counts how many of the prior activations fall inside a window ending at
// the given instant. The rows were read once over the widest window any rung
// declared, so each narrower rung is answered from that one read.
func within(prior []Activation, at time.Time, w time.Duration) int {
	n := 0
	for _, p := range prior {
		if at.Sub(p.At) <= w {
			n++
		}
	}
	return n
}

// priorInWindow reads this rule's earlier activations on this subject, most
// recent first. Suppressed ones are included in the read and counted, because a
// streak that ignores what a suppression hid would restart every time somebody
// suppressed one.
func (s *Shelf) priorInWindow(org string, a *Activation, w time.Duration) ([]Activation, error) {
	filter := fieldRule + " = {:rule} && " + fieldKind + " = {:kind} && " + fieldValue + " = {:value} && " +
		fieldAt + " >= {:from} && " + fieldAt + " < {:at}"
	rows, err := activationKind.Find(s.app, org, filter, "-"+fieldAt, MaxLimit,
		dbx.Params{"rule": a.Rule, "kind": a.Subject.Kind, "value": a.Subject.Value,
			"from": a.At.Add(-w), "at": a.At})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := make([]Activation, 0, len(rows))
	for _, row := range rows {
		out = append(out, readActivation(row))
	}
	return out, nil
}

// Feed reads the activation plane forward from an instant.
func (s *Shelf) Feed(ctx context.Context, org string, in *FeedIn) (*Feed, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	limit := bound(in.Limit)
	filter := fieldAt + " > {:since}"
	params := dbx.Params{"since": in.Since.UTC()}
	if r := strings.TrimSpace(in.Rule); r != "" {
		filter += " && " + fieldRule + " = {:rule}"
		params["rule"] = r
	}
	if in.Live {
		filter += " && " + fieldSuppressed + " = false"
	}
	rows, err := activationKind.Find(s.app, org, filter, fieldAt+",id", limit+1, params)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := &Feed{Activations: make([]Activation, 0, len(rows)), Through: in.Since.UTC(), Dropped: s.Dropped(org)}
	if len(rows) > limit {
		rows, out.Cut = rows[:limit], true
	}
	for _, row := range rows {
		a := readActivation(row)
		out.Activations = append(out.Activations, a)
		out.Through = a.At
	}
	return out, nil
}

// Rates folds the window into one row per rule.
func (s *Shelf) Rates(ctx context.Context, org string, in *RatesIn) (*Rates, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	to := in.Until.UTC()
	if to.IsZero() {
		to = s.now()
	}
	from := in.Since.UTC()
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	rows, err := activationKind.Find(s.app, org, fieldAt+" >= {:from} && "+fieldAt+" <= {:to}", fieldAt, MaxExamined+1,
		dbx.Params{"from": from, "to": to})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := &Rates{From: from, To: to}
	if len(rows) > MaxExamined {
		rows, out.Cut = rows[:MaxExamined], true
	}
	out.Examined = len(rows)

	byRule := map[string]*Rate{}
	subjects := map[string]map[string]struct{}{}
	for _, row := range rows {
		a := readActivation(row)
		r, ok := byRule[a.Rule]
		if !ok {
			r = &Rate{Rule: a.Rule, RuleName: a.RuleName}
			byRule[a.Rule] = r
			subjects[a.Rule] = map[string]struct{}{}
		}
		r.Fired++
		switch {
		case a.Cause == CauseSuppressed:
			r.Silenced++
		case a.Cause == CauseDuplicate:
			r.Folded++
		default:
			r.Live++
		}
		if a.Rung != "" && a.Cause == "" {
			r.Elevated++
		}
		if a.Response == types.ActionBlock {
			r.Blocked++
		}
		if a.At.After(r.Last) {
			r.Last = a.At
		}
		subjects[a.Rule][a.Subject.Kind+"\x1f"+a.Subject.Value] = struct{}{}
	}
	out.Rules = make([]Rate, 0, len(byRule))
	for id, r := range byRule {
		r.Subjects = len(subjects[id])
		out.Rules = append(out.Rules, *r)
	}
	sort.Slice(out.Rules, func(i, j int) bool {
		if out.Rules[i].Fired != out.Rules[j].Fired {
			return out.Rules[i].Fired > out.Rules[j].Fired
		}
		return out.Rules[i].Rule < out.Rules[j].Rule
	})
	return out, nil
}

// Declare records a rung.
func (s *Shelf) Declare(ctx context.Context, org string, in *DeclareIn) (*Rung, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	rule, kindName := strings.TrimSpace(in.Rule), strings.TrimSpace(in.Kind)
	switch {
	case rule == "":
		return nil, ErrRule
	case !slices.Contains(history.Kinds, kindName):
		return nil, fmt.Errorf("%w: %q, want one of %s", ErrKind, kindName, strings.Join(history.Kinds, ", "))
	case in.To != Fold && !slices.Contains(actions, in.To):
		return nil, fmt.Errorf("%w: %q", ErrTo, in.To)
	case in.To == types.ActionAllow:
		return nil, fmt.Errorf("%w: a rung may only raise a response, and lowering one is a suppression", ErrTo)
	case in.Count < 2:
		return nil, fmt.Errorf("%w: %d", ErrCount, in.Count)
	case in.Within <= 0:
		return nil, ErrWithin
	case strings.TrimSpace(in.Reason) == "":
		return nil, ErrReason
	case strings.TrimSpace(in.By) == "":
		return nil, ErrDecider
	}

	r := Rung{
		ID: uuid.NewString(), Org: org, Rule: rule, Kind: kindName,
		Count: in.Count, Within: in.Within, To: in.To,
		Reason: strings.TrimSpace(in.Reason), By: strings.TrimSpace(in.By), At: s.now(),
	}
	row, err := rungKind.New(s.app, org)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Id = r.ID
	writeRung(row, r)
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return &r, nil
}

// Retire ends a rung. The row stays and records who ended it and why.
func (s *Shelf) Retire(ctx context.Context, org string, in *RetireIn) (*Rung, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, ErrReason
	}
	if strings.TrimSpace(in.By) == "" {
		return nil, ErrDecider
	}
	rows, err := rungKind.Find(s.app, org, "id = {:id}", "", 1, dbx.Params{"id": strings.TrimSpace(in.ID)})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotHere, in.ID)
	}
	r := readRung(rows[0])
	if !r.Retired.IsZero() {
		return nil, fmt.Errorf("%w: %s", ErrRetired, r.ID)
	}
	r.Retired, r.RetiredBy, r.RetireWhy = s.now(), strings.TrimSpace(in.By), strings.TrimSpace(in.Reason)
	writeRung(rows[0], r)
	if err := s.app.Save(rows[0]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return &r, nil
}

// Ladder reads a tenant's rungs, ordered by rule then by how far along the ladder
// each one sits.
func (s *Shelf) Ladder(ctx context.Context, org string, in *LadderIn) (*Ladder, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	filter, params := "", dbx.Params{}
	if r := strings.TrimSpace(in.Rule); r != "" {
		filter, params["rule"] = fieldRule+" = {:rule}", r
	}
	rows, err := rungKind.Find(s.app, org, filter, fieldRule+","+fieldCount, 0, params)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	at := s.now()
	out := &Ladder{Rungs: make([]Rung, 0, len(rows))}
	for _, row := range rows {
		r := readRung(row)
		if in.InForce && !r.InForce(at) {
			continue
		}
		out.Rungs = append(out.Rungs, r)
	}
	return out, nil
}

// rungs reads the rungs in force for one rule and subject kind.
func (s *Shelf) rungs(org, rule, kindName string, at time.Time) ([]Rung, error) {
	rows, err := rungKind.Find(s.app, org, fieldRule+" = {:rule} && "+fieldKind+" = {:kind}", fieldCount, 0,
		dbx.Params{"rule": rule, "kind": kindName})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := make([]Rung, 0, len(rows))
	for _, row := range rows {
		if r := readRung(row); r.InForce(at) {
			out = append(out, r)
		}
	}
	return out, nil
}

func bound(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

func writeActivation(row *core.Record, a Activation) {
	row.Set(fieldAt, a.At.UTC())
	row.Set(fieldRule, a.Rule)
	row.Set(fieldRuleName, a.RuleName)
	row.Set(fieldTypology, a.Typology)
	row.Set(fieldSeverity, a.Severity)
	row.Set(fieldScore, a.Score)
	row.Set(fieldTx, a.Tx)
	row.Set(fieldKind, a.Subject.Kind)
	row.Set(fieldValue, a.Subject.Value)
	row.Set(fieldAction, a.Action)
	row.Set(fieldResponse, a.Response)
	row.Set(fieldSuppressed, a.Suppressed)
	row.Set(fieldCause, a.Cause)
	row.Set(fieldBy, a.By)
	row.Set(fieldRung, a.Rung)
	row.Set(fieldStreak, a.Streak)
}

func readActivation(row *core.Record) Activation {
	return Activation{
		ID:         row.Id,
		Org:        row.GetString(store.Org),
		At:         row.GetDateTime(fieldAt).Time(),
		Rule:       row.GetString(fieldRule),
		RuleName:   row.GetString(fieldRuleName),
		Typology:   row.GetString(fieldTypology),
		Severity:   row.GetString(fieldSeverity),
		Score:      row.GetFloat(fieldScore),
		Tx:         row.GetString(fieldTx),
		Subject:    Subject{Kind: row.GetString(fieldKind), Value: row.GetString(fieldValue)},
		Action:     row.GetString(fieldAction),
		Response:   row.GetString(fieldResponse),
		Suppressed: row.GetBool(fieldSuppressed),
		Cause:      row.GetString(fieldCause),
		By:         row.GetString(fieldBy),
		Rung:       row.GetString(fieldRung),
		Streak:     row.GetInt(fieldStreak),
	}
}

func writeRung(row *core.Record, r Rung) {
	row.Set(fieldRule, r.Rule)
	row.Set(fieldKind, r.Kind)
	row.Set(fieldCount, r.Count)
	row.Set(fieldWithin, int64(r.Within.Duration()/time.Second))
	row.Set(fieldTo, r.To)
	row.Set(fieldReason, r.Reason)
	row.Set(fieldBy, r.By)
	row.Set(fieldAt, r.At.UTC())
	row.Set(fieldRetired, moment(r.Retired))
	row.Set(fieldOffBy, r.RetiredBy)
	row.Set(fieldOffWhy, r.RetireWhy)
}

func readRung(row *core.Record) Rung {
	return Rung{
		ID:        row.Id,
		Org:       row.GetString(store.Org),
		Rule:      row.GetString(fieldRule),
		Kind:      row.GetString(fieldKind),
		Count:     row.GetInt(fieldCount),
		Within:    Span(time.Duration(row.GetInt(fieldWithin)) * time.Second),
		To:        row.GetString(fieldTo),
		Reason:    row.GetString(fieldReason),
		By:        row.GetString(fieldBy),
		At:        row.GetDateTime(fieldAt).Time(),
		Retired:   row.GetDateTime(fieldRetired).Time(),
		RetiredBy: row.GetString(fieldOffBy),
		RetireWhy: row.GetString(fieldOffWhy),
	}
}

func moment(t time.Time) any {
	if t.IsZero() {
		return ""
	}
	return t.UTC()
}
