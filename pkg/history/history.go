// Package history retrieves a subject's past transactions.
//
// The package offers exactly one query: the window of events for a subject over
// a lookback period. Every behavioural measure the rule library needs — counts,
// sums, distinct counterparties, sub-threshold aggregation, dormancy, in-out
// alternation, deviation from a baseline — is a pure function of that window,
// computed in the measure package. One query keeps the tenant filter in one
// place and turns the measures into ordinary testable functions.
package history

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Subject kinds. A subject is whoever the aggregation is grouped by: the
// customer, one of their accounts, a counterparty, or a device or address they
// transacted from. Grouping by device and address is what surfaces several
// nominally-unrelated customers acting as one.
const (
	SubjectUser         = "user"
	SubjectAccount      = "account"
	SubjectCounterparty = "counterparty"
	SubjectDevice       = "device"
	SubjectAddress      = "address"
)

// Directions. Direction is required for in-out and pass-through detection:
// the pattern is defined by funds leaving shortly after they arrive, which is
// invisible if the store records only magnitude.
const (
	In  = "in"
	Out = "out"
)

// Subject identifies whose history to aggregate. OrgID is part of the identity,
// not a filter applied afterwards, so a query cannot accidentally aggregate
// across tenants.
type Subject struct {
	OrgID string
	Kind  string
	ID    string
}

// Event is one past transaction, reduced to the fields the measures read.
// USD holds the value converted at ingestion time, because a threshold measured
// today over a window of historical events must not silently move when a rate
// moves.
type Event struct {
	ID        string
	At        time.Time
	USD       float64
	Currency  string
	Direction string
	// One field per subject kind, so any kind can be both aggregated over and
	// counted as a dimension of another. Counting distinct users per device is what
	// surfaces several nominally-unrelated customers acting as one.
	User         string
	Account      string
	Counterparty string
	Device       string
	Address      string
	Jurisdiction string
	Symbol       string
}

// Store retrieves event windows.
//
// Window returns the subject's events with At in [now-lookback, now], most
// recent first. Implementations must scope to Subject.OrgID and must return an
// error rather than a partial window: a measure computed over a truncated
// window silently under-reports, which is the failure mode a monitoring system
// can least afford.
type Store interface {
	Window(ctx context.Context, subj Subject, lookback time.Duration) ([]Event, error)
}

// Kinds are the subject kinds a window can be grouped by.
var Kinds = []string{SubjectUser, SubjectAccount, SubjectCounterparty, SubjectDevice, SubjectAddress}

// Valid reports why a subject cannot name anyone, if it cannot.
//
// This is defined once and called by everything that builds or consumes a
// Subject, because each of these failures has the same consequence and it is the
// worst one available: a subject that names nobody yields an empty window, a rule
// over an empty window sees no activity, and no activity is indistinguishable
// from a customer who did nothing. Every one of them is an error instead.
func (s Subject) Valid() error {
	if !slices.Contains(Kinds, s.Kind) {
		return fmt.Errorf("history: unknown subject kind %q, want one of %s", s.Kind, strings.Join(Kinds, ", "))
	}
	if s.OrgID == "" {
		return fmt.Errorf("history: subject %q names no organisation, so the window would cross tenants", s.Kind)
	}
	if s.ID == "" {
		return fmt.Errorf("history: subject %q carries no identifier", s.Kind)
	}
	return nil
}
