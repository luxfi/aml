// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package retention keeps what the instruments require kept, for as long as they
// require it, and destroys it when they require that.
//
// # Three clocks
//
// A retention period runs five years from the end of the business relationship,
// from the occasional transaction, or from the date of refusal to enter a
// relationship or carry out a transaction (Regulation (EU) 2024/1624
// Art. 77(3)). The third trigger is the one implementations miss, so [Refusal]
// is a first-class record here and a refused transaction is retained exactly
// like a completed one.
//
// A record retained inside a relationship cannot know its own expiry when it is
// written, because the clock starts at the end of the relationship. So
// [Ledger.Close] starts the clock on the relationship and on everything retained
// inside it, and until then those records have no expiry at all.
//
// # Deletion on expiry against no redaction
//
// Two requirements pull against each other. Records must not be redacted
// (AMLR Art. 77(1) final subparagraph); personal data must be deleted when the
// retention period expires, subject to a case-by-case extension of at most five
// further years (Directive (EU) 2015/849 Art. 40(1) second subparagraph).
//
// They are reconciled by the unit of disposal: the whole record, never a field.
// A retained record is append-only for its whole life — this package exposes no
// operation that removes, masks or rewrites any part of one, so redaction is not
// a policy that could be broken but an operation that does not exist. Reads hand
// out deep copies for the same reason: a reader cannot reach back into the
// ledger and unmake part of a record. At expiry the record is destroyed in full,
// with every index entry that referenced it, and [Ledger.Dispose] proves that
// before it reports success. There is no state in which a record exists
// partially.
//
// Sealing is not redaction. A sealed body (see pkg/token) holds the whole record
// and is reversible under the org's key, so the transaction can still be
// reconstructed (MLR 2017 reg. 40(2)(b)). Nothing is dropped, so nothing is
// redacted.
//
// # Purpose
//
// Retained personal data may be processed only to prevent money laundering and
// terrorist financing; processing for commercial purposes is prohibited
// (AMLD4 Art. 41(2)). Every read therefore takes a [Purpose] from a closed set,
// and a purpose outside that set is refused rather than logged.
package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// The retention arithmetic, in years, each from its own instrument.
const (
	// Period is the retention period. AMLR Art. 77(3).
	Period = 5
	// Extension is the cap on a case-by-case extension of it.
	// AMLD4 Art. 40(1) second subparagraph.
	Extension = 5
	// Ceiling is how long records of a transaction occurring during a business
	// relationship need be kept, counted from the transaction. MLR 2017
	// reg. 40(4).
	Ceiling = 10
)

// skew is how far ahead of the caller's clock a supplied "now" may be before it
// is refused. Disposal is destruction: it does not run on a future date.
const skew = time.Minute

// Errors. Each is a refusal, and none of them is recoverable by proceeding.
var (
	ErrOrg          = errors.New("retention: empty org")
	ErrClass        = errors.New("retention: unknown class")
	ErrTrigger      = errors.New("retention: unknown trigger")
	ErrParties      = errors.New("retention: record names no party")
	ErrNature       = errors.New("retention: relationship has no nature")
	ErrReason       = errors.New("retention: refusal has no reason")
	ErrBody         = errors.New("retention: record has no body to reconstruct from")
	ErrRef          = errors.New("retention: record has no reference to what it retains")
	ErrOccurred     = errors.New("retention: record has no event date")
	ErrClock        = errors.New("retention: retention clock has not started")
	ErrAssessment   = errors.New("retention: incomplete Art. 69(2) assessment")
	ErrRelationship = errors.New("retention: no such relationship")
	ErrClosed       = errors.New("retention: relationship is already closed")
	ErrNotFound     = errors.New("retention: no such record")
	ErrPurpose      = errors.New("retention: purpose is not money laundering or terrorist financing prevention")
	ErrFuture       = errors.New("retention: refusing to act on a future date")
	ErrExtension    = errors.New("retention: extension exceeds the five further years allowed")
	ErrDisposal     = errors.New("retention: disposal could not be proven")
	ErrConflict     = errors.New("retention: a different record is already retained under this identity")
	ErrStore        = errors.New("retention: the ledger could not be read or written")
)

// Class is what a record is.
type Class string

