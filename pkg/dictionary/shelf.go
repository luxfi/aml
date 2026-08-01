// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package dictionary

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/store"
	"github.com/luxfi/aml/pkg/types"
)

const (
	fieldName   = "name"
	fieldOrigin = "origin"
	fieldShape  = "shape"
	fieldSeen   = "seen"
	fieldBits   = "bits"
	fieldCount  = "num_count"
	fieldSum    = "num_sum"
	fieldSquare = "num_square"
	fieldMin    = "num_min"
	fieldMax    = "num_max"
	fieldFirst  = "first"
	fieldLast   = "last"

	fieldPayloads = "payloads"
	fieldSkipped  = "skipped"
)

var fieldKind = store.Kind{
	Name: "aml_fields",
	Fields: []core.Field{
		&core.TextField{Name: fieldName, Required: true},
		&core.TextField{Name: fieldOrigin, Required: true},
		&core.TextField{Name: fieldShape, Required: true},
		&core.NumberField{Name: fieldSeen},
		&core.TextField{Name: fieldBits},
		&core.NumberField{Name: fieldCount},
		&core.NumberField{Name: fieldSum},
		&core.NumberField{Name: fieldSquare},
		&core.NumberField{Name: fieldMin},
		&core.NumberField{Name: fieldMax},
		&core.DateField{Name: fieldFirst},
		&core.DateField{Name: fieldLast},
	},
	Indexes: []store.Index{
		{Name: "name", Fields: []string{store.Org, fieldName}, Unique: true},
	},
}

var censusKind = store.Kind{
	Name: "aml_payloads",
	Fields: []core.Field{
		&core.NumberField{Name: fieldPayloads},
		&core.NumberField{Name: fieldSkipped},
		&core.DateField{Name: fieldFirst},
		&core.DateField{Name: fieldLast},
	},
	Indexes: []store.Index{
		// One row per tenant. Unique, so a concurrent first flush cannot leave two
		// counts that each look like the whole.
		{Name: "org", Fields: []string{store.Org}, Unique: true},
	},
}

// Ensure creates what the catalog is kept in. Idempotent, so it runs on every
// start.
func Ensure(app core.App) error {
	if err := fieldKind.Ensure(app); err != nil {
		return err
	}
	return censusKind.Ensure(app)
}

// stat is one field's accumulated observations, in memory between flushes.
type stat struct {
	origin string
	shape  string
	seen   int64
	bits   sketch
	count  int64
	sum    float64
	square float64
	min    float64
	max    float64
	first  time.Time
	last   time.Time
}

func (s *stat) observe(org, name string, r reading, at time.Time) {
	if !r.filled {
		return
	}
	s.seen++
	if r.text != "" {
		s.bits.add(org, name, r.text)
	}
	if r.isNum {
		s.bits.add(org, name, strconv.FormatFloat(r.num, 'g', -1, 64))
		s.count++
		s.sum += r.num
		s.square += r.num * r.num
		if s.count == 1 || r.num < s.min {
			s.min = r.num
		}
		if s.count == 1 || r.num > s.max {
			s.max = r.num
		}
	}
	if s.first.IsZero() || at.Before(s.first) {
		s.first = at
	}
	if at.After(s.last) {
		s.last = at
	}
}

type census struct {
	fields   map[string]*stat
	payloads int64
	skipped  int64
	first    time.Time
	last     time.Time
}

// Shelf is the durable catalog, with the accumulator in front of it.
type Shelf struct {
	app core.App
	// Now supplies the instant an observation is stamped with. Tests set it.
	Now func() time.Time

	mu      sync.Mutex
	pending map[string]*census
	// count is how many observations are accumulated and not yet written, across
	// every tenant. It is published on every catalog so a reader can see how much
	// of the answer a restart would lose.
	count int64
}

// NewBase returns the durable catalog. Ensure has to have run first.
func NewBase(app core.App) *Shelf {
	return &Shelf{app: app, pending: map[string]*census{}}
}

