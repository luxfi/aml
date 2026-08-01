package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/measure"
	"github.com/luxfi/aml/pkg/types"
)

// scope is the environment a rule expression is evaluated against. Its exported
// fields and methods are the whole language: Tx and Entity are the transaction
// under evaluation and its customer, and the methods are the evidence terms
// listed in the vocabulary table.
//
// A fresh scope is built for each transaction, which is what lets the methods
// hold per-transaction state — notably the window cache, so a rule set asking
// about the same subject and lookback ten times issues one query.
type scope struct {
	Tx     types.Transaction
	Entity types.Entity

	p     Providers
	ctx   context.Context
	cache map[string][]history.Event
}

// newScope builds the evaluation environment for one transaction.
func newScope(ctx context.Context, p Providers, tx types.Transaction, ent types.Entity) *scope {
	return &scope{
		Tx:     tx,
		Entity: ent,
		p:      p,
		ctx:    ctx,
		cache:  make(map[string][]history.Event, 4),
	}
}

// subject resolves a subject kind to the identifier carried by this transaction.
//
// An empty identifier is an error, never an empty history. A transaction with no
// device fingerprint must not quietly satisfy a rule that counts transactions
// per device: that would report "nothing to see" for precisely the transactions
// that withheld the evidence. Rules that aggregate over an optional field guard
// on its presence, so the error surfaces at authoring time rather than in
// production silence.
func (s *scope) subject(kind string) (history.Subject, error) {
	var id string
	switch kind {
	case history.SubjectUser:
		id = s.Tx.UserID
	case history.SubjectAccount:
		id = s.Tx.AccountID
	case history.SubjectCounterparty:
		id = s.Tx.Counterparty
	case history.SubjectDevice:
		id = s.Tx.DeviceFingerprint
	case history.SubjectAddress:
		id = s.Tx.IPAddress
	}
	subj := history.Subject{OrgID: s.Tx.OrgID, Kind: kind, ID: id}
	if err := subj.Valid(); err != nil {
		return history.Subject{}, fmt.Errorf("%w (transaction %s)", err, s.Tx.ID)
	}
	return subj, nil
}

// window fetches and memoises the subject's events over the lookback.
func (s *scope) window(kind, lookback string) ([]history.Event, error) {
	d, err := parseWindow(lookback)
	if err != nil {
		return nil, err
	}
	subj, err := s.subject(kind)
	if err != nil {
		return nil, err
	}
	key := kind + "/" + lookback
	if evs, ok := s.cache[key]; ok {
		return evs, nil
	}
	evs, err := s.p.History.Window(s.ctx, subj, d)
	if err != nil {
		return nil, fmt.Errorf("history window %s over %s: %w", kind, lookback, err)
	}
	s.cache[key] = evs
	return evs, nil
}

// parseWindow reads a lookback written as an integer and a unit: m, h, or d.
//
// It rejects anything else rather than assuming a unit. A rule reading
// Count("user", "7") is ambiguous between seven minutes and seven days, and a
// monitoring window off by three orders of magnitude is a control that does not
// work while appearing to.
func parseWindow(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("window %q: want an integer and a unit, as in 24h or 7d", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return 0, fmt.Errorf("window %q: %q is not an integer", s, s[:len(s)-1])
	}
	if n <= 0 {
		return 0, fmt.Errorf("window %q: must be positive", s)
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("window %q: unit %q is not m, h, or d", s, s[len(s)-1:])
	}
}

// dimension names the event field a distinct-count or membership test groups by.
func dimension(name string) (func(history.Event) string, error) {
	switch name {
	case "counterparty":
		return func(e history.Event) string { return e.Counterparty }, nil
	case "account":
		return func(e history.Event) string { return e.Account }, nil
	case "device":
		return func(e history.Event) string { return e.Device }, nil
	case "address":
		return func(e history.Event) string { return e.Address }, nil
	case "jurisdiction":
		return func(e history.Event) string { return e.Jurisdiction }, nil
	case "currency":
		return func(e history.Event) string { return e.Currency }, nil
	case "symbol":
		return func(e history.Event) string { return e.Symbol }, nil
	default:
		return nil, fmt.Errorf("unknown dimension %q", name)
	}
}

// USD is this transaction's value in USD.
func (s *scope) USD() (float64, error) {
	return s.p.Rate.USD(s.ctx, s.Tx.Notional, s.Tx.Currency)
}

// Count is the number of the subject's transactions over the lookback.
func (s *scope) Count(kind, lookback string) (int, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	return measure.Count(evs), nil
}

// Sum is the subject's total USD value over the lookback.
func (s *scope) Sum(kind, lookback string) (float64, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	return measure.Sum(evs), nil
}

// Max is the subject's largest single USD value over the lookback.
func (s *scope) Max(kind, lookback string) (float64, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	return measure.Max(evs), nil
}

