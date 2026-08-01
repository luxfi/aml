// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package dictionary is the catalog of what a tenant's payloads actually carry:
// which fields exist, what shape they are, how often they are filled, and how
// much they vary.
//
// It answers the question a monitoring programme cannot answer from its rule
// catalog alone. A rule set can look complete and be reading a field nobody
// populates; a model can list nine features and be blind on four of them. Both
// read as "no findings", and the only way to tell that apart from a clean
// institution is to measure what arrived.
//
// # Three lenses on one catalog
//
// DECLARED fields are the transaction's own, discovered by reflection over
// types.Transaction rather than by a hand-kept list, so the catalog cannot
// describe a field the engine does not read or miss one it does.
//
// CUSTOM fields are the top-level keys of the tenant's own Raw payload. They are
// how an institution's own vocabulary appears in the catalog without anybody
// declaring it here.
//
// FEATURES are the model's inventory (pkg/anomaly), carried verbatim with their
// typology, indicator and citation. They are not measured here — the model's own
// State reports what it is blind on — and they are in the catalog because a
// reviewer reading "what does this system look at" needs one answer, not two.
//
// # No value is ever stored
//
// A distinct-value count is kept as a bitmap sketch and never as values or as
// hashes of values (see sketch.go). Identifiers belong in the retained record
// plane, sealed and purpose-gated; a statistics table does not get a second copy
// of them under a weaker regime.
//
// # Write-behind, and it says so
//
// Observations accumulate in memory and are written on Flush and on Close.
// A field statistic is not a compliance record — no decision is taken on it — so
// paying an indexed write per field per transaction to make it one would be
// spending the ingest path's latency on a diagnostic. What is NOT acceptable is
// silence about it, so the catalog reports Pending: how many observations are
// accumulated and not yet durable. A restart loses exactly that number and the
// answer says so, rather than reading as a quieter institution.
package dictionary

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
)

// Origins of a field in the catalog.
const (
	// Declared is a field of the transaction the engine itself reads.
	Declared = "declared"
	// Custom is a top-level key of the tenant's own Raw payload.
	Custom = "custom"
)

// Shapes a value can have. A closed set, because the shape decides which
// statistics mean anything: a mean over text is not a number anybody should read.
const (
	Text   = "text"
	Number = "number"
	Time   = "time"
	Bool   = "bool"
	Object = "object"
	List   = "list"
)

// Prefix under which a tenant's own payload keys appear, so a custom field named
// `amount` is never confused with the declared one.
const Prefix = "raw."

// ErrStore is what a catalog read or write returns when the store refuses.
var ErrStore = errors.New("dictionary: store")

// MaxKeys bounds how many top-level keys of one Raw payload are catalogued.
//
// A payload with thousands of distinct keys is not a payload with a vocabulary,
// it is a map used as a bag, and cataloguing it would grow one row per key per
// tenant without bound. The excess is counted and reported rather than absorbed.
const MaxKeys = 256

// Field is one entry in the catalog.
type Field struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Shape  string `json:"shape"`

	// Seen is how many payloads carried this field with a value. Fill is that
	// against the payloads examined, which is what says whether a rule reading it
	// can ever fire.
	Seen int64   `json:"seen"`
	Fill float64 `json:"fill"`
	// Blind is a declared field no payload has ever filled. It is stated rather
	// than left as a zero, because a coverage claim resting on a blind field is
	// the failure this catalog exists to surface.
	Blind bool `json:"blind,omitempty"`

	// Distinct is the estimated number of values the field has taken, and
	// Saturated says the estimate has stopped being a count — the field varies
	// more than the sketch can measure, which is itself the finding.
	Distinct  int64 `json:"distinct"`
	Saturated bool  `json:"saturated,omitempty"`

	// Numbers only. Absent — not zero — for a field that is not a number, because
	// a mean of 0.0 over an identifier reads as a fact.
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Mean      *float64 `json:"mean,omitempty"`
	Deviation *float64 `json:"deviation,omitempty"`

	First time.Time `json:"first,omitzero"`
	Last  time.Time `json:"last,omitzero"`
}

// The typed operations.
type (
	// CatalogIn takes no argument beyond the tenant: a catalog is of the tenant's
	// own payloads and there is no other one to ask for.
	CatalogIn struct{}

	// Catalog is what a tenant's payloads carry.
	Catalog struct {
		// Payloads is how many transactions the statistics were computed from.
		Payloads int64     `json:"payloads"`
		First    time.Time `json:"first,omitzero"`
		Last     time.Time `json:"last,omitzero"`
		// Pending is how many observations are accumulated in memory and not yet
		// written. It is published because a restart loses exactly this many, and
		// a statistic that quietly shrank is worse than one that says it might.
		Pending int64 `json:"pending"`
		// Skipped counts payload keys past MaxKeys that were not catalogued.
		Skipped int64 `json:"skipped"`

		Fields []Field `json:"fields"`
		// Features is the model's own inventory, carried whole. Its statistics are
		// the model's to report (GET /v1/aml/anomaly), not this catalog's, and
		// inventing them here would be a second account of the same thing.
		Features []anomaly.Feature `json:"features"`
	}
)

