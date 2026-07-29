// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/store"
)

// durable keeps records in Base collections, so they are still there after a
// restart. That is not a nicety: the five-year period is the obligation, and a
// ledger that starts empty every time the process starts has kept nothing
// (AMLR Art. 77).
//
// The app is a field rather than a package handle because a transaction is a
// different app over the same data — see tx.
type durable struct{ app core.App }

func (d durable) tx(fn func(shelf) error) error {
	return d.app.RunInTransaction(func(txApp core.App) error {
		return fn(durable{app: txApp})
	})
}

func (d durable) read(org, id string) (Record, error) {
	rows, err := records.Find(d.app, org, "id = {:id}", "", 1, dbx.Params{"id": id})
	if err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return read(rows[0])
}

func (d durable) retained(org, identity string) (Record, error) {
	rows, err := records.Find(d.app, org, fieldIdentity+" = {:identity}", "", 1, dbx.Params{"identity": identity})
	if err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return Record{}, fmt.Errorf("%w: identity %s", ErrNotFound, identity)
	}
	return read(rows[0])
}

// insert writes the record and the party index entries that find it. Both halves
// belong to one write, so the ledger calls this inside tx: a record indexed under
// no party cannot be found by a lookback, and a lookback that cannot find it
// answers Art. 78 with "no".
func (d durable) insert(r Record) error {
	row, err := records.New(d.app, r.Org)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Id = r.ID
	write(row, r)
	if err := d.app.Save(row); err != nil {
		return fmt.Errorf("%w: retaining %s: %w", ErrStore, r.ID, err)
	}
	for _, p := range r.Parties {
		index, err := parties.New(d.app, r.Org)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
		index.Set(fieldParty, p)
		index.Set(fieldRecord, r.ID)
		if err := d.app.Save(index); err != nil {
			return fmt.Errorf("%w: indexing %s: %w", ErrStore, r.ID, err)
		}
	}
	return nil
}

func (d durable) update(r Record) error {
	rows, err := records.Find(d.app, r.Org, "id = {:id}", "", 1, dbx.Params{"id": r.ID})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, r.ID)
	}
	write(rows[0], r)
	if err := d.app.Save(rows[0]); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	return nil
}

// erase destroys whole records and every index entry that referenced them. The
// index entries go first: an entry left pointing at a destroyed record is how a
// disposed record keeps being counted as evidence of a relationship.
func (d durable) erase(rs []Record) error {
	for _, r := range rs {
		index, err := parties.Find(d.app, r.Org, fieldRecord+" = {:record}", "", 0, dbx.Params{"record": r.ID})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
		for _, entry := range index {
			if err := d.app.Delete(entry); err != nil {
				return fmt.Errorf("%w: unindexing %s: %w", ErrStore, r.ID, err)
			}
		}
		rows, err := records.Find(d.app, r.Org, "id = {:id}", "", 1, dbx.Params{"id": r.ID})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
		for _, row := range rows {
			if err := d.app.Delete(row); err != nil {
				return fmt.Errorf("%w: destroying %s: %w", ErrStore, r.ID, err)
			}
		}
	}
	return nil
}

func (d durable) inside(org, relationship string) ([]Record, error) {
	rows, err := records.Find(d.app, org, fieldInside+" = {:inside}", fieldOccurred+",id", 0, dbx.Params{"inside": relationship})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return all(rows)
}

// party returns the records naming a party, read through the party index. The work
// is proportional to that party's records and not to the ledger, which is what
// Art. 78's "fully and speedily" requires.
func (d durable) party(org, party string) ([]Record, error) {
	index, err := parties.Find(d.app, org, fieldParty+" = {:party}", "", 0, dbx.Params{"party": party})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := make([]Record, 0, len(index))
	for _, entry := range index {
		r, err := d.read(org, entry.GetString(fieldRecord))
		if err != nil {
			// An index entry pointing at a record that is not there. Repairing it is
			// not this read's business; disposal is what proves the indexes, and it
			// reports exactly this as a failure.
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// each visits an org's records of one class, oldest event first, a page at a time.
//
// It pages by the last record seen rather than by an offset. A five-year ledger is
// walked while a disposal run may be destroying from underneath it, and an offset
// page over a shrinking collection skips rows — a skipped record is one that was
// never produced to the authority that asked for it.
func (d durable) each(org string, c Class, visit func(Record) error) error {
	filter := fieldOccurred + " > {:at} || (" + fieldOccurred + " = {:at} && id > {:id})"
	if c != "" {
		filter = fieldClass + " = {:class} && (" + filter + ")"
	}

	at, id := time.Time{}, ""
	for {
		params := dbx.Params{"at": at, "id": id, "class": string(c)}
		rows, err := records.Find(d.app, org, filter, fieldOccurred+",id", batch, params)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			r, err := read(row)
			if err != nil {
				return err
			}
			if err := visit(r); err != nil {
				return err
			}
		}
		last := rows[len(rows)-1]
		if last.Id == id {
			// The page did not move, so the next one would be this one again. A walk
			// that cannot advance says so rather than never returning.
			return fmt.Errorf("%w: a walk of %s stopped advancing at %s", ErrStore, org, id)
		}
		at, id = last.GetDateTime(fieldOccurred).Time(), last.Id
	}
}

// expired answers which records' periods have run out, in every org, from the
// expiry index. An empty expiry is a clock that has not started — an open
// relationship and everything retained inside it — and has not run out.
func (d durable) expired(now time.Time, limit int) ([]Record, error) {
	filter := fieldExpiry + " != '' && " + fieldExpiry + " <= {:now}"
	rows, err := records.Across(d.app, filter, "id", limit, dbx.Params{"now": now.UTC()})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return all(rows)
}

// orphans walks the party index a page at a time and asks, once per page, which of
// the records it names are there. Two queries per page rather than one per entry,
// and no page of it is bigger than a disposal batch, so proving the index holds
// costs the same shape of work whether the ledger has a thousand records or five
// years of them.
func (d durable) orphans(limit int) ([]string, error) {
	var out []string
	after := ""
	for {
		index, err := parties.Across(d.app, "id > {:after}", "id", batch, dbx.Params{"after": after})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStore, err)
		}
		if len(index) == 0 {
			return out, nil
		}

		named := make([]string, 0, len(index))
		for _, entry := range index {
			named = append(named, entry.GetString(fieldRecord))
		}
		filter, params := oneOf("id", named)
		rows, err := records.Across(d.app, filter, "", 0, params)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStore, err)
		}
		there := make(map[string]bool, len(rows))
		for _, row := range rows {
			there[row.Id] = true
		}

		for _, id := range named {
			if !there[id] {
				out = append(out, id)
				if limit > 0 && len(out) >= limit {
					return out, nil
				}
			}
		}
		after = index[len(index)-1].Id
	}
}

