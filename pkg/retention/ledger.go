// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Ledger holds an org's retained records, the party index the five-year
// lookback is answered from, and the relationship index the clock cascades
// through.
type Ledger struct {
	mu      sync.RWMutex
	records map[string]*Record
	// parties maps org and party to the records that name that party. AMLR
	// Art. 78 must be answered "fully and speedily", which is an index and not a
	// scan: a lookback examines one party's records, never the ledger.
	parties map[string][]string
	// inside maps a relationship to the records retained inside it, so ending the
	// relationship starts their clocks too.
	inside map[string][]string
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{
		records: make(map[string]*Record),
		parties: make(map[string][]string),
		inside:  make(map[string][]string),
	}
}

func partyKey(org, party string) string { return org + "\x00" + party }

// Retain writes a record and returns its id.
//
// The caller supplies what happened; the ledger supplies the clock. Which
// Art. 77(3) trigger applies is the caller's statement of fact — this is a
// refusal, this is an occasional transaction, this sits inside that relationship
// — and where the clock starts follows from it:
//
//   - a refusal and an occasional transaction start at the event;
//   - a record inside a relationship inherits that relationship's clock, which
//     starts when the relationship ends;
//   - a business relationship itself has no clock until [Ledger.Close].
//
// So Record.Start is not an input. A record arriving with one set is refused,
// because a caller that can name its own expiry can name one that never comes.
func (l *Ledger) Retain(r Record) (string, error) {
	if r.Org == "" {
		return "", ErrOrg
	}
	if !r.Start.IsZero() {
		return "", fmt.Errorf("%w: the clock is the ledger's to start", ErrClock)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	switch r.Trigger {
	case TriggerRefusal, TriggerOccasional:
		if r.Relationship != "" {
			return "", fmt.Errorf("%w: %s does not run inside a relationship", ErrTrigger, r.Trigger)
		}
		r.Start = r.Occurred
	case TriggerRelationshipEnd:
		if r.Relationship == "" {
			// The relationship itself. Its clock starts when it ends.
			if r.Class != ClassRelationship {
				return "", fmt.Errorf("%w: %s needs the relationship it is retained inside", ErrRelationship, r.Class)
			}
			break
		}
		rel, ok := l.records[r.Relationship]
		if !ok || rel.Org != r.Org || rel.Class != ClassRelationship {
			return "", fmt.Errorf("%w: %s", ErrRelationship, r.Relationship)
		}
		r.Start = rel.Start
	default:
		return "", fmt.Errorf("%w: %q", ErrTrigger, r.Trigger)
	}

	if err := r.validate(); err != nil {
		return "", err
	}

	r.ID = uuid.NewString()
	r.Written = time.Now().UTC()
	// One representation of a moment, everywhere in the ledger.
	r.Occurred = r.Occurred.UTC()
	if !r.Start.IsZero() {
		r.Start = r.Start.UTC()
	}

	stored := r.clone()
	l.records[r.ID] = &stored
	for _, p := range r.Parties {
		k := partyKey(r.Org, p)
		l.parties[k] = append(l.parties[k], r.ID)
	}
	if r.Relationship != "" {
		l.inside[r.Relationship] = append(l.inside[r.Relationship], r.ID)
	}
	return r.ID, nil
}

// Close ends a business relationship and starts the retention clock on it and on
// every record retained inside it. It returns how many records' clocks started,
// the relationship included: a caller that expects the cascade can check it.
func (l *Ledger) Close(org, id string, at time.Time) (int, error) {
	if org == "" {
		return 0, ErrOrg
	}
	if at.IsZero() {
		return 0, ErrOccurred
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	rel, ok := l.records[id]
	if !ok || rel.Org != org || rel.Class != ClassRelationship {
		return 0, fmt.Errorf("%w: %s", ErrRelationship, id)
	}
	if !rel.Ended.IsZero() {
		return 0, fmt.Errorf("%w: %s ended %s", ErrClosed, id, rel.Ended.UTC().Format(time.RFC3339))
	}
	if at.Before(rel.Occurred) {
		return 0, fmt.Errorf("%w: %s cannot end before it began", ErrOccurred, id)
	}

	at = at.UTC()
	rel.Ended = at
	rel.Start = at
	started := 1

	for _, child := range l.inside[id] {
		r, ok := l.records[child]
		if !ok {
			continue
		}
		// Only a clock that has not started; an extended or already-started
		// record is never moved backwards.
		if r.Start.IsZero() {
			r.Start = at
			started++
		}
	}
	return started, nil
}

// Extend applies a case-by-case extension of a retention period, capped at five
// further years (AMLD4 Art. 40(1)). It is refused without a reason and a
// decider, because "case by case" means somebody decided this case, and refused
// outright if the ask exceeds the cap rather than silently shortened to it.
func (l *Ledger) Extend(org, id string, by time.Duration, reason, who string) error {
	if org == "" {
		return ErrOrg
	}
	if by <= 0 || reason == "" || who == "" {
		return fmt.Errorf("%w: needs a positive period, a reason and a decider", ErrExtension)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	r, ok := l.records[id]
	if !ok || r.Org != org {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if r.Start.IsZero() {
		return fmt.Errorf("%w: %s", ErrClock, id)
	}
	if r.Extended != nil {
		return fmt.Errorf("%w: %s is already extended", ErrExtension, id)
	}
	limit := r.Start.AddDate(Period+Extension, 0, 0)
	if r.Start.AddDate(Period, 0, 0).Add(by).After(limit) {
		return fmt.Errorf("%w: %s past %s", ErrExtension, by, limit.UTC().Format(time.RFC3339))
	}

	r.Extended = &Extended{By: by, Reason: reason, Who: who, At: time.Now().UTC()}
	return nil
}

// Get returns one record for a permitted purpose. The returned record is a copy:
// a reader cannot reach back into the ledger and unmake part of it.
func (l *Ledger) Get(p Purpose, org, id string) (Record, error) {
	if !p.permitted() {
		return Record{}, fmt.Errorf("%w: %q", ErrPurpose, p)
	}
	if org == "" {
		return Record{}, ErrOrg
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	r, ok := l.records[id]
	if !ok || r.Org != org {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r.clone(), nil
}

// Each visits an org's records of one class, oldest event first, for a permitted
// purpose. An empty class visits every class. Iteration is ordered so that a
// file produced from it is reproducible.
func (l *Ledger) Each(p Purpose, org string, c Class, fn func(Record) error) error {
	if !p.permitted() {
		return fmt.Errorf("%w: %q", ErrPurpose, p)
	}
	if org == "" {
		return ErrOrg
	}

	// The order is settled first, on the cheap fields, and each record is copied
	// as it is visited. A walk therefore does not double the ledger in memory,
	// and it does not hold the lock across the caller's callback.
	type key struct {
		id string
		at time.Time
	}

	l.mu.RLock()
	keys := make([]key, 0, len(l.records))
	for id, r := range l.records {
		if r.Org != org || (c != "" && r.Class != c) {
			continue
		}
		keys = append(keys, key{id: id, at: r.Occurred})
	}
	l.mu.RUnlock()

	slices.SortFunc(keys, func(a, b key) int {
		if d := a.at.Compare(b.at); d != 0 {
			return d
		}
		return strings.Compare(a.id, b.id)
	})

	for _, k := range keys {
		l.mu.RLock()
		stored, ok := l.records[k.id]
		var r Record
		if ok {
			r = stored.clone()
		}
		l.mu.RUnlock()
		if !ok {
			// Disposed between settling the order and reading it. A destroyed
			// record is not visited.
			continue
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// Answer answers AMLR Art. 78: whether a business relationship with a named
// person is or was maintained within the prior five years, and its nature. It
// does not echo the party back, because the caller asked about a name and the
// ledger only ever saw a pseudonym.
type Answer struct {
	// Maintained is the answer: is, or was within the window.
	Maintained bool `json:"maintained"`
	// Current distinguishes "is" from "was".
	Current bool `json:"current"`
	// Natures is the nature of each relationship found, which Art. 78 requires
	// alongside its existence.
	Natures []string `json:"natures,omitempty"`
	// Records are the relationship records behind the answer, so the file can be
	// produced from it.
	Records []string  `json:"records,omitempty"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	// Examined is how many of this party's records were looked at. It is the
	// evidence that the answer came from an index and not from a scan: it does
	// not grow with the size of the ledger.
	Examined int `json:"examined"`
}

// Lookback answers Art. 78 for one party. The party is the pseudonym the records
// were indexed under, so the question is answerable without the ledger holding
// the name in the clear.
func (l *Ledger) Lookback(p Purpose, org, party string, now time.Time) (Answer, error) {
	if !p.permitted() {
		return Answer{}, fmt.Errorf("%w: %q", ErrPurpose, p)
	}
	if org == "" {
		return Answer{}, ErrOrg
	}
	if party == "" {
		return Answer{}, ErrParties
	}
	if err := checkNow(now); err != nil {
		return Answer{}, err
	}

	now = now.UTC()
	answer := Answer{
		From: now.AddDate(-Period, 0, 0),
		To:   now,
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	ids := l.parties[partyKey(org, party)]
	answer.Examined = len(ids)

	natures := make(map[string]bool)
	for _, id := range ids {
		r, ok := l.records[id]
		if !ok || r.Class != ClassRelationship {
			continue
		}
		// Open now, or ended inside the window.
		current := r.Ended.IsZero()
		if !current && r.Ended.Before(answer.From) {
			continue
		}
		if r.Occurred.After(now) {
			continue
		}
		answer.Maintained = true
		answer.Current = answer.Current || current
		answer.Records = append(answer.Records, id)
		if r.Nature != "" {
			natures[r.Nature] = true
		}
	}

	answer.Natures = make([]string, 0, len(natures))
	for n := range natures {
		answer.Natures = append(answer.Natures, n)
	}
	slices.Sort(answer.Natures)
	slices.Sort(answer.Records)
	if len(answer.Natures) == 0 {
		answer.Natures = nil
	}
	return answer, nil
}

// Disposal is what a disposal run proves. Its fields are meaningful only when
// Dispose returned no error: a run that cannot verify its own effect reports the
// error and no success.
type Disposal struct {
	At       time.Time `json:"at"`
	Examined int       `json:"examined"`
	Disposed []string  `json:"disposed,omitempty"`
}

// Dispose destroys every record whose retention period has expired, together
// with every index entry that referenced it, and proves it before returning.
//
// Fail-closed in three ways. It refuses a date the caller's own clock has not
// reached, so a clock lie cannot bring destruction forward. It verifies its own
// post-conditions and returns an error rather than a count if any of them fails.
// And it deletes whole records only — there is no partial disposal, which is
// what keeps deletion-on-expiry from becoming redaction.
func (l *Ledger) Dispose(now time.Time) (Disposal, error) {
	if err := checkNow(now); err != nil {
		return Disposal{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	d := Disposal{At: now.UTC(), Examined: len(l.records)}

	var doomed []string
	for id, r := range l.records {
		if r.Expired(now) {
			doomed = append(doomed, id)
		}
	}
	slices.Sort(doomed)

	for _, id := range doomed {
		r := l.records[id]
		delete(l.records, id)
		for _, party := range r.Parties {
			k := partyKey(r.Org, party)
			if rest := drop(l.parties[k], id); len(rest) == 0 {
				delete(l.parties, k)
			} else {
				l.parties[k] = rest
			}
		}
		if r.Relationship != "" {
			if rest := drop(l.inside[r.Relationship], id); len(rest) == 0 {
				delete(l.inside, r.Relationship)
			} else {
				l.inside[r.Relationship] = rest
			}
		}
		// A disposed relationship stops indexing what was retained inside it.
		// Anything of its own that outlives it did so by extension, on purpose.
		delete(l.inside, id)
	}
	d.Disposed = doomed

	if err := l.proveLocked(doomed, now); err != nil {
		return Disposal{}, err
	}
	return d, nil
}

// proveLocked is the post-condition of a disposal run. It is the difference
// between "the delete statement ran" and "the right records are gone".
func (l *Ledger) proveLocked(disposed []string, now time.Time) error {
	gone := make(map[string]bool, len(disposed))
	for _, id := range disposed {
		gone[id] = true
	}

	for _, id := range disposed {
		if _, ok := l.records[id]; ok {
			return fmt.Errorf("%w: %s survived disposal", ErrDisposal, id)
		}
		if _, ok := l.inside[id]; ok {
			return fmt.Errorf("%w: %s still indexes records retained inside it", ErrDisposal, id)
		}
	}
	for _, ids := range l.parties {
		for _, id := range ids {
			if gone[id] {
				return fmt.Errorf("%w: %s is still indexed under a party", ErrDisposal, id)
			}
			if _, ok := l.records[id]; !ok {
				return fmt.Errorf("%w: party index references vanished record %s", ErrDisposal, id)
			}
		}
	}
	for rel, ids := range l.inside {
		if _, ok := l.records[rel]; !ok {
			return fmt.Errorf("%w: relationship index references vanished record %s", ErrDisposal, rel)
		}
		for _, id := range ids {
			if _, ok := l.records[id]; !ok {
				return fmt.Errorf("%w: relationship %s references vanished record %s", ErrDisposal, rel, id)
			}
		}
	}
	for id, r := range l.records {
		if r.Expired(now) {
			return fmt.Errorf("%w: %s is expired and was not disposed", ErrDisposal, id)
		}
	}
	return nil
}

func drop(ids []string, id string) []string {
	out := ids[:0:0]
	for _, existing := range ids {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}

// Len is how many records the ledger holds.
func (l *Ledger) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.records)
}
