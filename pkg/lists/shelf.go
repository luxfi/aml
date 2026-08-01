// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package lists

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/store"
)

// Field names. Constants because the writer and the reader must name the same
// column: a silent disagreement between them returns nothing, and nothing is
// indistinguishable from a list with no entries.
const (
	fieldName    = "name"
	fieldKind    = "kind"
	fieldClass   = "class"
	fieldNote    = "note"
	fieldBy      = "by"
	fieldAdded   = "added"
	fieldRemoved = "removed"
	fieldRanges  = "ranges"
	fieldCreated = "created"
	fieldUpdated = "updated"

	fieldList   = "list"
	fieldValue  = "value"
	fieldRange  = "range"
	fieldReason = "reason"
	fieldAt     = "at"
	fieldUntil  = "until"
	fieldOff    = "off"
	fieldOffBy  = "off_by"
	fieldOffWhy = "off_why"
)

var listKind = store.Kind{
	Name: "aml_lists",
	Fields: []core.Field{
		&core.TextField{Name: fieldName, Required: true},
		&core.TextField{Name: fieldKind, Required: true},
		&core.TextField{Name: fieldClass, Required: true},
		&core.TextField{Name: fieldNote},
		&core.TextField{Name: fieldBy, Required: true},
		&core.NumberField{Name: fieldAdded},
		&core.NumberField{Name: fieldRemoved},
		&core.NumberField{Name: fieldRanges},
		&core.DateField{Name: fieldCreated, Required: true},
		&core.DateField{Name: fieldUpdated, Required: true},
	},
	Indexes: []store.Index{
		// One list of a name per tenant. Unique on the tenant AND the name, never
		// on the name alone: two institutions naming a list `deny` is ordinary.
		{Name: "name", Fields: []string{store.Org, fieldName}, Unique: true},
	},
}

var entryKind = store.Kind{
	Name: "aml_list_entries",
	Fields: []core.Field{
		&core.TextField{Name: fieldList, Required: true},
		&core.TextField{Name: fieldValue, Required: true},
		&core.BoolField{Name: fieldRange},
		&core.TextField{Name: fieldReason, Required: true},
		&core.TextField{Name: fieldBy, Required: true},
		&core.DateField{Name: fieldAt, Required: true},
		&core.DateField{Name: fieldUntil},
		&core.DateField{Name: fieldOff},
		&core.TextField{Name: fieldOffBy},
		&core.TextField{Name: fieldOffWhy},
	},
	Indexes: []store.Index{
		// The match: this tenant, this list, this exact value. Unique, so one
		// value is one row and a restatement cannot double it.
		{Name: "value", Fields: []string{store.Org, fieldList, fieldValue}, Unique: true},
		// The range scan: this tenant's ranges on this list, which is the only
		// read that cannot be an equality.
		{Name: "range", Fields: []string{store.Org, fieldList, fieldRange}},
	},
}

// Ensure creates what lists are kept in. Idempotent, so it runs on every start.
func Ensure(app core.App) error {
	if err := listKind.Ensure(app); err != nil {
		return err
	}
	return entryKind.Ensure(app)
}

// Shelf is the durable list plane. There is no memory implementation: a deny list
// that empties on a rollout is a control that was off, and the difference is
// invisible from the outside because "not listed" is the ordinary answer.
type Shelf struct {
	app core.App
	// Now supplies the instant a validity window is judged against. Tests set it;
	// production leaves it nil and gets the wall clock.
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

// Declare creates a list.
func (s *Shelf) Declare(ctx context.Context, org string, in *DeclareIn) (*List, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	name := trim(in.Name)
	switch {
	case name == "":
		return nil, ErrName
	case in.Kind != Allow && in.Kind != Deny:
		return nil, fmt.Errorf("%w: %q", ErrKind, in.Kind)
	case !known(Classes, in.Class):
		return nil, fmt.Errorf("%w: %q", ErrClass, in.Class)
	case trim(in.By) == "":
		return nil, ErrDecider
	}
	if _, err := s.list(org, name); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrExists, name)
	} else if !isNoList(err) {
		return nil, err
	}

	at := s.now()
	row, err := listKind.New(s.app, org)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	l := List{Org: org, Name: name, Kind: in.Kind, Class: in.Class, Note: trim(in.Note), By: trim(in.By), Created: at, Updated: at}
	writeList(row, l)
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: declaring %s: %w", ErrStore, name, err)
	}
	return &l, nil
}

