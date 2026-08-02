// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package lists holds a tenant's own allow and deny lists, and answers whether a
// value is on one.
//
// A list is a control an institution states in its own terms — these addresses
// are ours, these are not to be served, this card range is under review — and it
// is referenced from a rule by name: `Listed("ip-deny", Tx.IPAddress)`. That is
// the whole of the language it adds, because a list either has a value or it does
// not, and every question that is more than that is a rule.
//
// Three properties are held up by the code rather than by discipline.
//
// A list that was never declared is an ERROR and not a miss. A rule naming a list
// nobody created would otherwise evaluate to "not listed" for every value on
// earth: the rule appears in the catalog, appears in the interface, is reported as
// coverage, and can never fire. That is the same failure the evaluator refuses at
// admission time for a missing provider, arrived at from the data side.
//
// Nothing here deletes. Removing a value stops it matching and records who
// removed it and why; the row stays, because a list entry is the reason a payment
// was refused and the disposal of evidence is the retention plane's decision and
// nobody else's (AMLR Art. 77). There is no eviction, no bound and no sweep in
// this package — grep it for Delete and the result is empty.
//
// A value is normalised by the list's CLASS before it is stored or matched, once,
// here. An address written 10.0.0.1 and ::ffff:10.0.0.1 are one address and a
// deny list that holds only one of them is a control with a documented bypass.
package lists

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// Kinds. A list decides one of two things, and which it is has to be declared
// rather than inferred from the name: `partners` allows on one deployment and
// denies on another, and a rule reading the wrong sense inverts a control.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Classes are the kinds of value a list holds. It is a closed set because the
// class decides normalisation, and a list of "anything" cannot be normalised —
// which means it matches by luck.
const (
	IP      = "ip"
	Email   = "email"
	Account = "account"
	Device  = "device"
	Country = "country"
	BIN     = "bin"
	ASN     = "asn"
)

// Kinds and Classes are the closed sets, exported so a console renders the
// choices from the engine rather than from a copy of them.
var (
	Kinds   = []string{Allow, Deny}
	Classes = []string{IP, Email, Account, Device, Country, BIN, ASN}
)

// Errors.
var (
	ErrNoList    = errors.New("lists: no such list")
	ErrExists    = errors.New("lists: a list of that name already exists")
	ErrName      = errors.New("lists: a list needs a name")
	ErrKind      = errors.New("lists: a list is allow or deny")
	ErrClass     = errors.New("lists: unknown class")
	ErrValue     = errors.New("lists: value cannot be read as this class")
	ErrEmpty     = errors.New("lists: no value to look up")
	ErrDecider   = errors.New("lists: no decider, so the entry records nobody's decision")
	ErrReason    = errors.New("lists: no reason, so the entry records no decision")
	ErrCrowded   = errors.New("lists: too many ranges on one list")
	ErrNoEntry   = errors.New("lists: no such entry")
	ErrStore     = errors.New("lists: store")
	ErrRetired   = errors.New("lists: entry is already removed")
	ErrMaxValues = errors.New("lists: too many values in one request")
)

// MaxRanges bounds how many address RANGES one list may hold.
//
// A range cannot be looked up by equality, so every one of them is tested on
// every match. The bound is what keeps a match constant-ish rather than growing
// with the list, and it is refused at write time rather than degrading silently
// at read time. Exact values are unbounded: they are one indexed lookup however
// many there are.
const MaxRanges = 4096

// MaxValues bounds one Add. A bulk import arrives in pages; a single request that
// writes a million rows inside one transaction is a lock nobody can wait out.
const MaxValues = 1000