const (
	// ClassRelationship is a business relationship. Its retention clock starts
	// when it ends.
	ClassRelationship Class = "relationship"
	// ClassTransaction is a transaction, retained in enough detail to be
	// reconstructed. MLR 2017 reg. 40(2)(b).
	ClassTransaction Class = "transaction"
	// ClassAssessment is the Art. 69(2) assessment — retained whether or not it
	// produced a report. AMLR Art. 77(1)(b).
	ClassAssessment Class = "assessment"
	// ClassRefusal is a refusal to enter a relationship or carry out a
	// transaction. AMLR Art. 77(3) third trigger.
	ClassRefusal Class = "refusal"
)

// Trigger is which Art. 77(3) event starts the retention clock.
type Trigger string

const (
	// TriggerRelationshipEnd counts five years from the end of the relationship.
	TriggerRelationshipEnd Trigger = "relationship_end"
	// TriggerOccasional counts five years from the occasional transaction.
	TriggerOccasional Trigger = "occasional_transaction"
	// TriggerRefusal counts five years from the date of refusal.
	TriggerRefusal Trigger = "refusal"
)

// Result is what an assessment concluded.
type Result string

const (
	// Reported means a report went to the FIU.
	Reported Result = "reported"
	// NotReported means it did not, which is a retained decision and not a
	// deleted row. AMLR Art. 77(1)(b); JMLSG 6.32; JMLSG 8.6 on information not
	// acted upon.
	NotReported Result = "not_reported"
)

// Purpose is why a retained record is being read. The set is closed: retained
// personal data may be processed only to prevent money laundering and terrorist
// financing (AMLD4 Art. 41(2)), so a purpose outside it has no representation
// that the ledger will accept.
type Purpose string

const (
	// PurposeMonitoring is ongoing monitoring of the business relationship.
	PurposeMonitoring Purpose = "monitoring"
	// PurposeInvestigation is case work, up to and including a report.
	PurposeInvestigation Purpose = "investigation"
	// PurposeDisclosure is answering the FIU or a competent authority.
	PurposeDisclosure Purpose = "disclosure"
	// PurposeRetention is administering the retention period itself.
	PurposeRetention Purpose = "retention"
)

func (p Purpose) permitted() bool {
	switch p {
	case PurposeMonitoring, PurposeInvestigation, PurposeDisclosure, PurposeRetention:
		return true
	}
	return false
}

// Assessment is the Art. 69(2) record: the information and circumstances
// considered, and the result. It is retained whether or not it produced a
// report, and the reasons are required either way — a dismissed alert is a
// retained decision with its rationale.
type Assessment struct {
	// Alerts and Case are what was assessed.
	Alerts []string `json:"alerts,omitempty"`
	Case   string   `json:"case,omitempty"`
	// Considered is the information and circumstances that were considered.
	Considered []string `json:"considered"`
	// Result is what was concluded.
	Result Result `json:"result"`
	// Rationale is why. Required for both results: AMLR Art. 77(1)(b) retains
	// the result, and JMLSG 6.32 requires the reasons for not reporting.
	Rationale string `json:"rationale"`
	// By and At are who decided and when. An assessment is a person's decision.
	By string    `json:"by"`
	At time.Time `json:"at"`
}

func (a Assessment) validate() error {
	switch a.Result {
	case Reported, NotReported:
	default:
		return fmt.Errorf("%w: result %q", ErrAssessment, a.Result)
	}
	if len(a.Considered) == 0 {
		return fmt.Errorf("%w: nothing recorded as considered", ErrAssessment)
	}
	if a.Rationale == "" {
		return fmt.Errorf("%w: no rationale for %q", ErrAssessment, a.Result)
	}
	if a.By == "" {
		return fmt.Errorf("%w: no decider", ErrAssessment)
	}
	if a.At.IsZero() {
		return fmt.Errorf("%w: no decision date", ErrAssessment)
	}
	return nil
}

func (a Assessment) clone() Assessment {
	out := a
	out.Alerts = append([]string(nil), a.Alerts...)
	out.Considered = append([]string(nil), a.Considered...)
	return out
}

// Extended is a case-by-case extension of a retention period. AMLD4 Art. 40(1)
// permits a further period of at most five years, decided case by case, so an
// extension carries who decided it and why or it is refused.
type Extended struct {
	By     time.Duration `json:"by"`
	Reason string        `json:"reason"`
	Who    string        `json:"who"`
	At     time.Time     `json:"at"`
}