// Day is the subject's total for the calendar day this transaction falls on, in
// the institution's zone, including this transaction.
//
// The lookback is fixed at 48 hours because a calendar day cannot span more than
// that in any zone, and asking for a longer window would fetch events that the
// day filter then discards.
func (s *scope) Day(kind string) (float64, error) {
	evs, err := s.window(kind, "48h")
	if err != nil {
		return 0, err
	}
	usd, err := s.USD()
	if err != nil {
		return 0, err
	}
	return measure.Day(evs, s.p.zone(), s.Tx.Timestamp) + usd, nil
}

// Distinct counts the subject's distinct values of a dimension over the lookback.
func (s *scope) Distinct(kind, dim, lookback string) (int, error) {
	key, err := dimension(dim)
	if err != nil {
		return 0, err
	}
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	return measure.Distinct(evs, key), nil
}

// Seen reports whether the subject has already transacted against a value of a
// dimension within the lookback. Its negation is what makes a counterparty new.
func (s *scope) Seen(kind, dim, value, lookback string) (bool, error) {
	if value == "" {
		return false, fmt.Errorf("Seen: value for dimension %q is empty", dim)
	}
	key, err := dimension(dim)
	if err != nil {
		return false, err
	}
	evs, err := s.window(kind, lookback)
	if err != nil {
		return false, err
	}
	for _, e := range evs {
		if strings.EqualFold(key(e), value) {
			return true, nil
		}
	}
	return false, nil
}

// Structured reports sub-threshold transactions aggregating to the threshold
// over the lookback.
func (s *scope) Structured(kind, lookback string, threshold float64, min int) (bool, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return false, err
	}
	return measure.Structured(evs, threshold, min), nil
}

// Near reports whether this transaction's value sits in the band just below a
// threshold.
func (s *scope) Near(threshold, band float64) (bool, error) {
	usd, err := s.USD()
	if err != nil {
		return false, err
	}
	return measure.Near(usd, threshold, band), nil
}

// InOut reports funds arriving and leaving the subject over the lookback,
// retaining no more than the residue fraction.
func (s *scope) InOut(kind, lookback string, min, residue float64) (bool, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return false, err
	}
	return measure.InOut(evs, min, residue), nil
}

// Dormant is the number of whole days in the gap this transaction broke, within
// the lookback.
func (s *scope) Dormant(kind, lookback string) (float64, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	return measure.Dormancy(evs).Hours() / 24, nil
}

// Round is the fraction of the subject's transactions over the lookback whose
// value is an exact multiple of unit.
func (s *scope) Round(kind, lookback string, unit float64) (float64, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	return measure.Round(evs, unit), nil
}

// Deviation is this transaction's robust score against the subject's own
// baseline over the lookback. It is zero when the baseline is too thin or has no
// spread, so a rule comparing it against a positive threshold declines to fire
// rather than guessing.
func (s *scope) Deviation(kind, lookback string, min int) (float64, error) {
	evs, err := s.window(kind, lookback)
	if err != nil {
		return 0, err
	}
	usd, err := s.USD()
	if err != nil {
		return 0, err
	}
	return measure.Deviation(evs, usd, min), nil
}

// Screened reports whether a name matches a screening list of the given class.
func (s *scope) Screened(name, class string) (bool, error) {
	if name == "" {
		return false, fmt.Errorf("Screened: name is empty")
	}
	switch class {
	case ClassSanctions, ClassPEP:
	default:
		return false, fmt.Errorf("unknown screening class %q: want %s or %s", class, ClassSanctions, ClassPEP)
	}
	hit, err := s.p.Screen.Hit(s.ctx, name, class)
	if err != nil {
		return false, err
	}
	return hit.Matched, nil
}

// Listed reports whether a value is on one of this institution's own lists.
//
// The tenant is the transaction's, never the caller's and never an argument: a
// rule that could name the org it reads lists from would be a cross-tenant read
// the rule author asserted for itself.
//
// An empty value is an error and not a miss, for the same reason Seen refuses
// one. A transaction carrying no address must not quietly satisfy — or quietly
// fail — a rule that checks addresses against a deny list: that reports "nothing
// to see" for precisely the transactions that withheld the evidence.
func (s *scope) Listed(name, value string) (bool, error) {
	if name == "" {
		return false, fmt.Errorf("Listed: no list named")
	}
	if value == "" {
		return false, fmt.Errorf("Listed: value for list %q is empty", name)
	}
	return s.p.Lists.Listed(s.ctx, s.Tx.OrgID, name, value)
}

// Tier is the published risk tier of a jurisdiction: empty, monitoring, or
// action.
func (s *scope) Tier(code string) (string, error) {
	if code == "" {
		return TierNone, nil
	}
	return s.p.Reference.Jurisdiction(code)
}
