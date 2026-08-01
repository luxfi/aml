// Package policy is the ladder from a probability to an action, as a value that
// can be recorded, versioned and replayed.
//
// WHY IT IS A VALUE AND NOT A CONSTANT. Where the line sits between letting a
// customer through and stopping them is not a technical fact — it is a business
// decision with a price on either side, and it belongs to the organisation
// making it. A threshold compiled into the scoring path is a decision nobody
// signed, cannot be varied per tenant, cannot be reviewed, and cannot be shown
// to have been in force on the day a particular customer was declined. So a
// policy here is an ordinary immutable value with a version, an author, a stated
// reason and a digest — a record, in the same sense a decision is a record.
//
// WHY A LADDER RATHER THAN FOUR NAMED THRESHOLDS. Named fields (challenge,
// review, restrict, block) hard-code one product's action vocabulary into the
// arithmetic, and then a tenant that wants three rungs has to leave one field at
// a sentinel value that every reader has to remember to check. A ladder is the
// general shape: an ordered list of (probability, action) steps, the last step
// at or below the probability wins, and below the first step the floor applies.
// Adding a rung is adding an element.
//
// ACTION NAMES ARE OPAQUE HERE, DELIBERATELY. This package validates the
// arithmetic — thresholds inside the unit interval, non-decreasing, no
// duplicates, no empty action — and knows nothing about what the actions mean.
// The closed vocabulary belongs to the surface that receives the policy off the
// wire, which is where a caller-supplied string has to be validated anyway.
// Restating the vocabulary here would put it in two places and let them drift.
//
// COST IS PART OF THE POLICY, AND IN INTEGERS. A threshold is only defensible
// against a stated price for being wrong in each direction, and those two prices
// are almost never equal: a missed fraud costs the disputed amount plus the
// network fee, a false decline costs a customer. They are int64 nano-units, not
// floats, because they are money and they are multiplied by counts.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Errors. Each names the property that failed, because a policy is refused at
// the moment somebody is trying to change one and "invalid" is not actionable.
var (
	ErrNoStage    = errors.New("policy: a policy applies to one lifecycle stage and this one names none")
	ErrNoRungs    = errors.New("policy: a ladder with no rungs decides nothing")
	ErrNoFloor    = errors.New("policy: the action below the first rung must be named")
	ErrRange      = errors.New("policy: a rung sits outside the unit interval, so no probability can reach it")
	ErrOrder      = errors.New("policy: the rungs are not ascending, so a higher probability would ask for less")
	ErrDuplicate  = errors.New("policy: two rungs at one probability, so which one applies is undecided")
	ErrNoAction   = errors.New("policy: a rung with no action")
	ErrSameAction = errors.New("policy: a rung asks for the action already in force below it")
	ErrCost       = errors.New("policy: a cost cannot be negative")
)

// Rung is one step of the ladder: at or above this probability, this action.
type Rung struct {
	// At is the probability boundary, in [0,1]. It is a PROBABILITY and not a
	// raw score: a ladder over an uncalibrated score is a ladder over units that
	// mean nothing, and the cost arithmetic below would be meaningless too.
	At float64 `json:"at"`
	// Action is what the organisation wants done at or above At. Opaque here;
	// the surface that accepts it validates it against its own closed set.
	Action string `json:"action"`
}

// Cost is what being wrong is worth, in nano-units of the tenant's accounting
// currency. Integers, because this is money and it is multiplied by counts.
//
// Miss is the price of letting through something that should have been stopped —
// on a payment lane, typically the disputed amount plus the network's dispute
// fee. Alarm is the price of stopping something that was fine — a customer's
// abandoned purchase, a support contact, and whatever share of those customers
// do not come back. Neither is knowable to three significant figures, and that
// is not an argument for leaving them out: stating them badly makes the
// trade-off arguable, and leaving them out makes it invisible.
type Cost struct {
	Miss  int64 `json:"miss"`
	Alarm int64 `json:"alarm"`
}

// Stated reports whether this cost says anything. A zero cost is not a free
// mistake, it is an unstated price, and every metric derived from it is absent
// rather than zero.
func (c Cost) Stated() bool { return c.Miss > 0 || c.Alarm > 0 }

