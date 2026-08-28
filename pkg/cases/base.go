// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package cases

import (
	"fmt"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/store"
	"github.com/luxfi/aml/pkg/types"
)

// Field names, declared once because the writer and the reader have to agree. A
// disagreement between them does not fail — it returns nothing, and a case list
// that returns nothing looks exactly like an institution with no open cases.
const (
	fieldCase       = "case_id"
	fieldNumber     = "number"
	fieldStatus     = "status"
	fieldSeverity   = "severity"
	fieldEntities   = "entity_ids"
	fieldAlerts     = "alert_ids"
	fieldAssignee   = "assignee"
	fieldOpened     = "opened"
	fieldClosed     = "closed"
	fieldResolution = "resolution"
	fieldAssessment = "assessment"
	fieldCreated    = "created_at"
	fieldUpdated    = "updated_at"

	// On the timeline only.
	fieldEvent  = "event_id"
	fieldAuthor = "author"
	fieldKind   = "kind"
	fieldBody   = "body"
	fieldFile   = "file_path"
	fieldAt     = "at"
)

// caseKind is where cases live.
var caseKind = store.Kind{
	Name: "aml_cases",
	Fields: []core.Field{
		&core.TextField{Name: fieldCase, Required: true},
		&core.NumberField{Name: fieldNumber, Required: true},
		&core.TextField{Name: fieldStatus, Required: true},
		&core.TextField{Name: fieldSeverity},
		&core.JSONField{Name: fieldEntities},
		&core.JSONField{Name: fieldAlerts},
		&core.TextField{Name: fieldAssignee},
		&core.DateField{Name: fieldOpened, Required: true},
		&core.DateField{Name: fieldClosed},
		&core.TextField{Name: fieldResolution},
		&core.TextField{Name: fieldAssessment},
		&core.DateField{Name: fieldCreated, Required: true},
		&core.DateField{Name: fieldUpdated, Required: true},
	},
	Indexes: []store.Index{
		// A case is reached by its id, and an id is unique within the tenant that
		// owns it. Unique because writing the same case twice is a bug, not a
		// second case.
		{Name: "case", Fields: []string{store.Org, fieldCase}, Unique: true},
		// The queue: an org's cases, by status.
		{Name: "queue", Fields: []string{store.Org, fieldStatus, fieldNumber}},
		// The next case number, which is the highest one this org has used.
		{Name: "number", Fields: []string{store.Org, fieldNumber}},
	},
}

// eventKind is the case timeline. It is a collection of its own because a case
// has many events and a value that repeats cannot be a column.
var eventKind = store.Kind{
	Name: "aml_case_events",
	Fields: []core.Field{
		&core.TextField{Name: fieldCase, Required: true},
		&core.TextField{Name: fieldEvent, Required: true},
		&core.TextField{Name: fieldAuthor},
		&core.TextField{Name: fieldKind, Required: true},
		&core.TextField{Name: fieldBody},
		&core.TextField{Name: fieldFile},
		&core.DateField{Name: fieldAt, Required: true},
	},
	Indexes: []store.Index{
		// The timeline, oldest first.
		{Name: "timeline", Fields: []string{fieldCase, fieldAt}},
	},
}

// Ensure creates what cases are kept in. It is idempotent, so it runs on every
// start, and it has to have run before the first case is opened.
func Ensure(app core.App) error {
	if err := caseKind.Ensure(app); err != nil {
		return err
	}
	return eventKind.Ensure(app)
}

// durable keeps cases in Base collections, which survive a restart.
type durable struct{ app core.App }

// NewBase returns a case store that survives a restart. [Ensure] has to have run
// first.
//
// This is what an instance serves from. A case is the record that an alert was
// considered and what was decided (AMLR Art. 77(1)(b); JMLSG 6.32), and a record
// that a rollout empties is not a record — the question it answers comes back
// unanswerable at exactly the moment somebody asks it.
//
// It takes no bound and there is no constructor that gives it one, so this store
// never evicts. Surviving the restart was only half of it: a record the case
// plane deletes on a timer of its own is gone just as completely, and quietly,
// because nothing about it looks like a failure. Expiry is pkg/retention's,
// where the clock is five years, the sweep is per tenant, and the disposal is
// proven.
func NewBase(app core.App) *Store {
	return &Store{shelf: durable{app: app}}
}

func (d durable) put(c *types.Case) error {
	found, err := caseKind.Find(d.app, c.OrgID, fieldCase+" = {:id}", "", 1, dbx.Params{"id": c.ID})
	if err != nil {
		return err
	}
	var r *core.Record
	if len(found) > 0 {
		r = found[0]
	} else if r, err = caseKind.New(d.app, c.OrgID); err != nil {
		return err
	}

	r.Set(fieldCase, c.ID)
	r.Set(fieldNumber, c.Number)
	r.Set(fieldStatus, c.Status)
	r.Set(fieldSeverity, c.Severity)
	r.Set(fieldEntities, c.EntityIDs)
	r.Set(fieldAlerts, c.AlertIDs)
	r.Set(fieldAssignee, c.AssigneeID)
	r.Set(fieldOpened, c.OpenedAt)
	if c.ClosedAt != nil {
		r.Set(fieldClosed, *c.ClosedAt)
	} else {
		r.Set(fieldClosed, nil)
	}
	r.Set(fieldResolution, c.Resolution)
	r.Set(fieldAssessment, c.Assessment)
	r.Set(fieldCreated, c.CreatedAt)
	r.Set(fieldUpdated, c.UpdatedAt)

	if err := d.app.Save(r); err != nil {
		return fmt.Errorf("cases: save %s: %w", c.ID, err)
	}
	return nil
}