// Record is one retained record. It is append-only: nothing in this package
// rewrites a field of a written record except to start its retention clock and
// to extend its period, both of which only ever move the expiry later.
type Record struct {
	ID    string `json:"id"`
	Org   string `json:"org"`
	Class Class  `json:"class"`

	// Parties are every party the record concerns, and the keys the five-year
	// lookback is indexed by. They are pseudonyms in production: the ledger
	// never needs the cleartext to answer Art. 78.
	Parties []string `json:"parties"`

	// Ref is the caller's own reference for what is retained — a transaction id, a
	// case id. It is held in the clear, so it must be a synthetic reference and
	// never a direct identifier: it is the handle that ties a retained record to
	// the system it came from, and what a sealed body is bound to.
	Ref string `json:"ref,omitempty"`

	// Nature is the nature of the business relationship, which Art. 78 requires
	// to be reportable alongside its existence.
	Nature string `json:"nature,omitempty"`

	// Reason is why a transaction or relationship was refused.
	Reason string `json:"reason,omitempty"`

	// Relationship is the relationship this record is retained inside. Empty for
	// an occasional transaction or a refusal, which have their own clocks.
	Relationship string `json:"relationship,omitempty"`

	// Occurred is when the underlying event happened, or when the relationship
	// opened. Distinct from Start: an in-relationship transaction occurs years
	// before its retention clock starts.
	Occurred time.Time `json:"occurred"`
	// Ended is when a relationship ended. Zero while it is open.
	Ended time.Time `json:"ended,omitzero"`

	Trigger Trigger `json:"trigger"`
	// Start is when the retention clock started. Zero means it has not: an open
	// relationship, and everything retained inside it, has no expiry.
	Start    time.Time `json:"start,omitzero"`
	Extended *Extended `json:"extended,omitempty"`

	// Body is the record itself, opaque to the ledger and sealed by the caller.
	// It is what reconstructs the transaction, so it is never partial.
	Body []byte `json:"body,omitempty"`

	// Assessment is set on ClassAssessment records.
	Assessment *Assessment `json:"assessment,omitempty"`

	Written time.Time `json:"written"`

	// identity is what makes this record the same retained fact as another. It is
	// the ledger's to compute, like the clock, so it is not something a caller can
	// name. See [Record.identify].
	identity string
}

// identify is the record's identity: the thing that, repeated, means a client
// retried rather than that something happened twice.
//
// What is unique is a property of the class, and it is not unconditional:
//
//   - A transaction, a refusal and a relationship are identified by the reference
//     they retain. One record per transaction, per refused transaction, per
//     relationship — a resubmission of the same reference finds the record it
//     already wrote instead of a second copy of it.
//   - An assessment recurs. The same case is assessed again whenever new
//     information arrives, and every one of those decisions is retained whether or
//     not it produced a report (AMLR Art. 77(1)(b)), so the identity includes who
//     decided and when. Two decisions on one case are two records; a retry of one
//     decision is one.
//   - A record with no reference cannot be recognised, because there is nothing to
//     recognise it by. It is unique per write, which means a retry of it does
//     duplicate. Every writer in this codebase supplies a reference; validate
//     requires one of a transaction outright.
func (r Record) identify() string {
	if r.Ref == "" {
		return uuid.NewString()
	}
	if r.Class == ClassAssessment && r.Assessment != nil {
		return fmt.Sprintf("%s:%s:%s:%d", r.Class, r.Ref, r.Assessment.By, r.Assessment.At.UTC().UnixMilli())
	}
	return string(r.Class) + ":" + r.Ref
}