// oneOf asks for any of a set of values in one query. The values are bound, not
// written into the filter: a value that reaches the filter as text can change the
// shape of the query, and the direction that matters here is the one where it
// matches nothing and a corrupt index reads as a sound one.
func oneOf(field string, values []string) (string, dbx.Params) {
	filter := ""
	params := dbx.Params{}
	for i, v := range values {
		name := fmt.Sprintf("%s%d", field, i)
		if i > 0 {
			filter += " || "
		}
		filter += field + " = {:" + name + "}"
		params[name] = v
	}
	return filter, params
}

func (d durable) count() (int, error) {
	n, err := records.Count(d.app)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return n, nil
}

// write puts a record into a row. Every field the reader reads is set here, from
// the constants both of them name, so the two cannot drift apart.
//
// The expiry column is derived from [Record.Expiry] here and nowhere else. It
// exists so disposal can ask which records have expired without reading all of
// them to find out, and deriving it in one place is what keeps the column from
// saying something the arithmetic does not.
func write(row *core.Record, r Record) {
	row.Set(fieldClass, string(r.Class))
	row.Set(fieldTrigger, string(r.Trigger))
	row.Set(fieldParties, r.Parties)
	row.Set(fieldRef, r.Ref)
	row.Set(fieldNature, r.Nature)
	row.Set(fieldReason, r.Reason)
	row.Set(fieldInside, r.Relationship)
	row.Set(fieldOccurred, r.Occurred.UTC())
	row.Set(fieldEnded, r.Ended.UTC())
	row.Set(fieldStart, r.Start.UTC())
	row.Set(fieldExpiry, r.Expiry().UTC())
	row.Set(fieldWritten, r.Written.UTC())
	row.Set(fieldBody, base64.StdEncoding.EncodeToString(r.Body))
	row.Set(fieldAssess, r.Assessment)
	row.Set(fieldExtended, r.Extended)
	row.Set(fieldIdentity, r.identity)
}

// read takes a record out of a row. A field that cannot be decoded is an error and
// never a zero value: a body that silently read as empty is a transaction that can
// no longer be reconstructed, and a record that cannot be reconstructed in full is
// one this package would rather refuse to hand over.
func read(row *core.Record) (Record, error) {
	r := Record{
		ID:           row.Id,
		Org:          row.GetString(store.Org),
		Class:        Class(row.GetString(fieldClass)),
		Trigger:      Trigger(row.GetString(fieldTrigger)),
		Ref:          row.GetString(fieldRef),
		Nature:       row.GetString(fieldNature),
		Reason:       row.GetString(fieldReason),
		Relationship: row.GetString(fieldInside),
		Occurred:     row.GetDateTime(fieldOccurred).Time(),
		Ended:        row.GetDateTime(fieldEnded).Time(),
		Start:        row.GetDateTime(fieldStart).Time(),
		Written:      row.GetDateTime(fieldWritten).Time(),
		identity:     row.GetString(fieldIdentity),
	}
	if err := row.UnmarshalJSONField(fieldParties, &r.Parties); err != nil {
		return Record{}, fmt.Errorf("%w: %s parties: %w", ErrStore, row.Id, err)
	}
	if err := row.UnmarshalJSONField(fieldAssess, &r.Assessment); err != nil {
		return Record{}, fmt.Errorf("%w: %s assessment: %w", ErrStore, row.Id, err)
	}
	if err := row.UnmarshalJSONField(fieldExtended, &r.Extended); err != nil {
		return Record{}, fmt.Errorf("%w: %s extension: %w", ErrStore, row.Id, err)
	}
	if encoded := row.GetString(fieldBody); encoded != "" {
		body, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return Record{}, fmt.Errorf("%w: %s body: %w", ErrStore, row.Id, err)
		}
		r.Body = body
	}
	return r, nil
}

func all(rows []*core.Record) ([]Record, error) {
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		r, err := read(row)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
