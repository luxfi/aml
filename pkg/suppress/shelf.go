// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package suppress

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/store"
)

const (
	fieldRule    = "rule"
	fieldKind    = "kind"
	fieldValue   = "value"
	fieldReason  = "reason"
	fieldBy      = "by"
	fieldFrom    = "from_at"
	fieldUntil   = "until"
	fieldLifted  = "lifted"
	fieldLiftBy  = "lift_by"
	fieldLiftWhy = "lift_why"
)

var kind = store.Kind{
	Name: "aml_suppressions",
	Fields: []core.Field{
		&core.TextField{Name: fieldRule},
		&core.TextField{Name: fieldKind},
		&core.TextField{Name: fieldValue},
		&core.TextField{Name: fieldReason, Required: true},
		&core.TextField{Name: fieldBy, Required: true},
		&core.DateField{Name: fieldFrom, Required: true},
		&core.DateField{Name: fieldUntil},
		&core.DateField{Name: fieldLifted},
		&core.TextField{Name: fieldLiftBy},
		&core.TextField{Name: fieldLiftWhy},
	},
	Indexes: []store.Index{
		// The hot read: this tenant's suppressions that could cover an activation
		// of this rule. The blanket ones carry an empty rule and are read by the
		// same index.
		{Name: "rule", Fields: []string{store.Org, fieldRule}},
		// The subject read, for the ledger view of one account or address.
		{Name: "subject", Fields: []string{store.Org, fieldKind, fieldValue}},
	},
}

// Ensure creates what suppressions are kept in. Idempotent, so it runs on every
// start.
func Ensure(app core.App) error { return kind.Ensure(app) }

// MaxCandidates bounds the rows one Cover reads.
//
// The read is already narrowed to this tenant and to the rule named plus the
// blanket ones, so reaching this bound means an institution has declared more
// suppressions against one rule than anybody could review. It is reported rather
// than silently truncated, because a truncated cover check answers "not
// suppressed" for a detection that was.
const MaxCandidates = 2000

// Shelf is the durable suppression plane. There is no memory implementation: a
// suppression that vanishes on a rollout turns a governed silence into a
// surprise, and the reverse — a suppression that outlives its record — is worse.
type Shelf struct {
	app core.App
	// Now supplies the instant a window is judged against. Tests set it.
	Now func() time.Time
}

// NewBase returns the durable shelf. Ensure has to have run first.
func NewBase(app core.App) *Shelf { return &Shelf{app: app} }

func (s *Shelf) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Suppress records a decision to keep a detection out of a queue.
func (s *Shelf) Suppress(ctx context.Context, org string, in *SuppressIn) (*Suppression, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	rule, kindName, value := strings.TrimSpace(in.Rule), strings.TrimSpace(in.Kind), strings.TrimSpace(in.Value)
	switch {
	case strings.TrimSpace(in.Reason) == "":
		return nil, ErrReason
	case strings.TrimSpace(in.By) == "":
		return nil, ErrDecider
	case rule == "" && value == "":
		return nil, ErrBroad
	case kindName != "" && !slices.Contains(history.Kinds, kindName):
		return nil, fmt.Errorf("%w: %q, want one of %s", ErrKind, kindName, strings.Join(history.Kinds, ", "))
	case value != "" && kindName == "":
		return nil, fmt.Errorf("%w: %q", ErrSubject, value)
	}

	at := s.now()
	sup := Suppression{
		ID: uuid.NewString(), Org: org,
		Rule: rule, Kind: kindName, Value: value,
		Reason: strings.TrimSpace(in.Reason), By: strings.TrimSpace(in.By),
		From: in.From.UTC(), Until: in.Until.UTC(),
	}
	if sup.From.IsZero() {
		sup.From = at
	}
	if !sup.Until.IsZero() && !sup.Until.After(sup.From) {
		return nil, fmt.Errorf("%w: until %s is not after from %s", ErrWindow, sup.Until, sup.From)
	}
	if !sup.Until.IsZero() && !sup.Until.After(at) {
		return nil, fmt.Errorf("%w: until %s has passed", ErrWindow, sup.Until)
	}

	row, err := kind.New(s.app, org)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Id = sup.ID
	write(row, sup)
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return &sup, nil
}

// Lift ends a suppression. The row stays and records who ended it and why.
func (s *Shelf) Lift(ctx context.Context, org string, in *LiftIn) (*Suppression, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, ErrReason
	}
	if strings.TrimSpace(in.By) == "" {
		return nil, ErrDecider
	}
	// Scoped by the tenant, so naming another institution's suppression id reads
	// as "no such suppression" rather than as a refusal that confirms it exists.
	rows, err := kind.Find(s.app, org, "id = {:id}", "", 1, dbx.Params{"id": strings.TrimSpace(in.ID)})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotHere, in.ID)
	}
	sup := read(rows[0])
	if !sup.Lifted.IsZero() {
		return nil, fmt.Errorf("%w: %s", ErrLifted, sup.ID)
	}
	sup.Lifted = s.now()
	sup.LiftedBy = strings.TrimSpace(in.By)
	sup.LiftWhy = strings.TrimSpace(in.Reason)
	write(rows[0], sup)
	if err := s.app.Save(rows[0]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return &sup, nil
}

