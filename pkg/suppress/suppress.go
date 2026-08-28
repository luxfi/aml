// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package suppress holds the decisions an institution has taken to stop a
// detection reaching a queue, and answers whether one covers an activation.
//
// Suppression is the most dangerous control in a monitoring system, because its
// effect is silence and silence is what an unmonitored institution also looks
// like. Everything here is shaped by that.
//
// A suppression is a DECISION and carries one: a reason and a decider, both
// required, neither defaulted. Without them the row records that alerts stopped
// and nothing about why or on whose authority, which is the state a supervisor
// asking "who turned this off" cannot be answered from.
//
// A suppression that names neither a rule nor a subject is refused. That is not
// a suppression, it is a kill switch for the whole monitoring programme, and it
// would be indistinguishable in the store from an ordinary narrow one.
//
// Lifting does not delete. The row stays and gains the lift's own decider and
// reason, so the period a detection was silent is still readable after it is
// noisy again. Nothing in this package deletes anything: disposal is the
// retention plane's decision (AMLR Art. 77) and no operational knob gets to make
// it.
//
// And this package does not suppress anything. It answers whether a suppression
// covers an activation; the activation is still RECORDED, marked, by pkg/watch.
// A detection that is dropped rather than marked is one nobody can count, and a
// suppression whose volume nobody can count is not governed.
package suppress

import (
	"errors"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// Errors.
var (
	ErrReason  = errors.New("suppress: no reason, so the row records that alerts stopped and not why")
	ErrDecider = errors.New("suppress: no decider, so the row records nobody's decision")
	ErrBroad   = errors.New("suppress: a suppression naming neither a rule nor a subject is a kill switch, not a suppression")
	ErrKind    = errors.New("suppress: unknown subject kind")
	ErrSubject = errors.New("suppress: a subject value needs the kind it is a value of")
	ErrWindow  = errors.New("suppress: the window has already closed, so the suppression would never apply")
	ErrNotHere = errors.New("suppress: no such suppression")
	ErrLifted  = errors.New("suppress: already lifted")
	ErrCrowded = errors.New("suppress: too many suppressions in force on one rule")
	ErrStore   = errors.New("suppress: store")
)

// Suppression is one decision to keep a detection out of a queue.
//
// Rule, Kind and Value narrow what it covers, and an empty one means "any". At
// least one of Rule and Value must be named — see ErrBroad.
type Suppression struct {
	ID    string `json:"id"`
	Org   string `json:"org"`
	Rule  string `json:"rule,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Value string `json:"value,omitempty"`

	Reason string `json:"reason"`
	By     string `json:"by"`

	From  time.Time `json:"from"`
	Until time.Time `json:"until,omitzero"`

	Lifted   time.Time `json:"lifted,omitzero"`
	LiftedBy string    `json:"lifted_by,omitempty"`
	LiftWhy  string    `json:"lift_why,omitempty"`
}

// InForce reports whether a suppression applies at an instant. It is a pure
// function of the row and the clock, so the same question asked of a stored row
// and of a proposed one gets the same answer.
func (s Suppression) InForce(at time.Time) bool {
	if !s.Lifted.IsZero() && !at.Before(s.Lifted) {
		return false
	}
	if at.Before(s.From) {
		return false
	}
	return s.Until.IsZero() || s.Until.After(at)
}

// Covers reports whether this suppression covers an activation of a rule on a
// subject. It does not consider the clock; InForce does that, separately, so the
// two questions stay separable in a test.
func (s Suppression) Covers(rule, kind, value string) bool {
	if s.Rule != "" && s.Rule != rule {
		return false
	}
	if s.Kind != "" && s.Kind != kind {
		return false
	}
	if s.Value != "" && s.Value != value {
		return false
	}
	return true
}

// Reach is how narrow a suppression is: how many of the three coordinates it
// names. The narrowest match wins, so a blanket suppression on a rule never
// shadows a specific decision about one subject.
func (s Suppression) Reach() int {
	n := 0
	for _, v := range []string{s.Rule, s.Kind, s.Value} {
		if v != "" {
			n++
		}
	}
	return n
}

// The typed operations.
type (
	// SuppressIn declares a suppression.
	//
	// By is a [types.Decider]: it is not on the wire, and the transport writes the
	// authenticated subject onto it. Suppression is the control whose effect is
	// silence, so of everything here it is the one whose "on whose authority"
	// must not be a string the caller chose.
	SuppressIn struct {
		Rule   string        `json:"rule,omitempty"`
		Kind   string        `json:"kind,omitempty"`
		Value  string        `json:"value,omitempty"`
		Reason string        `json:"reason"`
		By     types.Decider `json:"-"`
		From   time.Time     `json:"from,omitzero"`
		Until  time.Time     `json:"until,omitzero"`
	}

	// LiftIn ends a suppression. It carries its own reason and decider: ending a
	// suppression is as much a decision as starting one, and an alert volume that
	// jumps needs an entry saying why.
	LiftIn struct {
		ID     string        `json:"id"`
		Reason string        `json:"reason"`
		By     types.Decider `json:"-"`
	}

	// LedgerIn reads a tenant's suppressions. InForce restricts the answer to the
	// ones applying right now; the default is all of them, because the lifted ones
	// are the record of what was silent and when.
	LedgerIn struct {
		Rule    string `json:"rule,omitempty"`
		InForce bool   `json:"in_force,omitempty"`
		Limit   int    `json:"limit,omitempty"`
	}

	// Ledger is the answer to LedgerIn.
	Ledger struct {
		Suppressions []Suppression `json:"suppressions"`
		Cut          bool          `json:"cut,omitempty"`
	}

	// CoverIn asks whether an activation is covered. At is the instant to judge,
	// zero meaning now.
	CoverIn struct {
		Rule  string    `json:"rule"`
		Kind  string    `json:"kind"`
		Value string    `json:"value"`
		At    time.Time `json:"at,omitzero"`
	}

	// Cover is the answer: covered or not, and by what.
	//
	// The suppression travels with the answer because pkg/watch writes its id onto
	// the activation it marks. That is what makes "how many detections has this
	// suppression silenced" a query rather than a guess, and it is why nothing
	// here keeps a counter of its own.
	Cover struct {
		Covered     bool         `json:"covered"`
		Suppression *Suppression `json:"suppression,omitempty"`
		// Partial says the candidates were more than one read may examine, so this
		// answer is over a bounded page of them and a narrower suppression may
		// exist beyond it. It is published rather than absorbed, and it is
		// published rather than raised as an error — see Cover.
		Partial bool `json:"partial,omitempty"`
	}
)

// DefaultLimit is how many suppressions a read returns when the caller names no
// bound.
const DefaultLimit = 500
