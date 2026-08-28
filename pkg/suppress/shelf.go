// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
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
		// Lifted is in the index because the cover read asks for rows that are
		// NOT lifted, and it asks on the ingest path: a ledger that keeps every
		// decision forever (by design — a lifted suppression is a record of a
		// decision) would otherwise make the hot read scan an institution's whole
		// history of them.
		{Name: "rule", Fields: []string{store.Org, fieldRule, fieldLifted}},
		// The subject read, for the ledger view of one account or address.
		{Name: "subject", Fields: []string{store.Org, fieldKind, fieldValue}},
	},
}

// Ensure creates what suppressions are kept in. Idempotent, so it runs on every
// start.
func Ensure(app core.App) error { return kind.Ensure(app) }

// MaxCandidates bounds the rows one Cover reads, and MaxInForce bounds how many
// suppressions a tenant may have in force on one rule.
//
// The two are one mechanism seen from both ends, and the smaller one is the one
// that matters. A cover check runs on the INGEST path, once per activation, and
// ingest is the request that must not fail: a transaction that cannot be
// processed is a payment that does not happen. So the bound is enforced where a
// refusal costs a tenant an operator request — at declaration, against that
// tenant's own suppressions on that rule, answered with ErrCrowded — and not
// where a refusal costs it every payment.
//
// MaxInForce is well below MaxCandidates so that the read bound is unreachable by
// declaring; it stays as a second line, and reaching it degrades (Cover.Partial)
// rather than refusing. Degrading in this direction is safe in the way that
// matters for a monitoring control: an unfound suppression produces an alert
// nobody wanted, and an ingest failure produces silence. Noise is recoverable and
// silence is not.
//
// Both are per tenant. One institution's suppressions are never read by another's
// cover check and can never crowd it out.
const (
	MaxCandidates = 2000
	MaxInForce    = 500
)

// Shelf is the durable suppression plane. There is no memory implementation: a
// suppression that vanishes on a rollout turns a governed silence into a
// surprise, and the reverse — a suppression that outlives its record — is worse.
type Shelf struct {
	app core.App
	// Now supplies the instant a window is judged against. Tests set it.
	Now func() time.Time

	// The two bounds, at zero meaning the constants above. They are UNEXPORTED
	// and there is no constructor that sets them, so a deployment cannot lower
	// MaxInForce into a control that refuses ordinary work, nor raise
	// MaxCandidates into a read that costs the ingest path a scan. What they are
	// for is the tests: proving the crowding behaviour needs thousands of rows at
	// the real numbers, and a bound whose behaviour is untested is a bound
	// nobody has seen work.
	candidates, inForceMax int
}

func (s *Shelf) maxCandidates() int {
	if s.candidates > 0 {
		return s.candidates
	}
	return MaxCandidates
}

func (s *Shelf) maxInForce() int {
	if s.inForceMax > 0 {
		return s.inForceMax
	}
	return MaxInForce
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
	case in.By.Trim() == "":
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
		Reason: strings.TrimSpace(in.Reason), By: in.By.Trim(),
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

	// The crowding bound, checked here because here is where a refusal costs one
	// operator request. The same crowding met on the ingest path costs a payment.
	inForce, err := s.inForce(org, rule, at)
	if err != nil {
		return nil, err
	}
	if inForce >= s.maxInForce() {
		return nil, fmt.Errorf("%w: %d in force on %s, at most %d; lift one before declaring another",
			ErrCrowded, inForce, ruleName(rule), s.maxInForce())
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

// covering is the one definition of which rows could cover an activation of a
// rule for this tenant: the ones naming it, plus the blanket ones, MINUS the
// lifted.
//
// Excluding the lifted in the QUERY and not in the loop is the whole of it.
// Lifting never deletes — a lifted suppression is the record of a decision, and
// the ledger keeps it — so declare-and-lift churn grows the rows on a rule
// without ever reaching the crowding bound, which counts only what is in force.
// Read unordered and paged at MaxCandidates, those dead rows eventually fill the
// page and the institution's live, declared suppression stops being found: the
// MLRO's decision silently stops applying, and only a query for Unchecked would
// ever reveal it. A row that cannot cover is not a candidate, so it is not read.
//
// It is one function because [Shelf.Cover] and [Shelf.inForce] must agree. Two
// copies of this predicate is how a bound comes to count a different set than
// the read it is bounding.
func (s *Shelf) covering(org, rule string) ([]*core.Record, error) {
	rows, err := kind.Find(s.app, org,
		"("+fieldRule+" = {:rule} || "+fieldRule+" = '') && "+fieldLifted+" = ''",
		"", s.maxCandidates()+1, dbx.Params{"rule": rule})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return rows, nil
}

// inForce counts this tenant's suppressions that would be candidates for a cover
// check of this rule — the ones naming it, plus the blanket ones — and that are
// in force now. Lifted and expired rows are not candidates and are not counted:
// the ledger keeps them forever and a bound over the ledger would eventually
// refuse an institution for its own history.
func (s *Shelf) inForce(org, rule string, at time.Time) (int, error) {
	rows, err := s.covering(org, rule)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		if read(row).InForce(at) {
			n++
		}
	}
	return n, nil
}

func ruleName(rule string) string {
	if rule == "" {
		return "every rule"
	}
	return "rule " + rule
}

// Lift ends a suppression. The row stays and records who ended it and why.
func (s *Shelf) Lift(ctx context.Context, org string, in *LiftIn) (*Suppression, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, ErrReason
	}
	if in.By.Trim() == "" {
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
	sup.LiftedBy = in.By.Trim()
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
//
// # It degrades and never refuses
//
// This runs on the ingest path, once per activation, and its caller cannot
// process a transaction it cannot record. An error here would therefore be a
// tenant-triggerable outage of its OWN payments — declare enough suppressions on
// one rule and every transaction that fires it stops — and the retry of a failed
// ingest is what turns one incomplete read into a double-counted one.
//
// So a read that hits its bound answers from what it read and says so
// (Cover.Partial). The failure that produces is an alert that a suppression
// beyond the page would have silenced: noise, which a reviewer dismisses, rather
// than silence, which nobody sees. Declaring past the bound is refused at
// [Shelf.Suppress] where a refusal costs one request, so a tenant that reaches
// this state has to have been let there.
func (s *Shelf) Cover(ctx context.Context, org string, in *CoverIn) (*Cover, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	at := in.At.UTC()
	if at.IsZero() {
		at = s.now()
	}
	rule := strings.TrimSpace(in.Rule)

	rows, err := s.covering(org, rule)
	if err != nil {
		return nil, err
	}
	out := &Cover{}
	if len(rows) > s.maxCandidates() {
		rows, out.Partial = rows[:s.maxCandidates()], true
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
		return out, nil
	}
	out.Covered, out.Suppression = true, best
	return out, nil
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