func (s *Shelf) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Observe accumulates one payload.
//
// It writes nothing: the observation joins this tenant's accumulator and reaches
// the store on the next Flush. That keeps a per-field indexed write off the
// ingest path, and the cost of it — what a restart would lose — is published as
// Pending rather than absorbed.
func (s *Shelf) Observe(org string, tx types.Transaction) error {
	if err := brand.Tenant(org); err != nil {
		return err
	}
	at := tx.Timestamp.UTC()
	if at.IsZero() {
		at = s.now()
	}
	v := reflect.ValueOf(tx)
	extra, skipped := custom(tx.Raw)

	var top map[string]json.RawMessage
	if len(tx.Raw) > 0 {
		_ = json.Unmarshal(tx.Raw, &top)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.pending[org]
	if c == nil {
		c = &census{fields: map[string]*stat{}}
		s.pending[org] = c
	}
	c.payloads++
	c.skipped += int64(skipped)
	if c.first.IsZero() || at.Before(c.first) {
		c.first = at
	}
	if at.After(c.last) {
		c.last = at
	}
	for _, spec := range declared {
		st := c.field(spec.name, Declared, spec.shape)
		st.observe(org, spec.name, read(v, spec), at)
	}
	for name, r := range extra {
		st := c.field(name, Custom, shapeText(top[name[len(Prefix):]]))
		st.shape = shapeText(top[name[len(Prefix):]])
		st.observe(org, name, r, at)
	}
	s.count++
	return nil
}

func (c *census) field(name, origin, shape string) *stat {
	st := c.fields[name]
	if st == nil {
		st = &stat{origin: origin, shape: shape}
		c.fields[name] = st
	}
	return st
}

// Pending is how many observations are accumulated and not yet durable.
func (s *Shelf) Pending() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Flush writes every tenant's accumulated observations.
//
// One transaction per tenant: a half-written census is a fill rate computed over
// a payload count that does not match it, and a fill rate over the wrong
// denominator is a number that reads as a finding.
func (s *Shelf) Flush(ctx context.Context) error {
	s.mu.Lock()
	batch := s.pending
	s.pending = map[string]*census{}
	s.count = 0
	s.mu.Unlock()

	var failed error
	for org, c := range batch {
		if err := s.write(org, c); err != nil {
			// Put the tenant's observations back, so a store that was briefly
			// unavailable costs latency rather than the census.
			s.restore(org, c)
			failed = err
		}
	}
	return failed
}

// restore folds an unwritten census back into the accumulator.
func (s *Shelf) restore(org string, c *census) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.pending[org]
	if held == nil {
		s.pending[org] = c
		s.count += c.payloads
		return
	}
	merge(held, c)
	s.count += c.payloads
}

func merge(into, from *census) {
	into.payloads += from.payloads
	into.skipped += from.skipped
	if into.first.IsZero() || (!from.first.IsZero() && from.first.Before(into.first)) {
		into.first = from.first
	}
	if from.last.After(into.last) {
		into.last = from.last
	}
	for name, st := range from.fields {
		held := into.fields[name]
		if held == nil {
			into.fields[name] = st
			continue
		}
		held.seen += st.seen
		held.bits.merge(st.bits)
		if st.count > 0 {
			if held.count == 0 || st.min < held.min {
				held.min = st.min
			}
			if held.count == 0 || st.max > held.max {
				held.max = st.max
			}
			held.count += st.count
			held.sum += st.sum
			held.square += st.square
		}
		if held.first.IsZero() || (!st.first.IsZero() && st.first.Before(held.first)) {
			held.first = st.first
		}
		if st.last.After(held.last) {
			held.last = st.last
		}
	}
}

func (s *Shelf) write(org string, c *census) error {
	if err := brand.Tenant(org); err != nil {
		return err
	}
	return s.app.RunInTransaction(func(tx core.App) error {
		for name, st := range c.fields {
			rows, err := fieldKind.Find(tx, org, fieldName+" = {:name}", "", 1, dbx.Params{"name": name})
			if err != nil {
				return fmt.Errorf("%w: %w", ErrStore, err)
			}
			var row *core.Record
			held := &stat{}
			if len(rows) > 0 {
				row = rows[0]
				held = readStat(row)
			} else {
				row, err = fieldKind.New(tx, org)
				if err != nil {
					return fmt.Errorf("%w: %w", ErrStore, err)
				}
			}
			fold(held, st)
			held.origin, held.shape = st.origin, st.shape
			row.Set(fieldName, name)
			writeStat(row, held)
			if err := tx.Save(row); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrStore, name, err)
			}
		}

		rows, err := censusKind.Find(tx, org, "", "", 1, nil)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
		var row *core.Record
		var payloads, skipped int64
		var first, last time.Time
		if len(rows) > 0 {
			row = rows[0]
			payloads = int64(row.GetInt(fieldPayloads))
			skipped = int64(row.GetInt(fieldSkipped))
			first = row.GetDateTime(fieldFirst).Time()
			last = row.GetDateTime(fieldLast).Time()
		} else {
			row, err = censusKind.New(tx, org)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrStore, err)
			}
		}
		payloads += c.payloads
		skipped += c.skipped
		if first.IsZero() || (!c.first.IsZero() && c.first.Before(first)) {
			first = c.first
		}
		if c.last.After(last) {
			last = c.last
		}
		row.Set(fieldPayloads, payloads)
		row.Set(fieldSkipped, skipped)
		row.Set(fieldFirst, first.UTC())
		row.Set(fieldLast, last.UTC())
		if err := tx.Save(row); err != nil {
			return fmt.Errorf("%w: census: %w", ErrStore, err)
		}
		return nil
	})
}

// fold merges an accumulated stat into the stored one. Every accumulator here is
// a monoid — a sum, a max, a bitwise union — which is what makes a write-behind
// census correct across a restart rather than approximately correct.
func fold(into, from *stat) {
	into.seen += from.seen
	into.bits.merge(from.bits)
	if from.count > 0 {
		if into.count == 0 || from.min < into.min {
			into.min = from.min
		}
		if into.count == 0 || from.max > into.max {
			into.max = from.max
		}
		into.count += from.count
		into.sum += from.sum
		into.square += from.square
	}
	if into.first.IsZero() || (!from.first.IsZero() && from.first.Before(into.first)) {
		into.first = from.first
	}
	if from.last.After(into.last) {
		into.last = from.last
	}
}

