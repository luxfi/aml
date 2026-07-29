// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package history

import (
	"fmt"

	"github.com/hanzoai/base/core"
)

// Ensure creates the transaction collection if it does not exist.
//
// This is the seam every aggregate rule stood on, and it was missing. Without the
// collection, a window query fails, and a rule that cannot read history reaches no
// verdict — so twelve of twenty rules faulted on every transaction and the engine
// could report nothing about velocity, structuring, dormancy or deviation. It is
// created here rather than in a migration file so that the schema the reader
// queries and the schema that exists are written in one place: a migration that
// drifts from these field names produces an empty window, and an empty window
// reads as a customer with no history rather than as a broken deployment.
//
// It is idempotent, so it runs on every start and does nothing on an existing
// install.
func Ensure(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(Collection); err == nil {
		return nil
	}

	c := core.NewBaseCollection(Collection)

	// Every field a window reads. Nothing here is Required except the identifiers a
	// window is keyed by: a transaction with no device is ordinary, but one with no
	// organisation cannot be scoped to a tenant and one with no timestamp cannot be
	// placed in a window.
	c.Fields.Add(
		&core.TextField{Name: fieldOrg, Required: true},
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
	)

	// Windows are always read as (org, subject, time range), so the indexes follow
	// the query rather than the fields. Without them every velocity rule is a table
	// scan per rule per transaction, which is the cost model this design exists to
	// avoid.
	c.Indexes = []string{
		idx("org_at", fieldOrg, fieldAt),
		idx("org_user_at", fieldOrg, fieldUser, fieldAt),
		idx("org_account_at", fieldOrg, fieldAccount, fieldAt),
		idx("org_counterparty_at", fieldOrg, fieldCounterparty, fieldAt),
		idx("org_device_at", fieldOrg, fieldDevice, fieldAt),
		idx("org_address_at", fieldOrg, fieldAddress, fieldAt),
	}

	if err := app.Save(c); err != nil {
		return fmt.Errorf("history: creating collection %s: %w", Collection, err)
	}
	return nil
}

// idx builds a CREATE INDEX statement over the collection's own table.
func idx(name string, fields ...string) string {
	cols := ""
	for i, f := range fields {
		if i > 0 {
			cols += ", "
		}
		cols += "`" + f + "`"
	}
	return fmt.Sprintf("CREATE INDEX `idx_%s_%s` ON `%s` (%s)", Collection, name, Collection, cols)
}