// Catalog is every list this tenant has declared.
func (s *Shelf) Catalog(ctx context.Context, org string, _ *CatalogIn) (*Catalog, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	rows, err := listKind.Find(s.app, org, "", fieldName, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := &Catalog{Kinds: Kinds, Classes: Classes, Lists: make([]List, 0, len(rows))}
	for _, row := range rows {
		out.Lists = append(out.Lists, readList(row))
	}
	return out, nil
}

// Add puts values on a list, in one transaction: a half-written import is a
// control whose contents nobody can state.
func (s *Shelf) Add(ctx context.Context, org string, in *AddIn) (*List, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if trim(in.By) == "" {
		return nil, ErrDecider
	}
	if len(in.Values) == 0 {
		return nil, ErrEmpty
	}
	if len(in.Values) > MaxValues {
		return nil, fmt.Errorf("%w: %d values, at most %d", ErrMaxValues, len(in.Values), MaxValues)
	}
	l, err := s.list(org, trim(in.Name))
	if err != nil {
		return nil, err
	}

	// Normalise and check everything before writing anything, so one unreadable
	// value refuses the request instead of landing half of it.
	type prepared struct {
		value   string
		isRange bool
		reason  string
		until   time.Time
	}
	ready := make([]prepared, 0, len(in.Values))
	for _, v := range in.Values {
		norm, isRange, err := normalise(l.Class, v.Value)
		if err != nil {
			return nil, err
		}
		if trim(v.Reason) == "" {
			return nil, fmt.Errorf("%w: %s", ErrReason, v.Value)
		}
		ready = append(ready, prepared{value: norm, isRange: isRange, reason: trim(v.Reason), until: v.Until.UTC()})
	}

	at := s.now()
	by := trim(in.By)
	updated := *l
	err = s.app.RunInTransaction(func(tx core.App) error {
		for _, p := range ready {
			found, ferr := entryKind.Find(tx, org, fieldList+" = {:list} && "+fieldValue+" = {:value}", "", 1,
				dbx.Params{"list": l.Name, "value": p.value})
			if ferr != nil {
				return fmt.Errorf("%w: %w", ErrStore, ferr)
			}
			var row *core.Record
			if len(found) > 0 {
				row = found[0]
				// A restatement clears the removal: the value is on the list again,
				// and the row names who put it back.
				row.Set(fieldOff, "")
				row.Set(fieldOffBy, "")
				row.Set(fieldOffWhy, "")
			} else {
				if p.isRange {
					if updated.Ranges >= MaxRanges {
						return fmt.Errorf("%w: %s holds %d, at most %d", ErrCrowded, l.Name, updated.Ranges, MaxRanges)
					}
					updated.Ranges++
				}
				updated.Added++
				var nerr error
				row, nerr = entryKind.New(tx, org)
				if nerr != nil {
					return fmt.Errorf("%w: %w", ErrStore, nerr)
				}
				row.Set(fieldList, l.Name)
				row.Set(fieldValue, p.value)
				row.Set(fieldRange, p.isRange)
			}
			row.Set(fieldReason, p.reason)
			row.Set(fieldBy, by)
			row.Set(fieldAt, at)
			row.Set(fieldUntil, moment(p.until))
			if serr := tx.Save(row); serr != nil {
				return fmt.Errorf("%w: adding %s to %s: %w", ErrStore, p.value, l.Name, serr)
			}
		}
		updated.Updated = at
		return s.saveList(tx, updated)
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Remove takes a value off a list without destroying the record of it having been
// there. The row keeps its reason and its original decider and gains the removal's
// own.
func (s *Shelf) Remove(ctx context.Context, org string, in *RemoveIn) (*List, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if trim(in.By) == "" {
		return nil, ErrDecider
	}
	if trim(in.Reason) == "" {
		return nil, ErrReason
	}
	l, err := s.list(org, trim(in.Name))
	if err != nil {
		return nil, err
	}
	norm, _, err := normalise(l.Class, in.Value)
	if err != nil {
		return nil, err
	}

	at := s.now()
	updated := *l
	err = s.app.RunInTransaction(func(tx core.App) error {
		found, ferr := entryKind.Find(tx, org, fieldList+" = {:list} && "+fieldValue+" = {:value}", "", 1,
			dbx.Params{"list": l.Name, "value": norm})
		if ferr != nil {
			return fmt.Errorf("%w: %w", ErrStore, ferr)
		}
		if len(found) == 0 {
			return fmt.Errorf("%w: %s on %s", ErrNoEntry, norm, l.Name)
		}
		row := found[0]
		if row.GetString(fieldOff) != "" {
			return fmt.Errorf("%w: %s on %s", ErrRetired, norm, l.Name)
		}
		row.Set(fieldOff, at)
		row.Set(fieldOffBy, trim(in.By))
		row.Set(fieldOffWhy, trim(in.Reason))
		if serr := tx.Save(row); serr != nil {
			return fmt.Errorf("%w: removing %s from %s: %w", ErrStore, norm, l.Name, serr)
		}
		updated.Removed++
		updated.Updated = at
		return s.saveList(tx, updated)
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Entries reads a list.
func (s *Shelf) Entries(ctx context.Context, org string, in *EntriesIn) (*Page, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	l, err := s.list(org, trim(in.Name))
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	// One more than asked, so a full page can be told from the end of the list.
	rows, err := entryKind.Find(s.app, org, fieldList+" = {:list}", fieldValue, limit+1, dbx.Params{"list": l.Name})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	page := &Page{List: *l, Entries: make([]Entry, 0, len(rows))}
	if len(rows) > limit {
		rows, page.Cut = rows[:limit], true
	}
	at := s.now()
	for _, row := range rows {
		e := readEntry(row)
		if in.Live && !e.Live(at) {
			continue
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

// Lookup answers whether one value is on one list, and on what entry.
func (s *Shelf) Lookup(ctx context.Context, org string, in *LookupIn) (*Match, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	l, err := s.list(org, trim(in.Name))
	if err != nil {
		return nil, err
	}
	norm, isRange, err := normalise(l.Class, in.Value)
	if err != nil {
		return nil, err
	}
	if isRange {
		return nil, fmt.Errorf("%w: %q is a range, and a range is not a value to look up", ErrValue, in.Value)
	}
	at := s.now()

	rows, err := entryKind.Find(s.app, org, fieldList+" = {:list} && "+fieldValue+" = {:value}", "", 1,
		dbx.Params{"list": l.Name, "value": norm})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) > 0 {
		if e := readEntry(rows[0]); e.Live(at) {
			return &Match{Listed: true, Kind: l.Kind, Entry: &e}, nil
		}
	}
	if l.Class != IP || l.Ranges == 0 {
		return &Match{Kind: l.Kind}, nil
	}

	// The ranges. Bounded by MaxRanges at write time, so this is a bounded read
	// and not a scan that grows with the list.
	ranges, err := entryKind.Find(s.app, org, fieldList+" = {:list} && "+fieldRange+" = true", fieldValue, MaxRanges,
		dbx.Params{"list": l.Name})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	for _, row := range ranges {
		e := readEntry(row)
		if e.Live(at) && contains(e.Value, norm) {
			return &Match{Listed: true, Kind: l.Kind, Entry: &e}, nil
		}
	}
	return &Match{Kind: l.Kind}, nil
}

// list reads one declaration, or reports that the caller named a list nobody
// declared.
func (s *Shelf) list(org, name string) (*List, error) {
	if name == "" {
		return nil, ErrName
	}
	rows, err := listKind.Find(s.app, org, fieldName+" = {:name}", "", 1, dbx.Params{"name": name})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoList, name)
	}
	l := readList(rows[0])
	return &l, nil
}

func (s *Shelf) saveList(app core.App, l List) error {
	rows, err := listKind.Find(app, l.Org, fieldName+" = {:name}", "", 1, dbx.Params{"name": l.Name})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("%w: %s", ErrNoList, l.Name)
	}
	writeList(rows[0], l)
	if err := app.Save(rows[0]); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	return nil
}

func writeList(row *core.Record, l List) {
	row.Set(fieldName, l.Name)
	row.Set(fieldKind, l.Kind)
	row.Set(fieldClass, l.Class)
	row.Set(fieldNote, l.Note)
	row.Set(fieldBy, l.By)
	row.Set(fieldAdded, l.Added)
	row.Set(fieldRemoved, l.Removed)
	row.Set(fieldRanges, l.Ranges)
	row.Set(fieldCreated, l.Created.UTC())
	row.Set(fieldUpdated, l.Updated.UTC())
}

func readList(row *core.Record) List {
	return List{
		Org:     row.GetString(store.Org),
		Name:    row.GetString(fieldName),
		Kind:    row.GetString(fieldKind),
		Class:   row.GetString(fieldClass),
		Note:    row.GetString(fieldNote),
		By:      row.GetString(fieldBy),
		Added:   int64(row.GetInt(fieldAdded)),
		Removed: int64(row.GetInt(fieldRemoved)),
		Ranges:  int64(row.GetInt(fieldRanges)),
		Created: row.GetDateTime(fieldCreated).Time(),
		Updated: row.GetDateTime(fieldUpdated).Time(),
	}
}

func readEntry(row *core.Record) Entry {
	return Entry{
		Org:       row.GetString(store.Org),
		List:      row.GetString(fieldList),
		Value:     row.GetString(fieldValue),
		Range:     row.GetBool(fieldRange),
		Reason:    row.GetString(fieldReason),
		By:        row.GetString(fieldBy),
		At:        row.GetDateTime(fieldAt).Time(),
		Until:     row.GetDateTime(fieldUntil).Time(),
		Removed:   row.GetDateTime(fieldOff).Time(),
		RemoveBy:  row.GetString(fieldOffBy),
		RemoveWhy: row.GetString(fieldOffWhy),
	}
}

// moment renders an optional instant the way a date column holds "unset": the
// empty string, which is what the filters compare against. A zero time written as
// a date is year one, which sorts and compares as a real moment in the past.
func moment(t time.Time) any {
	if t.IsZero() {
		return ""
	}
	return t.UTC()
}

func trim(s string) string { return strings.TrimSpace(s) }

func known(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func isNoList(err error) bool { return errors.Is(err, ErrNoList) }