// get reads a case by id across orgs, because an id is all the caller has when
// it asks. The tenant is checked by that caller against the case it gets back —
// which is what [Store.Get]'s callers already do, and the only place it is done.
func (d durable) get(id string) (*types.Case, error) {
	found, err := caseKind.Across(d.app, fieldCase+" = {:id}", "", 1, dbx.Params{"id": id})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return asCase(found[0]), nil
}

func (d durable) list(org, status string) ([]*types.Case, error) {
	filter, params := "", dbx.Params{}
	if status != "" {
		filter, params = fieldStatus+" = {:status}", dbx.Params{"status": status}
	}
	found, err := caseKind.Find(d.app, org, filter, "-"+fieldNumber, 0, params)
	if err != nil {
		return nil, err
	}
	out := make([]*types.Case, 0, len(found))
	for _, r := range found {
		out = append(out, asCase(r))
	}
	return out, nil
}

func (d durable) each(org string, visit func(*types.Case) error) error {
	found, err := caseKind.Find(d.app, org, "", "-"+fieldNumber, 0, nil)
	if err != nil {
		return err
	}
	for _, r := range found {
		if err := visit(asCase(r)); err != nil {
			return err
		}
	}
	return nil
}

func (d durable) appendEvent(e types.CaseEvent) error {
	owner, err := d.get(e.CaseID)
	if err != nil {
		return err
	}
	if owner == nil {
		return ErrNotFound
	}
	r, err := eventKind.New(d.app, owner.OrgID)
	if err != nil {
		return err
	}
	r.Set(fieldCase, e.CaseID)
	r.Set(fieldEvent, e.ID)
	r.Set(fieldAuthor, e.AuthorID)
	r.Set(fieldKind, e.Kind)
	r.Set(fieldBody, e.Body)
	r.Set(fieldFile, e.FilePath)
	r.Set(fieldAt, e.CreatedAt)
	if err := d.app.Save(r); err != nil {
		return fmt.Errorf("cases: save event on %s: %w", e.CaseID, err)
	}
	return nil
}

func (d durable) events(caseID string) ([]types.CaseEvent, error) {
	found, err := eventKind.Across(d.app, fieldCase+" = {:id}", fieldAt, 0, dbx.Params{"id": caseID})
	if err != nil {
		return nil, err
	}
	out := make([]types.CaseEvent, 0, len(found))
	for _, r := range found {
		out = append(out, types.CaseEvent{
			ID:        r.GetString(fieldEvent),
			CaseID:    r.GetString(fieldCase),
			AuthorID:  r.GetString(fieldAuthor),
			Kind:      r.GetString(fieldKind),
			Body:      r.GetString(fieldBody),
			FilePath:  r.GetString(fieldFile),
			CreatedAt: r.GetDateTime(fieldAt).Time().UTC(),
		})
	}
	return out, nil
}

func (d durable) drop(org string, ids []string) error {
	for _, id := range ids {
		cs, err := caseKind.Find(d.app, org, fieldCase+" = {:id}", "", 0, dbx.Params{"id": id})
		if err != nil {
			return err
		}
		evs, err := eventKind.Find(d.app, org, fieldCase+" = {:id}", "", 0, dbx.Params{"id": id})
		if err != nil {
			return err
		}
		for _, r := range append(cs, evs...) {
			if err := d.app.Delete(r); err != nil {
				return fmt.Errorf("cases: delete %s: %w", id, err)
			}
		}
	}
	return nil
}

// number continues the org's sequence across a restart by reading the highest
// one that org has written. A counter that starts again at one gives two cases
// the same number, and a case number is what a file is referred to by.
//
// Scoped, so the sequence an institution sees is its own. Read across tenants it
// answered a question nobody asked it — how many cases everyone else has opened
// — in the gaps between one institution's own case numbers.
func (d durable) number(org string) (int64, error) {
	found, err := caseKind.Find(d.app, org, "", "-"+fieldNumber, 1, nil)
	if err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 1, nil
	}
	return int64(found[0].GetInt(fieldNumber)) + 1, nil
}

func asCase(r *core.Record) *types.Case {
	c := &types.Case{
		ID:         r.GetString(fieldCase),
		OrgID:      r.GetString(store.Org),
		Number:     int64(r.GetInt(fieldNumber)),
		Status:     r.GetString(fieldStatus),
		Severity:   r.GetString(fieldSeverity),
		AssigneeID: r.GetString(fieldAssignee),
		OpenedAt:   r.GetDateTime(fieldOpened).Time().UTC(),
		Resolution: r.GetString(fieldResolution),
		Assessment: r.GetString(fieldAssessment),
		CreatedAt:  r.GetDateTime(fieldCreated).Time().UTC(),
		UpdatedAt:  r.GetDateTime(fieldUpdated).Time().UTC(),
	}
	r.UnmarshalJSONField(fieldEntities, &c.EntityIDs)
	r.UnmarshalJSONField(fieldAlerts, &c.AlertIDs)
	if closed := r.GetDateTime(fieldClosed); !closed.IsZero() {
		at := closed.Time().UTC()
		c.ClosedAt = &at
	}
	return c
}

// stillOpen reports whether a closed case is inside the retention window.
func stillOpen(c *types.Case, cutoff time.Time) bool {
	return c.Status != types.CaseClosed || c.ClosedAt == nil || !c.ClosedAt.Before(cutoff)
}
