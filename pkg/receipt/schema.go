// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package receipt

import (
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/store"
)

// Field names, named once so the writer and the reader cannot disagree. A
// disagreement here does not fail: it finds no prior receipt, which reads as a
// transaction nobody has offered before, which is exactly the double count this
// plane exists to prevent.
const (
	fieldRef    = "ref"
	fieldMark   = "mark"
	fieldAnswer = "answer"
	fieldAt     = "at"
)

// receipts is where answers are kept against the offers that earned them.
var receipts = store.Kind{
	Name: "aml_receipts",
	Fields: []core.Field{
		&core.TextField{Name: fieldRef, Required: true},
		&core.TextField{Name: fieldMark, Required: true},
		&core.TextField{Name: fieldAnswer},
		&core.DateField{Name: fieldAt, Required: true},
	},
	Indexes: []store.Index{
		// One answer per transaction per tenant, and the database is what makes
		// it so: two concurrent offers of one transaction both read no prior
		// receipt before either writes, so the guarantee cannot live in the read.
		{Name: "ref", Fields: []string{store.Org, fieldRef}, Unique: true},
		// Disposal asks which receipts have outlived every window they could
		// still affect, in every org.
		{Name: "at", Fields: []string{fieldAt}},
	},
}

// Ensure creates the collection and is idempotent, so it runs on every start.
func Ensure(app core.App) error { return receipts.Ensure(app) }