// Policy is one tenant's ladder for one stage, as a record.
type Policy struct {
	// Stage is the lifecycle moment this governs — signup, payment, session,
	// payout. One ladder per stage: the price of a false decline at signup and at
	// payout are different numbers about different things.
	Stage string `json:"stage"`
	// Version is monotone within (tenant, stage). A change mints the next one;
	// nothing is ever edited, so what was in force on a given day is a lookup and
	// not a reconstruction.
	Version int `json:"version"`
	// At and By are when it took effect and who decided. A control that changed
	// and cannot say who changed it is a control nobody owns.
	At time.Time `json:"at"`
	By string    `json:"by"`
	// Reason is why. It is required of the surface, not of this package, for the
	// same reason the action vocabulary is: the wire is where a caller-supplied
	// string is checked.
	Reason string `json:"reason,omitempty"`
	// Floor is the action below the first rung.
	Floor string `json:"floor"`
	// Rungs is the ladder, ascending.
	Rungs []Rung `json:"rungs"`
	// Shadow observes without acting: every threshold is computed and recorded
	// and the action returned is always the floor. It is the second gate in
	// series with the engine's own Shadow, so a deployment-wide flag flipped by
	// mistake still cannot make one tenant act.
	Shadow bool `json:"shadow"`
	// Cost is what this tenant says being wrong is worth.
	Cost Cost `json:"cost"`
	// Digest identifies this policy exactly, so a decision can cite the ladder
	// that produced it and an auditor can pin it.
	Digest string `json:"digest"`
}

// Action is the ladder read at one probability: the highest rung at or below it,
// or the floor.
//
// In shadow it is always the floor, and that is the whole of the shadow
// mechanism — the caller still computes everything and still records what would
// have happened, because a shadow that skipped the computation would prove
// nothing about the live path.
func (p Policy) Action(prob float64) string {
	if p.Shadow {
		return p.Floor
	}
	action := p.Floor
	for _, r := range p.Rungs {
		if prob >= r.At {
			action = r.Action
			continue
		}
		break
	}
	return action
}

// Reached is the rung that decided, for a decision that wants to cite one.
// Absent when the floor applied, which is a real answer: nothing was reached.
func (p Policy) Reached(prob float64) *Rung {
	if p.Shadow {
		return nil
	}
	var hit *Rung
	for i := range p.Rungs {
		if prob >= p.Rungs[i].At {
			hit = &p.Rungs[i]
			continue
		}
		break
	}
	return hit
}

// Validate checks everything about a ladder that can be checked without knowing
// what the actions mean.
//
// Every one of these refusals is a real failure mode. A rung above one and no
// probability reaches it, so the action is unreachable and the operator believes
// a control is in place that is not. Rungs out of order and a higher probability
// asks for LESS, which is the opposite of a risk policy. Two rungs at one
// probability and which applies depends on iteration order. A rung repeating the
// action below it is dead: it changes nothing and hides that the ladder has
// fewer real steps than it appears to.
func (p Policy) Validate() error {
	if p.Stage == "" {
		return ErrNoStage
	}
	if p.Floor == "" {
		return ErrNoFloor
	}
	if len(p.Rungs) == 0 {
		return ErrNoRungs
	}
	if p.Cost.Miss < 0 || p.Cost.Alarm < 0 {
		return ErrCost
	}
	prev, prevAction := -1.0, p.Floor
	for i, r := range p.Rungs {
		switch {
		case r.Action == "":
			return fmt.Errorf("%w: rung %d", ErrNoAction, i)
		case math.IsNaN(r.At), r.At < 0, r.At > 1:
			return fmt.Errorf("%w: rung %d at %g", ErrRange, i, r.At)
		case r.At == prev:
			return fmt.Errorf("%w: rungs %d and %d at %g", ErrDuplicate, i-1, i, r.At)
		case r.At < prev:
			return fmt.Errorf("%w: rung %d at %g follows %g", ErrOrder, i, r.At, prev)
		case r.Action == prevAction:
			return fmt.Errorf("%w: rung %d repeats %q", ErrSameAction, i, r.Action)
		}
		prev, prevAction = r.At, r.Action
	}
	return nil
}

// Seal validates a ladder and returns it with its digest set. It is the ONE way
// to obtain a policy that carries an identity, so an unvalidated ladder cannot
// be recorded as if it had been agreed.
func Seal(p Policy) (Policy, error) {
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	p.Digest = Digest(p)
	return p, nil
}

// Digest identifies a ladder by what it DECIDES — stage, floor, rungs, shadow
// and cost — and deliberately not by who wrote it or when.
//
// Two policies with the same digest decide identically, so a backtest of one is
// a backtest of the other and a version bump that changed nothing is visible as
// such. Folding the author and the timestamp in would make every re-save a
// different policy and destroy exactly that property.
func Digest(p Policy) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%s|%t|%d|%d|", p.Stage, p.Floor, p.Shadow, p.Cost.Miss, p.Cost.Alarm)
	for _, r := range p.Rungs {
		fmt.Fprintf(h, "%s:%s|", strconv.FormatFloat(r.At, 'g', 17, 64), r.Action)
	}
	return hex.EncodeToString(h.Sum(nil))
}
