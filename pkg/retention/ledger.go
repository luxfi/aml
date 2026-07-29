// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"
)

// Ledger holds an org's retained records. It is the obligations and nothing else —
// which clock starts when, how far a closure cascades, what a read is allowed to
// be for, what a disposal has to prove — and the records themselves are in a
// [shelf]. So each obligation is written once and holds wherever the records are
// kept.
type Ledger struct{ shelf shelf }

// New returns a ledger that keeps records in memory, for exercising the
// obligations without a database. What is in it does not survive a restart, so it
// is not what an instance serves from.
func New() *Ledger { return &Ledger{shelf: newMemory()} }

// NewBase returns a ledger that keeps records in Base collections, which do
// survive a restart. [Ensure] has to have run first.
func NewBase(app core.App) *Ledger { return &Ledger{shelf: durable{app: app}} }

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
//
// A retry is not a second fact. A client that resends because it never saw the
// first response gets back the id it already has rather than a second record of
// one transaction — [Record.identify] is what "the same" means for each class,
// and [ErrConflict] is what happens when two different records claim one identity.
func (l *Ledger) Retain(r Record) (string, error) {
	if r.Org == "" {
		return "", ErrOrg
	}
	if !r.Start.IsZero() {
		return "", fmt.Errorf("%w: the clock is the ledger's to start", ErrClock)
	}

	var id string
	err := l.shelf.tx(func(s shelf) error {
		switch r.Trigger {
		case TriggerRefusal, TriggerOccasional:
			if r.Relationship != "" {
				return fmt.Errorf("%w: %s does not run inside a relationship", ErrTrigger, r.Trigger)
			}
			r.Start = r.Occurred
		case TriggerRelationshipEnd:
			if r.Relationship == "" {
				// The relationship itself. Its clock starts when it ends.
				if r.Class != ClassRelationship {
					return fmt.Errorf("%w: %s needs the relationship it is retained inside", ErrRelationship, r.Class)
				}
				break
			}
			rel, err := s.read(r.Org, r.Relationship)
			if err != nil || rel.Class != ClassRelationship {
				return fmt.Errorf("%w: %s", ErrRelationship, r.Relationship)
			}
			r.Start = rel.Start
		default:
			return fmt.Errorf("%w: %q", ErrTrigger, r.Trigger)
		}

		if err := r.validate(); err != nil {
			return err
		}

		// The read and the write are one transaction, so two retries arriving
		// together cannot both find nothing and both write.
		r.identity = r.identify()
		prior, err := s.retained(r.Org, r.identity)
		switch {
		case err == nil:
			if prior.digest() != r.digest() {
				return fmt.Errorf("%w: %s", ErrConflict, r.identity)
			}
			id = prior.ID
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}

		r.ID = uuid.NewString()
		r.Written = time.Now().UTC()
		// One representation of a moment, everywhere in the ledger.
		r.Occurred = r.Occurred.UTC()
		if !r.Start.IsZero() {
			r.Start = r.Start.UTC()
		}
		if err := s.insert(r); err != nil {
			return err
		}
		id = r.ID
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Close ends a business relationship and starts the retention clock on it and on
// every record retained inside it. It returns how many records' clocks started,
// the relationship included: a caller that expects the cascade can check it.
//
// The relationship and the cascade are one write. Half a cascade leaves records
// with no expiry at all, and a record with no expiry is never disposed of — of the
// two ways this can fail, that is the one nobody notices.
func (l *Ledger) Close(org, id string, at time.Time) (int, error) {
	if org == "" {
		return 0, ErrOrg
	}
	if at.IsZero() {
		return 0, ErrOccurred
	}

	started := 0
	err := l.shelf.tx(func(s shelf) error {
		started = 0
		rel, err := s.read(org, id)
		if err != nil || rel.Class != ClassRelationship {
			return fmt.Errorf("%w: %s", ErrRelationship, id)
		}
		if !rel.Ended.IsZero() {
			return fmt.Errorf("%w: %s ended %s", ErrClosed, id, rel.Ended.UTC().Format(time.RFC3339))
		}
		if at.Before(rel.Occurred) {
			return fmt.Errorf("%w: %s cannot end before it began", ErrOccurred, id)
		}

		ended := at.UTC()
		rel.Ended, rel.Start = ended, ended
		if err := s.update(rel); err != nil {
			return err
		}
		started = 1

		retained, err := s.inside(org, id)
		if err != nil {
			return err
		}
		for _, r := range retained {
			// Only a clock that has not started; an extended or already-started
			// record is never moved backwards.
			if !r.Start.IsZero() {
				continue
			}
			r.Start = ended
			if err := s.update(r); err != nil {
				return err
			}
			started++
		}
		return nil
	})
	if err != nil {
		return 0, err
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

	return l.shelf.tx(func(s shelf) error {
		r, err := s.read(org, id)
		if err != nil {
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
		return s.update(r)
	})
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
	return l.shelf.read(org, id)
}

// Each visits an org's records of one class, oldest event first, for a permitted
// purpose. An empty class visits every class. Iteration is ordered so that a
// file produced from it is reproducible.
func (l *Ledger) Each(p Purpose, org string, c Class, visit func(Record) error) error {
	if !p.permitted() {
		return fmt.Errorf("%w: %q", ErrPurpose, p)
	}
	if org == "" {
		return ErrOrg
	}
	return l.shelf.each(org, c, visit)
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

	found, err := l.shelf.party(org, party)
	if err != nil {
		return Answer{}, err
	}
	answer.Examined = len(found)

	natures := make(map[string]bool)
	for _, r := range found {
		if r.Class != ClassRelationship {
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
		answer.Records = append(answer.Records, r.ID)
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
//
// A run works in batches, each of them one write, because the size of a run is
// the ledger's and not one party's. A batch that comes back a second time is a
// batch that survived its own destruction, which is reported rather than retried
// forever.
func (l *Ledger) Dispose(now time.Time) (Disposal, error) {
	if err := checkNow(now); err != nil {
		return Disposal{}, err
	}

	held, err := l.shelf.count()
	if err != nil {
		return Disposal{}, err
	}
	d := Disposal{At: now.UTC(), Examined: held}

	var disposed []Record
	previous := ""
	for {
		doomed, err := l.shelf.expired(now, batch)
		if err != nil {
			return Disposal{}, err
		}
		if len(doomed) == 0 {
			break
		}
		if doomed[0].ID == previous {
			return Disposal{}, fmt.Errorf("%w: %s survived its own destruction", ErrDisposal, previous)
		}
		previous = doomed[0].ID

		if err := l.shelf.tx(func(s shelf) error { return s.erase(doomed) }); err != nil {
			return Disposal{}, err
		}
		disposed = append(disposed, doomed...)
	}

	for _, r := range disposed {
		d.Disposed = append(d.Disposed, r.ID)
	}
	slices.Sort(d.Disposed)

	if err := l.prove(disposed, now); err != nil {
		return Disposal{}, err
	}
	return d, nil
}

// prove is the post-condition of a disposal run. It is the difference between "the
// delete statement ran" and "the right records are gone", and it asks the store
// rather than the run's own bookkeeping, because a run that believes its own count
// is exactly the run that hides this bug.
func (l *Ledger) prove(disposed []Record, now time.Time) error {
	for _, r := range disposed {
		if _, err := l.shelf.read(r.Org, r.ID); !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: %s survived disposal", ErrDisposal, r.ID)
		}
		retained, err := l.shelf.inside(r.Org, r.ID)
		if err != nil {
			return err
		}
		if len(retained) > 0 {
			return fmt.Errorf("%w: %s still indexes %d records retained inside it", ErrDisposal, r.ID, len(retained))
		}
		for _, p := range r.Parties {
			found, err := l.shelf.party(r.Org, p)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(found, func(other Record) bool { return other.ID == r.ID }) {
				return fmt.Errorf("%w: %s is still indexed under a party", ErrDisposal, r.ID)
			}
		}
	}

	// Nothing expired is left anywhere.
	left, err := l.shelf.expired(now, 1)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		return fmt.Errorf("%w: %s is expired and was not disposed", ErrDisposal, left[0].ID)
	}

	// And no index entry outlived the record it named. An entry that did would keep
	// a destroyed record findable as evidence of a relationship, and it is the state
	// a disposal job leaves behind when it counts its own delete statements instead
	// of looking.
	orphans, err := l.shelf.orphans(1)
	if err != nil {
		return err
	}
	if len(orphans) > 0 {
		return fmt.Errorf("%w: the party index names %s, which is not there", ErrDisposal, orphans[0])
	}
	return nil
}

// Len is how many records the ledger holds, in every org.
func (l *Ledger) Len() (int, error) { return l.shelf.count() }
