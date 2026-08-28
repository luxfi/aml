// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import "time"

// shelf is where a ledger's records are kept.
//
// The [Ledger] holds the obligations — which clock starts when, how far a closure
// cascades, what a disposal has to prove before it reports success — and the shelf
// holds the records. So every obligation is stated once and holds whichever shelf
// is underneath, and a shelf cannot weaken one by implementing it differently.
//
// The contract:
//
//   - Every method is scoped to an org, except the two that answer to the operator
//     rather than to a tenant: expired and count. Destroying what has expired is
//     the operator's obligation wherever the record sits, and there is no list of
//     orgs to iterate.
//   - Methods promise their own single effect and nothing more. A sequence that
//     must be all-or-nothing — read a relationship then write its children — is
//     wrapped in tx by the ledger, and inside tx the shelf is one that commits
//     everything or nothing.
//   - read and retained report [ErrNotFound] for an absent record, so a caller can
//     tell "not there" from "could not look".
//   - insert reports [ErrConflict] when the identity is already taken. Two retries
//     that both find nothing retained are stopped here whatever else happens, so
//     one identity is one record even if the shelf is written to from two places.
//   - Reads return whole records, copied. There is no operation that returns part
//     of one, because the unit of disposal is the whole record and a shelf that
//     could hand out a piece could hand out a record with a piece missing.
type shelf interface {
	tx(fn func(shelf) error) error

	read(org, id string) (Record, error)
	retained(org, identity string) (Record, error)

	insert(r Record) error
	update(r Record) error
	erase(rs []Record) error

	inside(org, relationship string) ([]Record, error)
	party(org, party string) ([]Record, error)
	each(org string, c Class, visit func(Record) error) error
	expired(now time.Time, limit int) ([]Record, error)

	// orphans names the records the party index still points at although they are
	// not there, up to limit of them. It is how a disposal run proves it left no
	// way to find a destroyed record, and it crosses orgs for the same reason
	// disposal does.
	orphans(limit int) ([]string, error)

	count() (int, error)
}

// batch is how many records a disposal run destroys at a time. Disposal is the one
// operation whose size is the ledger's rather than one party's, so it is done in
// bounded pieces instead of assembling five years of records in memory.
const batch = 500