// Ledger reads a tenant's suppressions.
func (s *Shelf) Ledger(ctx context.Context, org string, in *LedgerIn) (*Ledger, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	filter, params := "", dbx.Params{}
	if r := strings.TrimSpace(in.Rule); r != "" {
		filter, params[fieldRule] = fieldRule+" = {:"+fieldRule+"}", r
	}
	rows, err := kind.Find(s.app, org, filter, "-"+fieldFrom, limit+1, params)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := &Ledger{Suppressions: make([]Suppression, 0, len(rows))}
	if len(rows) > limit {
		rows, out.Cut = rows[:limit], true
	}
	at := s.now()
	for _, row := range rows {
		sup := read(row)
		if in.InForce && !sup.InForce(at) {
			continue
		}
		out.Suppressions = append(out.Suppressions, sup)
	}
	return out, nil
}

// Cover answers whether a suppression in force covers this activation, and which
// one.
//
// The narrowest wins. A tenant that has suppressed a whole rule and then made a
// specific decision about one account should see the specific one named on the
// activation, because that is the decision a reviewer will be asked about.
func (s *Shelf) Cover(ctx context.Context, org string, in *CoverIn) (*Cover, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	at := in.At.UTC()
	if at.IsZero() {
		at = s.now()
	}
	rule := strings.TrimSpace(in.Rule)

	// The candidates are this tenant's suppressions naming this rule, plus the
	// blanket ones. Both halves are bound parameters; neither is interpolated.
	rows, err := kind.Find(s.app, org, fieldRule+" = {:rule} || "+fieldRule+" = ''", "", MaxCandidates+1,
		dbx.Params{"rule": rule})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) > MaxCandidates {
		return nil, fmt.Errorf("%w: more than %d suppressions bear on rule %q, so a cover check cannot be answered completely",
			ErrStore, MaxCandidates, rule)
	}

	var best *Suppression
	for _, row := range rows {
		sup := read(row)
		if !sup.InForce(at) || !sup.Covers(rule, in.Kind, in.Value) {
			continue
		}
		if best == nil || better(sup, *best) {
			s := sup
			best = &s
		}
	}
	if best == nil {
		return &Cover{}, nil
	}
	return &Cover{Covered: true, Suppression: best}, nil
}

// better breaks the tie between two covering suppressions: the narrower one, then
// the more recent decision, then the id. Deterministic, because an activation
// marked with a different suppression on a replay is an activation nobody can
// reconcile.
func better(a, b Suppression) bool {
	if a.Reach() != b.Reach() {
		return a.Reach() > b.Reach()
	}
	if !a.From.Equal(b.From) {
		return a.From.After(b.From)
	}
	return a.ID < b.ID
}

// Sort orders suppressions the way a ledger reads: most recent decision first.
func Sort(in []Suppression) {
	sort.SliceStable(in, func(i, j int) bool { return in[i].From.After(in[j].From) })
}

func write(row *core.Record, s Suppression) {
	row.Set(fieldRule, s.Rule)
	row.Set(fieldKind, s.Kind)
	row.Set(fieldValue, s.Value)
	row.Set(fieldReason, s.Reason)
	row.Set(fieldBy, s.By)
	row.Set(fieldFrom, s.From.UTC())
	row.Set(fieldUntil, moment(s.Until))
	row.Set(fieldLifted, moment(s.Lifted))
	row.Set(fieldLiftBy, s.LiftedBy)
	row.Set(fieldLiftWhy, s.LiftWhy)
}

func read(row *core.Record) Suppression {
	return Suppression{
		ID:       row.Id,
		Org:      row.GetString(store.Org),
		Rule:     row.GetString(fieldRule),
		Kind:     row.GetString(fieldKind),
		Value:    row.GetString(fieldValue),
		Reason:   row.GetString(fieldReason),
		By:       row.GetString(fieldBy),
		From:     row.GetDateTime(fieldFrom).Time(),
		Until:    row.GetDateTime(fieldUntil).Time(),
		Lifted:   row.GetDateTime(fieldLifted).Time(),
		LiftedBy: row.GetString(fieldLiftBy),
		LiftWhy:  row.GetString(fieldLiftWhy),
	}
}

// moment renders an optional instant the way a date column holds "unset": the
// empty string. A zero time written as a date is year one, which compares as a
// real moment in the past — so an open-ended suppression would read as one that
// closed two thousand years ago.
func moment(t time.Time) any {
	if t.IsZero() {
		return ""
	}
	return t.UTC()
}
