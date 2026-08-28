// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import (
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/store"
)

// Field names. They are named once here because the writer and the reader have to
// agree: a disagreement between them does not fail, it returns nothing, and a
// ledger that returns nothing looks exactly like a customer with no history.
const (
	fieldClass    = "class"
	fieldTrigger  = "trigger"
	fieldParties  = "parties"
	fieldRef      = "ref"
	fieldNature   = "nature"
	fieldReason   = "reason"
	fieldInside   = "relationship"
	fieldOccurred = "occurred"
	fieldEnded    = "ended"
	fieldStart    = "start"
	fieldExpiry   = "expiry"
	fieldWritten  = "written"
	fieldBody     = "body"
	fieldMark     = "fingerprint"
	fieldAssess   = "assessment"
	fieldExtended = "extended"
	fieldIdentity = "identity"

	// On the party index only: which party, and which record names it.
	fieldParty  = "party"
	fieldRecord = "record"
)

// records is where retained records live.
//
// Expiry is stored although [Record.Expiry] computes it, because disposal has to
// find what has expired without reading every record to ask. It is derived in one
// place — the writer, from that one function — so the column cannot say something
// the arithmetic does not.
var records = store.Kind{
	Name: "aml_retention",
	Fields: []core.Field{
		&core.TextField{Name: fieldClass, Required: true},
		&core.TextField{Name: fieldTrigger, Required: true},
		&core.JSONField{Name: fieldParties, Required: true},
		&core.TextField{Name: fieldRef},
		&core.TextField{Name: fieldNature},
		&core.TextField{Name: fieldReason},
		&core.TextField{Name: fieldInside},
		&core.DateField{Name: fieldOccurred, Required: true},
		&core.DateField{Name: fieldEnded},
		&core.DateField{Name: fieldStart},
		&core.DateField{Name: fieldExpiry},
		&core.DateField{Name: fieldWritten, Required: true},
		&core.TextField{Name: fieldBody},
		// What the body says, named by the caller that sealed it. It is a column
		// because it is a fact about the record: without it a sealed body read back
		// carries no name, the digest compares a body against a name, and every
		// retry of one transaction is a permanent conflict. See Record.Fingerprint.
		&core.TextField{Name: fieldMark},
		&core.JSONField{Name: fieldAssess},
		&core.JSONField{Name: fieldExtended},
		&core.TextField{Name: fieldIdentity, Required: true},
	},
	Indexes: []store.Index{
		// A client retry must not retain the same fact twice. The identity is
		// unique per org and the database is what makes it so, because two
		// concurrent retries both read no prior record before either writes.
		{Name: "identity", Fields: []string{store.Org, fieldIdentity}, Unique: true},
		// Closing a relationship cascades the clock to everything retained inside
		// it, so that set has to be reachable without a scan.
		{Name: "inside", Fields: []string{store.Org, fieldInside}},
		// The ordered walk a file is produced from, oldest event first.
		{Name: "class", Fields: []string{store.Org, fieldClass, fieldOccurred}},
		// Disposal asks which records have expired, in every org.
		{Name: "expiry", Fields: []string{fieldExpiry}},
	},
}

// parties indexes records by the parties they name. It is a collection of its own
// because a party is one of several on a record, and a value that repeats cannot
// be a column that is indexed.
//
// AMLR Art. 78 has to be answered "fully and speedily", which is this index: a
// lookback reads one party's records and its cost does not grow with the ledger.
var parties = store.Kind{
	Name: "aml_retention_parties",
	Fields: []core.Field{
		&core.TextField{Name: fieldParty, Required: true},
		&core.TextField{Name: fieldRecord, Required: true},
	},
	Indexes: []store.Index{
		// The lookback: which records name this party.
		{Name: "party", Fields: []string{store.Org, fieldParty}, Unique: false},
		// Disposal: which index entries reference this record, so they go with it.
		// Unique because one record names a party once.
		{Name: "record", Fields: []string{store.Org, fieldRecord, fieldParty}, Unique: true},
	},
}

// Ensure creates what the ledger stores in, and is idempotent so it runs on every
// start. A retention ledger that does not survive a restart breaches the
// record-keeping obligation it exists to discharge (AMLR Art. 77), so this is the
// call that has to have happened before the first record is retained.
func Ensure(app core.App) error {
	if err := records.Ensure(app); err != nil {
		return err
	}
	return parties.Ensure(app)
}
