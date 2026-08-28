// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package history

import (
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/store"
)

// events is where transaction events are stored.
//
// Nothing here is required except the identifiers a window is keyed by: a
// transaction with no device is ordinary, but one with no organisation cannot be
// scoped to a tenant and one with no timestamp cannot be placed in a window.
//
// Windows are always read as (org, subject, time range), so the indexes follow the
// query rather than the fields. Without them every velocity rule is a table scan
// per rule per transaction, which is the cost model this design exists to avoid.
var events = store.Kind{
	Name: Collection,
	Fields: []core.Field{
		&core.TextField{Name: fieldTxID, Required: true},
		&core.DateField{Name: fieldAt, Required: true},
		&core.NumberField{Name: fieldUSD},
		&core.TextField{Name: fieldCurrency},
		&core.TextField{Name: fieldDirection},
		&core.TextField{Name: fieldUser},
		&core.TextField{Name: fieldAccount},
		&core.TextField{Name: fieldCounterparty},
		&core.TextField{Name: fieldDevice},
		&core.TextField{Name: fieldAddress},
		&core.TextField{Name: fieldJurisdiction},
		&core.TextField{Name: fieldSymbol},
	},
	Indexes: []store.Index{
		{Name: "org_at", Fields: []string{store.Org, fieldAt}},
		// The identity read. An event IS one transaction, so a re-offer of one is
		// not a second event — see Base.Append. Not unique, deliberately: a
		// deployment upgrading from a version that appended unconditionally
		// already holds duplicates, and a unique index would refuse to start over
		// its own history rather than stop making more.
		{Name: "org_tx", Fields: []string{store.Org, fieldTxID}},
		{Name: "org_user_at", Fields: []string{store.Org, fieldUser, fieldAt}},
		{Name: "org_account_at", Fields: []string{store.Org, fieldAccount, fieldAt}},
		{Name: "org_counterparty_at", Fields: []string{store.Org, fieldCounterparty, fieldAt}},
		{Name: "org_device_at", Fields: []string{store.Org, fieldDevice, fieldAt}},
		{Name: "org_address_at", Fields: []string{store.Org, fieldAddress, fieldAt}},
	},
}

// Ensure creates the transaction collection if it does not exist.
//
// This is the seam every aggregate rule stood on, and it was missing. Without the
// collection, a window query fails, and a rule that cannot read history reaches no
// verdict — so twelve of twenty rules faulted on every transaction and the engine
// could report nothing about velocity, structuring, dormancy or deviation. The
// collection is declared here rather than in a migration file so that the schema
// the reader queries and the schema that exists are one declaration: a migration
// that drifts from these field names produces an empty window, and an empty window
// reads as a customer with no history rather than as a broken deployment.
//
// It is idempotent, so it runs on every start and does nothing on an existing
// install.
func Ensure(app core.App) error { return events.Ensure(app) }