// Catalog reports what a tenant's payloads carry.
//
// It reads the durable rows and folds this tenant's un-flushed accumulator on
// top, so the answer is current rather than as-of-the-last-flush — and Pending
// says how much of it a restart would take back.
func (s *Shelf) Catalog(ctx context.Context, org string, _ *CatalogIn) (*Catalog, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	rows, err := fieldKind.Find(s.app, org, "", fieldName, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	held := &census{fields: map[string]*stat{}}
	for _, row := range rows {
		st := readStat(row)
		st.origin = row.GetString(fieldOrigin)
		st.shape = row.GetString(fieldShape)
		held.fields[row.GetString(fieldName)] = st
	}
	crows, err := censusKind.Find(s.app, org, "", "", 1, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(crows) > 0 {
		held.payloads = int64(crows[0].GetInt(fieldPayloads))
		held.skipped = int64(crows[0].GetInt(fieldSkipped))
		held.first = crows[0].GetDateTime(fieldFirst).Time()
		held.last = crows[0].GetDateTime(fieldLast).Time()
	}

	s.mu.Lock()
	pending := int64(0)
	if c := s.pending[org]; c != nil {
		pending = c.payloads
		merge(held, copyCensus(c))
	}
	s.mu.Unlock()

	out := &Catalog{
		Payloads: held.payloads, First: held.first, Last: held.last,
		Pending: pending, Skipped: held.skipped,
		Fields:   make([]Field, 0, len(held.fields)+len(declared)),
		Features: inventory(),
	}
	// Every declared field appears whether or not it was ever filled: a field
	// missing from the catalog reads as a field nobody sends, and a field present
	// with Blind set reads as one nobody fills. Only the second is true.
	for _, spec := range declared {
		if held.fields[spec.name] == nil {
			held.fields[spec.name] = &stat{origin: Declared, shape: spec.shape}
		}
	}
	for name, st := range held.fields {
		out.Fields = append(out.Fields, project(name, st, held.payloads))
	}
	sort.Slice(out.Fields, func(i, j int) bool { return out.Fields[i].Name < out.Fields[j].Name })
	return out, nil
}

// copyCensus takes a snapshot of an accumulator under the lock, so folding it
// into an answer cannot race the next Observe.
func copyCensus(c *census) *census {
	out := &census{fields: make(map[string]*stat, len(c.fields)),
		payloads: c.payloads, skipped: c.skipped, first: c.first, last: c.last}
	for name, st := range c.fields {
		copied := *st
		out.fields[name] = &copied
	}
	return out
}

func project(name string, st *stat, payloads int64) Field {
	distinct, saturated := st.bits.estimate()
	f := Field{
		Name: name, Origin: st.origin, Shape: st.shape,
		Seen: st.seen, Distinct: distinct, Saturated: saturated,
		First: st.first, Last: st.last,
		Blind: st.seen == 0,
	}
	if payloads > 0 {
		f.Fill = float64(st.seen) / float64(payloads)
	}
	if st.count > 0 {
		mean := st.sum / float64(st.count)
		variance := st.square/float64(st.count) - mean*mean
		if variance < 0 {
			variance = 0
		}
		dev := math.Sqrt(variance)
		min, max := st.min, st.max
		f.Min, f.Max, f.Mean, f.Deviation = &min, &max, &mean, &dev
	}
	return f
}

// inventory is the model's feature list, carried whole.
func inventory() []anomaly.Feature {
	inv := anomaly.Inventory()
	return append([]anomaly.Feature(nil), inv[:]...)
}

func writeStat(row *core.Record, s *stat) {
	row.Set(fieldOrigin, s.origin)
	row.Set(fieldShape, s.shape)
	row.Set(fieldSeen, s.seen)
	row.Set(fieldBits, s.bits.encode())
	row.Set(fieldCount, s.count)
	row.Set(fieldSum, s.sum)
	row.Set(fieldSquare, s.square)
	row.Set(fieldMin, s.min)
	row.Set(fieldMax, s.max)
	row.Set(fieldFirst, s.first.UTC())
	row.Set(fieldLast, s.last.UTC())
}

func readStat(row *core.Record) *stat {
	return &stat{
		seen:   int64(row.GetInt(fieldSeen)),
		bits:   decode(row.GetString(fieldBits)),
		count:  int64(row.GetInt(fieldCount)),
		sum:    row.GetFloat(fieldSum),
		square: row.GetFloat(fieldSquare),
		min:    row.GetFloat(fieldMin),
		max:    row.GetFloat(fieldMax),
		first:  row.GetDateTime(fieldFirst).Time(),
		last:   row.GetDateTime(fieldLast).Time(),
	}
}