// declared is the transaction's own field list, derived from the struct once.
//
// Reflection over the json tags rather than a list written out here, because a
// list is a second declaration of the payload and the two drift the first time a
// field is added — and the drift is invisible: the catalog simply never mentions
// the new field, which reads as a field nobody sends.
var declared = fields(reflect.TypeOf(types.Transaction{}))

type spec struct {
	name  string
	shape string
	index int
}

func fields(t reflect.Type) []spec {
	out := make([]spec, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || tag == "" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		out = append(out, spec{name: name, shape: shapeOf(f.Type), index: i})
	}
	return out
}

func shapeOf(t reflect.Type) string {
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return Time
	case t == reflect.TypeOf(json.RawMessage{}):
		return Object
	}
	switch t.Kind() {
	case reflect.String:
		return Text
	case reflect.Bool:
		return Bool
	case reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Number
	case reflect.Slice, reflect.Array:
		return List
	default:
		return Object
	}
}

// Fields is the transaction fields the engine reads, in declaration order. It is
// exported so a console can render the vocabulary a rule may name without keeping
// its own copy of it.
func Fields() []string {
	out := make([]string, 0, len(declared))
	for _, s := range declared {
		out = append(out, s.name)
	}
	return out
}

// reading is one field's value in one payload, reduced to what the statistics
// need: whether it was filled, its text form for the distinct count, and its
// numeric value where it has one.
type reading struct {
	filled bool
	text   string
	num    float64
	isNum  bool
}

// read pulls one declared field out of a transaction.
//
// Emptiness is by value and not by presence, because JSON's zero and JSON's
// absence are the same thing on a Go struct and a fill rate that counted zeros as
// filled would report every field on every payload as populated.
func read(v reflect.Value, s spec) reading {
	f := v.Field(s.index)
	switch s.shape {
	case Text:
		str := f.String()
		return reading{filled: str != "", text: str}
	case Bool:
		// A bool is filled whichever way it points: false is a value, not an
		// absence, and a field that is false on every payload is a fact worth
		// seeing in the catalog.
		return reading{filled: true, text: boolText(f.Bool())}
	case Number:
		n := number(f)
		return reading{filled: n != 0, text: "", num: n, isNum: true}
	case Time:
		t, _ := f.Interface().(time.Time)
		return reading{filled: !t.IsZero(), text: t.UTC().Format(time.RFC3339)}
	default:
		return reading{filled: f.Len() > 0}
	}
}

func number(v reflect.Value) float64 {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint())
	default:
		return 0
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// custom reads the top-level keys of a Raw payload.
//
// The shape is read from the JSON itself rather than assumed, so a key sent as a
// number on one payload and as text on the next is catalogued as it arrived. The
// catalog reports the LAST shape seen; a field whose shape moves is a data
// quality finding the fill and distinct counts will already be showing.
func custom(raw json.RawMessage) (map[string]reading, int) {
	if len(raw) == 0 {
		return nil, 0
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		// A payload whose extension is not an object carries no keys to catalog.
		// It is not an error here: refusing the observation would make the
		// dictionary decide whether a transaction is acceptable, which is the
		// engine's job and not a statistic's.
		return nil, 0
	}
	out := make(map[string]reading, len(top))
	skipped := 0
	names := make([]string, 0, len(top))
	for k := range top {
		names = append(names, k)
	}
	// Sorted, so which keys are dropped past the bound is deterministic rather
	// than a property of map iteration order.
	sortStrings(names)
	for _, k := range names {
		if len(out) >= MaxKeys {
			skipped++
			continue
		}
		out[Prefix+k] = value(top[k])
	}
	return out, skipped
}

func value(raw json.RawMessage) reading {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return reading{}
	}
	switch s[0] {
	case '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return reading{}
		}
		return reading{filled: str != "", text: str}
	case '{':
		return reading{filled: true, text: ""}
	case '[':
		return reading{filled: true, text: ""}
	case 't', 'f':
		return reading{filled: true, text: s}
	default:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return reading{}
		}
		return reading{filled: true, num: n, isNum: true}
	}
}

// shapeText names the shape of a custom reading, from the value rather than from
// a declaration.
func shapeText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return Text
	}
	switch s[0] {
	case '"':
		return Text
	case '{':
		return Object
	case '[':
		return List
	case 't', 'f':
		return Bool
	case 'n':
		return Text
	default:
		return Number
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
