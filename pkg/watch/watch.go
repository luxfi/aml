// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package watch records every detection as it fires and reports what the
// monitoring programme is doing right now.
//
// An ACTIVATION is one rule firing on one subject at one instant. It is written
// before anything else happens to it, and it is never dropped: a detection that a
// suppression covers, or that repeats inside a fold window, is recorded WITH the
// reason it did not reach a queue. A monitoring system that discards what it
// decided not to show cannot answer how much it is not showing, and "no alerts"
// then means either a quiet institution or a silent control with no way to tell
// which.
//
// # The durable row is the record; the feed is a convenience
//
// Record writes the row first and only then offers it to live subscribers. A
// subscriber that cannot keep up is dropped from, and the drops are COUNTED and
// published, because a feed that silently skips is a monitor that reports calm.
// Nothing is ever served from the feed: every question this package answers is
// answered from the durable rows.
//
// # Response, and the two things that change it
//
// Action is what the rule asked for. Response is what this plane concluded, and
// exactly two declared mechanisms move it:
//
//   - A SUPPRESSION (pkg/suppress) covers the activation. The response drops to
//     allow, the row is marked, and the suppression's id is on it — so "how many
//     detections has this suppression silenced" is a query.
//   - A RUNG raises it. A rung is a tenant's declared policy: when this rule has
//     fired this many times on one subject within this window, the response is
//     this. A rung can only RAISE — lowering a response is a suppression by
//     another name, and a suppression needs a reason and a decider, which a rung
//     is not asked for on every activation it touches.
//
// Folding is the same mechanism with a different outcome: a rung whose To is Fold
// marks the repeat as a duplicate rather than raising it. Elevation beats
// folding, because an activation that reached an escalation rung is the
// escalation and not a repeat of one.
//
// Nothing here deletes and nothing evicts. The activation plane is the evidence
// that a control ran, and disposal is the retention plane's decision alone
// (AMLR Art. 77).
package watch

