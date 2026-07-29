// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package store is how this codebase keeps anything that has to survive a
// restart. There is one way, and this is it.
//
// A durable store is a [Kind]: a collection's name, the fields it holds, and the
// indexes that follow the queries it will be asked. [Kind.Ensure] creates it and
// brings an existing one up to the declaration, so it runs on every start and the
// schema the reader queries is the schema that exists. Written as a migration
// instead, the two drift, and a drifted schema does not fail — it returns nothing,
// which a monitoring system reads as nothing to report.
//
// Three rules, each held up by the code rather than by discipline:
//
//   - Every collection carries [Org]. Ensure adds it, so a Kind cannot be
//     declared without a tenant, and [Kind.New] sets it, so a record cannot be
//     written without one.
//   - Every read is scoped. [Kind.Find] ands the tenant predicate onto the
//     caller's filter and refuses an empty org. A query that has to cross tenants
//     says [Kind.Across] instead, so the whole set of them is one grep.
//   - Field names are constants in the package that declares the Kind, so the
//     writer and the reader name the same column and a rename cannot compile on
//     one side only. A silent disagreement between them produces an empty result,
//     and an empty result is indistinguishable from an empty ledger.
//
// Values are bound as parameters, never interpolated. A value that reaches the
// filter as text can change the shape of the query: it can widen the result to
// another tenant's records or narrow it to none, and narrowing it to none is the
// dangerous direction.
package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/types"
	"github.com/hanzoai/dbx"
)

// Org is the field that carries the tenant. Every Kind has it.
const Org = "org_id"

// orgParam is what the tenant predicate binds to. It is deliberately not "org",
// so that a caller's own parameter can never shadow it.
const orgParam = "store_org"

// Index is one index over a Kind, named for the query it serves rather than for
// the fields it happens to contain.
type Index struct {
	Name   string
	Fields []string
	Unique bool
}

// Kind declares one collection: what it is called, what it holds, and the
// indexes its queries need. It is a value, and the app is passed to each
// operation, so the same Kind serves a plain app and a transaction.
type Kind struct {
	Name    string
	Fields  []core.Field
	Indexes []Index
}

// Ensure creates the collection if it is absent and adds anything the
// declaration has that the existing collection lacks. It is idempotent, so it
// runs on every start, and it is additive: it never drops a field or an index,
// because a field this binary does not know about may be the one holding a record
// another obligation depends on.
func (k Kind) Ensure(app core.App) error {
	collection, err := app.FindCollectionByNameOrId(k.Name)
	if err != nil {
		collection = core.NewBaseCollection(k.Name)
		collection.Fields.Add(&core.TextField{Name: Org, Required: true})
	}

	changed := false
	for _, f := range k.Fields {
		if collection.Fields.GetByName(f.GetName()) == nil {
			collection.Fields.Add(f)
			changed = true
		}
	}
	for _, i := range k.Indexes {
		if !indexed(collection.Indexes, k.name(i)) {
			collection.Indexes = append(collection.Indexes, k.statement(i))
			changed = true
		}
	}
	if !changed && !collection.IsNew() {
		return nil
	}
	if err := app.Save(collection); err != nil {
		return fmt.Errorf("store: %s: %w", k.Name, err)
	}
	return nil
}

// New builds an unsaved record already scoped to an org, which is the only way to
// get one: a record whose tenant is decided after the fact is a record that can
// be written to the wrong one.
func (k Kind) New(app core.App, org string) (*core.Record, error) {
	if org == "" {
		return nil, fmt.Errorf("store: %s: refusing to write a record that belongs to no organisation", k.Name)
	}
	collection, err := app.FindCollectionByNameOrId(k.Name)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", k.Name, err)
	}
	r := core.NewRecord(collection)
	r.Set(Org, org)
	return r, nil
}

// Find returns one org's records. The tenant predicate is added here rather than
// left to the caller's filter, so a forgotten clause cannot read across tenants.
// A limit of 0 returns everything matched.
//
// There is no offset. A walk that pages by offset over a collection something
// else is deleting from skips rows, and skipping a row in a retention ledger is
// how a record outlives its period unnoticed; page by the last key seen instead.
func (k Kind) Find(app core.App, org, filter, sort string, limit int, params dbx.Params) ([]*core.Record, error) {
	if org == "" {
		return nil, fmt.Errorf("store: %s: refusing an unscoped query", k.Name)
	}
	if params == nil {
		params = dbx.Params{}
	}
	params[orgParam] = org
	scoped := Org + " = {:" + orgParam + "}"
	if filter != "" {
		scoped += " && (" + filter + ")"
	}
	return k.records(app, scoped, sort, limit, params)
}

// Across reads every org's records. It exists for the operations an obligation
// puts on the operator rather than on a tenant — destroying what has expired
// wherever it is — and it is named so that every crossing of the tenant boundary
// is found by searching for one word.
func (k Kind) Across(app core.App, filter, sort string, limit int, params dbx.Params) ([]*core.Record, error) {
	return k.records(app, filter, sort, limit, params)
}

// Count is how many records the collection holds, in every org.
func (k Kind) Count(app core.App) (int, error) {
	n, err := app.CountRecords(k.Name)
	if err != nil {
		return 0, fmt.Errorf("store: %s: %w", k.Name, err)
	}
	return int(n), nil
}

func (k Kind) records(app core.App, filter, sort string, limit int, params dbx.Params) ([]*core.Record, error) {
	out, err := app.FindRecordsByFilter(k.Name, filter, sort, limit, 0, bind(params))
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", k.Name, err)
	}
	return out, nil
}

// bind puts the parameters in the form the store compares values in.
//
// A moment is the one that has to be converted. Dates are stored in a single fixed
// layout and compared as text; a Go time bound straight into the filter arrives in
// a different layout and compares as neither greater nor less than what it means.
// Nothing errors — the query simply answers with the wrong rows, which for a walk
// that pages by the last record seen is a page that never ends, and for an expiry
// check is a record destroyed a year early or never at all. Converting here rather
// than at each call site is what makes that unforgettable.
func bind(params dbx.Params) dbx.Params {
	for name, value := range params {
		moment, ok := value.(time.Time)
		if !ok {
			continue
		}
		converted, err := types.ParseDateTime(moment)
		if err != nil {
			// ParseDateTime accepts every time.Time; a value it rejects would be a
			// filter comparing against something that is not a moment at all.
			continue
		}
		params[name] = converted
	}
	return params
}

// name is an index's full name, which is unique across collections because an
// index name is a schema-wide identifier.
func (k Kind) name(i Index) string { return "idx_" + k.Name + "_" + i.Name }

func (k Kind) statement(i Index) string {
	unique := ""
	if i.Unique {
		unique = "UNIQUE "
	}
	cols := make([]string, 0, len(i.Fields))
	for _, f := range i.Fields {
		cols = append(cols, "`"+f+"`")
	}
	return fmt.Sprintf("CREATE %sINDEX `%s` ON `%s` (%s)", unique, k.name(i), k.Name, strings.Join(cols, ", "))
}

// indexed reports whether an index of that name already exists, by name rather
// than by statement: the stored statement is normalised on save, so comparing
// text would declare every index missing and try to create it again.
func indexed(existing []string, name string) bool {
	for _, s := range existing {
		if strings.Contains(s, "`"+name+"`") {
			return true
		}
	}
	return false
}