// List is a declared list: what it decides, what class of value it holds, and who
// declared it.
type List struct {
	Org     string    `json:"org"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Class   string    `json:"class"`
	Note    string    `json:"note,omitempty"`
	By      string    `json:"by"`
	Added   int64     `json:"added"`
	Removed int64     `json:"removed"`
	Ranges  int64     `json:"ranges"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Entry is one value on a list, and the decision that put it there.
//
// Until is an operational validity window and NOT an expiry: past it the entry
// stops matching, and the row stays exactly where it is. Removed is the same
// thing reached by an operator instead of by the clock. Neither destroys
// anything, because the reason a transaction was refused outlives the control
// that refused it.
type Entry struct {
	Org       string    `json:"org"`
	List      string    `json:"list"`
	Value     string    `json:"value"`
	Range     bool      `json:"range,omitempty"`
	Reason    string    `json:"reason"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
	Until     time.Time `json:"until,omitzero"`
	Removed   time.Time `json:"removed,omitzero"`
	RemoveBy  string    `json:"remove_by,omitempty"`
	RemoveWhy string    `json:"remove_why,omitempty"`
}

// Live reports whether an entry matches at the given instant.
func (e Entry) Live(at time.Time) bool {
	if !e.Removed.IsZero() {
		return false
	}
	return e.Until.IsZero() || e.Until.After(at)
}

// Match is what a lookup found: whether the value is listed, and on which entry.
//
// The entry travels with the answer because a refusal has to be able to say what
// refused it. A rule that blocks a payment on a deny list and cannot produce the
// reason and the decider behind that list entry has made a decision nobody can
// explain to the customer.
type Match struct {
	Listed bool   `json:"listed"`
	Kind   string `json:"kind,omitempty"`
	Entry  *Entry `json:"entry,omitempty"`
}

// Value is one value being put on a list.
type Value struct {
	Value  string    `json:"value"`
	Reason string    `json:"reason"`
	Until  time.Time `json:"until,omitzero"`
}

// The typed operations. Each op is one In struct and one Out struct, so an HTTP
// face — this repo's Base router, or a zip typed op in the cloud mount — is a
// decode, a call, and an encode, with no second copy of the contract to drift.
//
// By is a [types.Decider] on each op that records a decision: it carries
// `json:"-"`, so it is not part of that contract at all, and the transport writes
// the authenticated subject onto it. A list entry is the reason a payment was
// refused, and who put it there is not something the caller gets to assert.
type (
	// DeclareIn declares a list. It is not an upsert: redeclaring an existing
	// name with a different class would silently reinterpret every value already
	// on it.
	DeclareIn struct {
		Name  string        `json:"name"`
		Kind  string        `json:"kind"`
		Class string        `json:"class"`
		Note  string        `json:"note,omitempty"`
		By    types.Decider `json:"-"`
	}

	// AddIn puts values on a list. Adding a value already there restates it: the
	// row keeps its identity and takes the new reason, decider and window, which
	// is also how a removed value is put back.
	AddIn struct {
		Name   string        `json:"name"`
		Values []Value       `json:"values"`
		By     types.Decider `json:"-"`
	}

	// RemoveIn takes a value off a list. The row stays; it stops matching.
	RemoveIn struct {
		Name   string        `json:"name"`
		Value  string        `json:"value"`
		Reason string        `json:"reason"`
		By     types.Decider `json:"-"`
	}

	// EntriesIn reads a list. Live restricts the answer to entries that match
	// right now; the default is everything, because the removed ones are the
	// audit.
	EntriesIn struct {
		Name  string `json:"name"`
		Live  bool   `json:"live,omitempty"`
		Limit int    `json:"limit,omitempty"`
	}

	// LookupIn asks whether one value is listed.
	LookupIn struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}

	// CatalogIn takes no argument: a tenant's catalog is its own.
	CatalogIn struct{}

	// Catalog is every list this tenant has declared.
	Catalog struct {
		Kinds   []string `json:"kinds"`
		Classes []string `json:"classes"`
		Lists   []List   `json:"lists"`
	}

	// Page is a list and the entries read from it.
	Page struct {
		List    List    `json:"list"`
		Entries []Entry `json:"entries"`
		// Cut is true when Limit stopped the read before the list ended, so an
		// empty tail is never mistaken for the end of the list.
		Cut bool `json:"cut,omitempty"`
	}
)

// DefaultLimit is how many entries a read returns when the caller names no bound.
const DefaultLimit = 500

// normalise puts a value into the one form its class is stored and matched in,
// and reports whether it is a range.
//
// Every class that has a canonical form gets one here, once. The alternative is a
// deny list holding "Alice@Example.com " that never matches "alice@example.com",
// which is a control that reports itself installed and is not.
func normalise(class, value string) (norm string, isRange bool, err error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", false, ErrEmpty
	}
	switch class {
	case IP:
		if strings.Contains(v, "/") {
			p, perr := netip.ParsePrefix(v)
			if perr != nil {
				return "", false, fmt.Errorf("%w: %q is not an address range: %v", ErrValue, value, perr)
			}
			// Masked, so 10.0.0.7/8 and 10.0.0.0/8 are one range rather than two
			// rows that test identically.
			return p.Masked().String(), true, nil
		}
		a, aerr := netip.ParseAddr(v)
		if aerr != nil {
			return "", false, fmt.Errorf("%w: %q is not an address: %v", ErrValue, value, aerr)
		}
		return a.Unmap().String(), false, nil
	case Email:
		v = strings.ToLower(v)
		if strings.Count(v, "@") != 1 || strings.HasPrefix(v, "@") || strings.HasSuffix(v, "@") {
			return "", false, fmt.Errorf("%w: %q is not an address", ErrValue, value)
		}
		return v, false, nil
	case Country:
		v = strings.ToUpper(v)
		if len(v) != 2 {
			return "", false, fmt.Errorf("%w: %q is not an ISO 3166-1 alpha-2 code", ErrValue, value)
		}
		return v, false, nil
	case BIN, ASN:
		// Digits only, so a BIN written with spaces or an ASN written AS15169 is
		// one value rather than three.
		v = strings.TrimPrefix(strings.ToUpper(v), "AS")
		v = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, v)
		if v == "" {
			return "", false, fmt.Errorf("%w: %q holds no digits", ErrValue, value)
		}
		return v, false, nil
	case Account, Device:
		// Identifiers an institution issues are case-sensitive and opaque, so
		// trimming is the only safe normalisation.
		return v, false, nil
	default:
		return "", false, fmt.Errorf("%w: %q", ErrClass, class)
	}
}

// contains reports whether a normalised address falls inside a normalised range.
func contains(rangeCIDR, addr string) bool {
	p, err := netip.ParsePrefix(rangeCIDR)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	return p.Contains(a.Unmap())
}

// Listed answers the rule vocabulary's question: is this value on this list.
//
// It satisfies engine.Lists, and its refusals are the point. An empty value is an
// error, because a transaction that carries no address must not quietly pass a
// rule that checks addresses against a deny list — that is silence reading as a
// clean result for precisely the transactions that withheld the evidence. A list
// that does not exist is an error for the same reason.
func (s *Shelf) Listed(ctx context.Context, org, name, value string) (bool, error) {
	m, err := s.Lookup(ctx, org, &LookupIn{Name: name, Value: value})
	if err != nil {
		return false, err
	}
	return m.Listed, nil
}