import (
	"errors"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// Causes an activation did not reach a queue. Both are ordinary and neither is a
// verdict that nothing happened.
const (
	// CauseSuppressed means a declared suppression covered it.
	CauseSuppressed = "suppression"
	// CauseDuplicate means a fold rung matched: this rule already fired on this
	// subject inside the window.
	CauseDuplicate = "duplicate"
)

// Fold is the rung outcome that marks a repeat as a duplicate rather than raising
// the response. It is deliberately not an action: folding does not change what
// the institution would do about the first one.
const Fold = "fold"

// Errors.
var (
	ErrRule    = errors.New("watch: an activation names no rule")
	ErrSubject = errors.New("watch: an activation names no subject, so there is nothing for it to be about")
	ErrKind    = errors.New("watch: unknown subject kind")
	ErrAction  = errors.New("watch: unknown action")
	ErrTo      = errors.New("watch: a rung's outcome is fold or an action")
	ErrCount   = errors.New("watch: a rung counts at least two activations, or it is not about repetition")
	ErrWithin  = errors.New("watch: a rung needs a window to count within")
	ErrReason  = errors.New("watch: no reason, so the rung records no decision")
	ErrDecider = errors.New("watch: no decider, so the rung records nobody's decision")
	ErrNotHere = errors.New("watch: no such rung")
	ErrRetired = errors.New("watch: already retired")
	ErrStore   = errors.New("watch: store")
)

// Subject is what an activation is about: one axis, one identifier.
//
// One subject and not a set, deliberately. A fold window and an escalation streak
// are both counts over "the same thing firing again", and a set makes "the same
// thing" ambiguous — two activations sharing an account but not a device would be
// both a repeat and not one. The caller names the axis the detection is about;
// the transaction id on the row is what reaches everything else the transaction
// touched.
type Subject struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Activation is one rule firing on one subject, and what became of it.
type Activation struct {
	ID  string    `json:"id"`
	Org string    `json:"org"`
	At  time.Time `json:"at"`

	Rule     string  `json:"rule"`
	RuleName string  `json:"rule_name,omitempty"`
	Typology string  `json:"typology,omitempty"`
	Severity string  `json:"severity,omitempty"`
	Score    float64 `json:"score,omitempty"`

	Tx      string  `json:"tx,omitempty"`
	Subject Subject `json:"subject"`

	// Action is what the rule asked for. Response is what this plane concluded.
	// Both are kept, because the difference is the whole governance question.
	Action   string `json:"action"`
	Response string `json:"response"`

	// Suppressed marks an activation that did not reach a queue, Cause says which
	// mechanism, and By names the suppression or the prior activation behind it.
	Suppressed bool   `json:"suppressed,omitempty"`
	Cause      string `json:"cause,omitempty"`
	By         string `json:"by,omitempty"`

	// Rung names the declared policy that raised the response, when one did.
	Rung string `json:"rung,omitempty"`
	// Unchecked marks an activation whose suppression check could not be answered
	// over every candidate, so it reached a queue that a suppression beyond the
	// bound might have kept it out of. It is a marking for the same reason
	// Suppressed is one: a plane that silently absorbed an incomplete governance
	// answer could not tell anybody how often it had. Querying for it is how an
	// institution finds every activation whose answer was partial.
	Unchecked bool `json:"unchecked,omitempty"`
	// Streak is how many activations of this rule on this subject fall inside the
	// widest window any rung declared for it, this one included, counted up to
	// what the deepest rung asks for. Zero when the tenant has declared no rung,
	// because the count is not computed then — an unread number is not worth an
	// indexed read per detection.
	Streak int `json:"streak,omitempty"`
}

// Rung is a tenant's declared response to repetition: when this rule has fired
// Count times on one subject of this kind within Within, do To.
type Rung struct {
	ID   string `json:"id"`
	Org  string `json:"org"`
	Rule string `json:"rule"`
	Kind string `json:"kind"`

	Count  int    `json:"count"`
	Within Span   `json:"within"`
	To     string `json:"to"`

	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`

	Retired   time.Time `json:"retired,omitzero"`
	RetiredBy string    `json:"retired_by,omitempty"`
	RetireWhy string    `json:"retire_why,omitempty"`
}

// InForce reports whether a rung applies at an instant.
func (r Rung) InForce(at time.Time) bool {
	return r.Retired.IsZero() || at.Before(r.Retired)
}

// Rate is one rule's activity over a window: what it did and what became of it.
type Rate struct {
	Rule     string    `json:"rule"`
	RuleName string    `json:"rule_name,omitempty"`
	Fired    int64     `json:"fired"`
	Live     int64     `json:"live"`
	Silenced int64     `json:"silenced"`
	Folded   int64     `json:"folded"`
	Elevated int64     `json:"elevated"`
	Blocked  int64     `json:"blocked"`
	Last     time.Time `json:"last"`
	// Subjects is how many distinct subjects this rule fired on. A rule firing a
	// thousand times on one account and a rule firing once on a thousand accounts
	// are different situations and the count alone does not distinguish them.
	Subjects int `json:"subjects"`
}

// The typed operations.
type (
	// RecordIn is one detection as it fires.
	RecordIn struct {
		Rule     string    `json:"rule"`
		RuleName string    `json:"rule_name,omitempty"`
		Typology string    `json:"typology,omitempty"`
		Severity string    `json:"severity,omitempty"`
		Action   string    `json:"action"`
		Score    float64   `json:"score,omitempty"`
		Tx       string    `json:"tx,omitempty"`
		Subject  Subject   `json:"subject"`
		At       time.Time `json:"at,omitzero"`
	}

	// FeedIn reads the activation plane forward from an instant. It is the poll a
	// monitor makes; the live channel is Subscribe.
	FeedIn struct {
		Since time.Time `json:"since,omitzero"`
		Rule  string    `json:"rule,omitempty"`
		// Live restricts the answer to activations that reached a queue.
		Live  bool `json:"live,omitempty"`
		Limit int  `json:"limit,omitempty"`
	}

	// Feed is a page of activations, oldest first, so a poller can carry the last
	// instant forward without missing one.
	Feed struct {
		Activations []Activation `json:"activations"`
		// Through is the instant a caller should ask from next. It is the last
		// activation's, or the request's Since when the page is empty.
		Through time.Time `json:"through"`
		Cut     bool      `json:"cut,omitempty"`
		// Dropped is how many live-feed deliveries this instance has abandoned
		// because a subscriber could not keep up. It travels on the poll because a
		// monitor reading the feed is exactly who needs to know the feed is lossy.
		Dropped int64 `json:"dropped"`
	}

	// RatesIn asks what the rules have been doing.
	RatesIn struct {
		Since time.Time `json:"since,omitzero"`
		Until time.Time `json:"until,omitzero"`
	}

	// Rates is the answer, one row per rule, busiest first.
	Rates struct {
		From  time.Time `json:"from"`
		To    time.Time `json:"to"`
		Rules []Rate    `json:"rules"`
		// Examined is how many activations the answer was computed from, and Cut
		// says the window held more than could be read. A rate computed from a
		// truncated window under-reports, so the truncation is published rather
		// than absorbed.
		Examined int  `json:"examined"`
		Cut      bool `json:"cut,omitempty"`
	}

	// DeclareIn declares a rung.
	//
	// By is a [types.Decider]: not on the wire, written from the credential.
	DeclareIn struct {
		Rule   string        `json:"rule"`
		Kind   string        `json:"kind"`
		Count  int           `json:"count"`
		Within Span          `json:"within"`
		To     string        `json:"to"`
		Reason string        `json:"reason"`
		By     types.Decider `json:"-"`
	}

	// RetireIn ends a rung. The row stays.
	RetireIn struct {
		ID     string        `json:"id"`
		Reason string        `json:"reason"`
		By     types.Decider `json:"-"`
	}

	// LadderIn reads a tenant's rungs.
	LadderIn struct {
		Rule    string `json:"rule,omitempty"`
		InForce bool   `json:"in_force,omitempty"`
	}

	// Ladder is every rung a tenant has declared, ordered by rule then by count so
	// it reads as the ladder it is.
	Ladder struct {
		Rungs []Rung `json:"rungs"`
	}
)

// DefaultLimit is how many activations a read returns when the caller names no
// bound, and MaxLimit is the most it will return however large a bound is asked
// for.
const (
	DefaultLimit = 200
	MaxLimit     = 5000
)

// MaxExamined bounds the rows one Rates call folds over.
//
// It is a real bound and not a page: a rate is a fold over a window and there is
// no correct partial answer, so reaching it is reported on the answer rather than
// hidden inside it.
const MaxExamined = 50_000

// MaxCount and MaxWithin bound what a rung may ask for.
//
// A rung's Count becomes the LIMIT of a read on the INGEST path: the streak query
// reads exactly as many prior activations as the deepest declared rung needs (see
// priorInWindow), which is a bounded read only if the declaration is bounded.
// Undeclared, a tenant asking for a count of ten million turns every activation
// of that rule into a ten-million-row scan of its own store, on the path that
// must answer before a payment can be taken. The window is bounded for the same
// reason from the other side: a span of a decade is a scan over the whole plane.
//
// Both are refused at declaration, where a refusal costs one operator request,
// and both are clamped again where they are USED, because a bound introduced
// after a row was written does not reach that row.
//
// The numbers are what a repetition policy plausibly means. A rung that fires on
// the thousandth repeat inside thirty days is already past the point where the
// answer is "this rule is wrong", not "escalate".
const (
	MaxCount  = 1000
	MaxWithin = Span(30 * 24 * time.Hour)
)

// actions are the responses a rung may name, alongside Fold.
var actions = []string{types.ActionAllow, types.ActionFlag, types.ActionReview, types.ActionReport, types.ActionBlock}
