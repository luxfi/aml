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
	fieldCrowded  = "crowded"
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
		&core.NumberField{Name: fieldCrowded},
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

// observe folds one payload's reading of one field into the accumulator.
//
// # A number is only measured where the number is ours
//
// The catalog's invariant is that no payload value is ever stored, and the
// distinct count honours it by keeping a bitmap sketch rather than values. The
// numeric moments do not: min and max ARE payload values, exactly, at any volume
// — and at a count of one, so are sum, square and the mean derived from them. A
// column holding them is a second copy of the payload, in a statistics table,
// unsealed and outside the purpose gate that governs the retained record.
//
// For a DECLARED field that is a measurement of the transaction model this engine
// defines — a notional, a converted amount, a score — and its range is the
// statistic a reviewer is asking for. For a CUSTOM field it is whatever the
// institution put in its own payload under its own key: a national identifier, a
// date of birth as a number, a card range, an internal customer number. Nothing
// here knows which, and a catalog that could not say what it was storing stored
// it anyway.
//
// So a custom field gets the sketch and nothing else — how often it is filled and
// how much it varies, which is the whole of what the catalog is for — and its
// min, max, mean and deviation are ABSENT rather than zero, because a zero reads
// as a fact. Declared fields keep their moments.
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
		if s.origin == Declared {
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
	// crowded counts readings of a custom key there was no room for. See
	// MaxCustom: the vocabulary is bounded per tenant, and a tenant that reaches
	// its bound degrades its own catalog and nobody else's.
	crowded int64
	// names is how many distinct CUSTOM names this census holds, maintained where
	// a name is added and nowhere else. Declared fields are a fixed set and are
	// never counted against the bound.
	names int
	first time.Time
	last  time.Time
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
	// vocab is the per-tenant vocabulary bound, at zero meaning MaxCustom. It is
	// UNEXPORTED and no constructor sets it, so a deployment cannot lower a
	// diagnostic's bound into something that hides an institution's payload
	// surface. It is here for the tests: proving what happens at the bound needs
	// to reach it, and a bound nobody has seen work is a bound nobody has.
	vocab int
}

func (s *Shelf) maxCustom() int {
	if s.vocab > 0 {
		return s.vocab
	}
	return MaxCustom
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
		c.field(spec.name, Declared, spec.shape, s.maxCustom()).observe(org, spec.name, read(v, spec), at)
	}
	for name, r := range extra {
		shape := shapeText(top[name[len(Prefix):]])
		// MaxCustom bounds the ACCUMULATOR here, which is what keeps a tenant's
		// vocabulary out of the memory every other tenant's ingest runs in. The
		// same bound is applied to the stored rows at write, because a bound over
		// what is in flight is not a bound over what is kept.
		st := c.field(name, Custom, shape, s.maxCustom())
		if st == nil {
			continue
		}
		st.shape = shape
		st.observe(org, name, r, at)
	}
	s.count++
	return nil
}

// field is this census's accumulator for one name, created on first sight.
//
// A new CUSTOM name is refused once the tenant's vocabulary is full, and nil is
// what a refusal looks like: the reading is counted as crowded and dropped. It is
// a refusal of a NAME and never of a payload — the payload is still counted, every
// name already in the vocabulary still measures it, and nothing anywhere returns
// an error. See MaxCustom.
func (c *census) field(name, origin, shape string, room int) *stat {
	st := c.fields[name]
	if st != nil {
		return st
	}
	if origin == Custom {
		if c.names >= room {
			c.crowded++
			return nil
		}
		c.names++
	}
	st = &stat{origin: origin, shape: shape}
	c.fields[name] = st
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

// restore folds an unwritten census back into the accumulator. The vocabulary
// bound is not re-applied here: these are names this tenant already had room for,
// and refusing them on the way back would lose measurements the accumulator was
// holding rather than bound anything.
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
	into.crowded += from.crowded
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
			if st.origin == Custom {
				into.names++
			}
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
		// Room in this tenant's vocabulary, read once and only if a new custom
		// name actually needs it. It is this tenant's own row count against this
		// tenant's own bound: another institution's vocabulary is neither read nor
		// counted, and cannot take room from this one.
		room := -1
		crowded := c.crowded
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
				if st.origin == Custom {
					if room < 0 {
						n, err := vocabulary(tx, org, s.maxCustom())
						if err != nil {
							return err
						}
						room = s.maxCustom() - n
					}
					if room <= 0 {
						// No room for another name. The readings are counted and the
						// name is dropped; the tenant's existing fields keep measuring
						// and no payload is refused. See MaxCustom.
						crowded += st.seen
						continue
					}
					room--
				}
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
		var payloads, skipped, held int64
		var first, last time.Time
		if len(rows) > 0 {
			row = rows[0]
			payloads = int64(row.GetInt(fieldPayloads))
			skipped = int64(row.GetInt(fieldSkipped))
			held = int64(row.GetInt(fieldCrowded))
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
		held += crowded
		if first.IsZero() || (!c.first.IsZero() && c.first.Before(first)) {
			first = c.first
		}
		if c.last.After(last) {
			last = c.last
		}
		row.Set(fieldPayloads, payloads)
		row.Set(fieldSkipped, skipped)
		row.Set(fieldCrowded, held)
		row.Set(fieldFirst, first.UTC())
		row.Set(fieldLast, last.UTC())
		if err := tx.Save(row); err != nil {
			return fmt.Errorf("%w: census: %w", ErrStore, err)
		}
		return nil
	})
}

// vocabulary is how many custom names this tenant's catalog already holds,
// counted no further than the bound: what a caller of this needs is whether there
// is room, and reading a whole overgrown catalog to answer that would be the cost
// the bound exists to avoid.
func vocabulary(app core.App, org string, bound int) (int, error) {
	rows, err := fieldKind.Find(app, org, fieldOrigin+" = {:origin}", "", bound, dbx.Params{"origin": Custom})
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return len(rows), nil
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
		if st.origin == Custom {
			held.names++
		}
	}
	crows, err := censusKind.Find(s.app, org, "", "", 1, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(crows) > 0 {
		held.payloads = int64(crows[0].GetInt(fieldPayloads))
		held.skipped = int64(crows[0].GetInt(fieldSkipped))
		held.crowded = int64(crows[0].GetInt(fieldCrowded))
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
		Pending: pending, Skipped: held.skipped, Crowded: held.crowded,
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
		payloads: c.payloads, skipped: c.skipped, crowded: c.crowded,
		names: c.names, first: c.first, last: c.last}
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