// digest is the retained fact, hashed. Two records with one identity are a retry
// only if they say the same thing; if they do not, the caller has two different
// facts under one name, and the ledger refuses rather than choosing between them
// or rewriting what it holds.
//
// Only what the caller supplied is hashed. The id, the clock and the write time
// are the ledger's, and hashing them would make every retry a conflict. Times are
// taken to the millisecond they are stored at, so a record and the same record
// read back digest alike.
func (r Record) digest() string {
	type fact struct {
		Class      Class
		Trigger    Trigger
		Parties    []string
		Ref        string
		Nature     string
		Reason     string
		Inside     string
		Occurred   int64
		Body       []byte
		Assessment *Assessment
	}
	f := fact{
		Class:      r.Class,
		Trigger:    r.Trigger,
		Parties:    slices.Sorted(slices.Values(r.Parties)),
		Ref:        r.Ref,
		Nature:     r.Nature,
		Reason:     r.Reason,
		Inside:     r.Relationship,
		Occurred:   r.Occurred.UTC().UnixMilli(),
		Body:       r.Body,
		Assessment: r.Assessment,
	}
	if f.Assessment != nil {
		a := f.Assessment.clone()
		a.At = a.At.UTC().Truncate(time.Millisecond)
		f.Assessment = &a
	}
	// A struct marshals in field order, so the encoding is stable; a map would not
	// be. Marshalling cannot fail on these types, and a digest that silently became
	// the empty string would make every record a retry of every other, so the
	// error is not ignored.
	encoded, err := json.Marshal(f)
	if err != nil {
		return fmt.Sprintf("unhashable:%v", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Expiry is when the record must be destroyed. Zero means the retention clock
// has not started, so the record has no expiry — an open business relationship
// is retained for as long as it is open, because the period runs from its end.
func (r Record) Expiry() time.Time {
	if r.Start.IsZero() {
		return time.Time{}
	}

	// The five years the AMLR requires. This is a floor: nothing below shortens it.
	floor := r.Start.AddDate(Period, 0, 0)

	at := floor
	if r.Extended != nil {
		at = at.Add(r.Extended.By)
	}
	// A case-by-case extension cannot exceed five further years.
	if limit := r.Start.AddDate(Period+Extension, 0, 0); at.After(limit) {
		at = limit
	}
	// MLR reg. 40(4): records of a transaction occurring during a relationship
	// need not be kept beyond ten years from the transaction. Applied only to an
	// extension, never below the floor — where the two instruments disagree the
	// mandatory floor wins, because destroying a record an instrument requires
	// kept is the worse of the two failures. A UK-only deployment wanting the
	// full effect of reg. 40(4) needs a compliance decision, not a code change.
	if r.Class == ClassTransaction && !r.Occurred.IsZero() {
		if c := r.Occurred.AddDate(Ceiling, 0, 0); at.After(c) && !c.Before(floor) {
			at = c
		}
	}
	return at
}

// Expired reports whether the retention period has run out.
func (r Record) Expired(now time.Time) bool {
	e := r.Expiry()
	return !e.IsZero() && !e.After(now)
}

// clone deep-copies a record. Reads hand out clones so that no reader can reach
// into the ledger and unmake part of a retained record.
func (r Record) clone() Record {
	out := r
	out.Parties = append([]string(nil), r.Parties...)
	out.Body = append([]byte(nil), r.Body...)
	if r.Extended != nil {
		e := *r.Extended
		out.Extended = &e
	}
	if r.Assessment != nil {
		a := r.Assessment.clone()
		out.Assessment = &a
	}
	return out
}

func (r Record) validate() error {
	if r.Org == "" {
		return ErrOrg
	}
	switch r.Class {
	case ClassRelationship, ClassTransaction, ClassAssessment, ClassRefusal:
	default:
		return fmt.Errorf("%w: %q", ErrClass, r.Class)
	}
	switch r.Trigger {
	case TriggerRelationshipEnd, TriggerOccasional, TriggerRefusal:
	default:
		return fmt.Errorf("%w: %q", ErrTrigger, r.Trigger)
	}
	if len(r.Parties) == 0 {
		return ErrParties
	}
	for _, p := range r.Parties {
		if p == "" {
			return ErrParties
		}
	}
	if r.Occurred.IsZero() {
		return ErrOccurred
	}

	switch r.Class {
	case ClassRelationship:
		if r.Nature == "" {
			return ErrNature
		}
	case ClassTransaction:
		if len(r.Body) == 0 {
			return ErrBody
		}
		// A transaction record with no reference cannot be tied back to the
		// transaction it is supposed to reconstruct.
		if r.Ref == "" {
			return ErrRef
		}
	case ClassRefusal:
		if r.Reason == "" {
			return ErrReason
		}
	case ClassAssessment:
		if r.Assessment == nil {
			return fmt.Errorf("%w: no assessment", ErrAssessment)
		}
		if err := r.Assessment.validate(); err != nil {
			return err
		}
	}
	return nil
}

// checkNow refuses a date the caller's own clock has not reached. Disposal is
// destruction, and a lie about the time must not be able to bring it forward.
func checkNow(now time.Time) error {
	if now.After(time.Now().UTC().Add(skew)) {
		return fmt.Errorf("%w: %s", ErrFuture, now.UTC().Format(time.RFC3339))
	}
	return nil
}
